// Package projectctl 是 Work Deck 与 Graph 控制器之间的唯一通道。
//
// 包外的代码不得自己去 exec projectctl、不得读 events.jsonl、不得碰 ledger-head.json。
// 想加一条新的数据来源，就加在这个包里，别处不开第二个口子。
//
// n035 起只读：run() 只认四个不写任何文件的子命令。n042 起有且只有一个写入口 Client.Start（见文件末尾），
// owner 在界面上选定并确认派工之后，由派工器在上游会话起来、确认是 agent 之后调用（n044）；开工出岔子时它只用只读的
// verify-chain 与状态投影对账，不另写任何东西。submit / review / block 等写命令仍然没有方法、run() 也拒绝。
package projectctl

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// ReadOnlyCommands 是本包允许执行的全部子命令。
//
// 这四个命令都经过实测：跑完之后项目目录的全文件哈希不变。start / submit / review /
// block / resume / fail / init / status 会写账本或重写状态投影，一律不在此列，run() 都拒绝。
// 其中只有 start 另有专门的方法 Start（n042，见文件末尾）；其余写命令本包不提供任何方法。
var ReadOnlyCommands = []string{"validate", "next", "brief", "verify-chain"}

func isReadOnly(command string) bool {
	for _, c := range ReadOnlyCommands {
		if c == command {
			return true
		}
	}
	return false
}

// ErrWriteCommandRefused 表示有人试图通过本包执行一个会写文件的子命令。
type ErrWriteCommandRefused struct{ Command string }

func (e *ErrWriteCommandRefused) Error() string { return e.I18n().Error() }

// I18n 交出码：英文界面按码翻译。
func (e *ErrWriteCommandRefused) I18n() *i18n.Error {
	return i18n.E("err.projectctl.refused", e.Command)
}

// Client 执行 projectctl 并把输出翻译成 Work Deck 的类型。
type Client struct {
	// Python 解释器。
	Python string
	// projectctl.py 的路径。
	Script string
	// 目标项目根目录。
	Project string
	// 单条命令的超时。超时按「读不到」处理，不猜。
	// 控制器对项目目录加锁，节点多的时候单条命令要排很久的队，所以给得宽。
	Timeout time.Duration
}

// NewClient 按给定项目根构造一个客户端，其余字段取默认值。
func NewClient(project, script string) *Client {
	return &Client{
		Python:  "python3",
		Script:  script,
		Project: project,
		Timeout: 60 * time.Second,
	}
}

// DefaultScript 找 projectctl.py。先看环境变量，再按工作区的固定布局猜。
// 都找不到就返回错误，不返回一个能跑但指向别处的路径。
func DefaultScript() (string, error) {
	if p := os.Getenv("WORKDECK_PROJECTCTL"); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", i18n.E("err.projectctl.defaultscript.1", err)
		}
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", i18n.E("err.projectctl.defaultscript.2", err)
	}
	candidate := filepath.Join(home, "ai", "01.assets", "project-control", "projectctl.py")
	if _, err := os.Stat(candidate); err != nil {
		return "", i18n.E("err.projectctl.defaultscript.3", "projectctl.py", err)
	}
	return candidate, nil
}

// envelope 是 projectctl --format json 的统一外壳。
type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

// run 执行一个只读子命令，返回 result 字段的原始 JSON。
func (c *Client) run(ctx context.Context, command string, args ...string) (json.RawMessage, error) {
	if !isReadOnly(command) {
		return nil, &ErrWriteCommandRefused{Command: command}
	}
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}

	full := append([]string{c.Script, "--format", "json", command}, args...)
	cmd := exec.CommandContext(ctx, c.Python, full...)
	cmd.Dir = c.Project
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	// 子进程的输出是外部原文：伪造的打码标记在这里换掉（返工 4）
	outBytes, errText := i18n.NeutralizeBytes(stdout.Bytes()), i18n.Neutralize(stderr.String())

	// verify-chain 审计不过时退出码是 2，但 stdout 仍是合法结果，要照常解析。
	var env envelope
	if jsonErr := json.Unmarshal(outBytes, &env); jsonErr != nil {
		if runErr != nil {
			return nil, i18n.E("err.projectctl.run.1", command, runErr, truncate(errText, 400))
		}
		return nil, i18n.E("err.projectctl.run.2", command, jsonErr)
	}
	if !env.OK {
		return nil, i18n.E("err.projectctl.run.3", command, truncate(env.Error, 400))
	}
	return env.Result, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// StartCommand 是本包唯一会写账本的子命令（n042）。
//
// n035 起本包对 Graph 控制器只读。n040 终审发现：Work Deck 派了工，Graph 却没有 attempt，
// 要 owner 另开终端手工 start；恢复过的节点还被界面默认成 attempt 1，与账本错配。
// n042 合同要求 attempt 由 projectctl 建立并与派工对齐，所以只放开这一条：
// 由 owner 在界面上选定模型并按回车之后调用，一次派工调一次。submit / review / block 等仍一律拒绝，
// run() 的只读白名单不变。
const StartCommand = "start"

// StartRequest 是一次开工要登记的内容，字段一一对应 projectctl start 的参数。
type StartRequest struct {
	NodeID     string
	Actor      string
	Role       string
	RouteClass string
	Model      string
	Reason     string
}

// StartResult 是 projectctl start 的回执。Attempt 是账本给的号，派工就用它。
type StartResult struct {
	NodeID  string `json:"node_id"`
	State   string `json:"state"`
	Attempt int    `json:"attempt"`
	EventID string `json:"event_id"`
	// Reconciled 为真：回执没拿到或不可信，是开工后对账确认的（n044 attempt 3）。这时 EventID 为空。
	Reconciled bool `json:"-"`
}

// ErrStartNotStarted 表示确定没开工：开工前后账本事件数没变（n044 attempt 3）。刚起的会话可以放心停掉。
// Start 返回的其余错误都是「结果不明」：控制器可能已经把开工写进账本，只是回执没到。
type ErrStartNotStarted struct{ Cause error }

func (e *ErrStartNotStarted) Error() string { return e.Cause.Error() }
func (e *ErrStartNotStarted) Unwrap() error { return e.Cause }

// reconcileTimeout 是开工出岔子之后对账最多等多久。
const reconcileTimeout = time.Minute

// Start 对一个节点开工，返回账本分配的 attempt。字段缺一个就不调。
//
// 开工前先记下账本事件数。开工的回执不完整、不可信、或控制器报失败时，不猜，回头对账（n044 attempt 2 评审 blocker 2）：
// 子进程超时或异常退出时，控制器可能已经写进了账本，只是回执没到。
//   - 事件数没变：确定没开工（ErrStartNotStarted）。
//   - 事件数变了，状态投影显示这个节点正由本次的 actor 跑着：确认开工，用投影里的 attempt（Reconciled）。
//   - 别的情况：结果不明，照实报错。
//
// 开工前核对不了账本就不开工：出岔子时没法对账。
func (c *Client) Start(ctx context.Context, r StartRequest) (StartResult, error) {
	for _, f := range []struct {
		name any
		v    string
	}{{i18n.E("err.projectctl.start.node"), r.NodeID}, {"actor", r.Actor}, {"role", r.Role},
		{"route class", r.RouteClass}, {"model", r.Model}, {"reason", r.Reason}} {
		if strings.TrimSpace(f.v) == "" {
			return StartResult{}, &ErrStartNotStarted{i18n.E("err.projectctl.start.missing", f.name)}
		}
	}
	before, err := c.VerifyChain(ctx)
	if err != nil {
		return StartResult{}, &ErrStartNotStarted{i18n.E("err.projectctl.start.noverify", err)}
	}
	runCtx := ctx
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	args := []string{c.Script, "--format", "json", StartCommand, r.NodeID,
		"--actor", r.Actor, "--role", r.Role, "--route-class", r.RouteClass, "--model", r.Model, "--reason", r.Reason}
	cmd := exec.CommandContext(runCtx, c.Python, args...)
	cmd.Dir = c.Project
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	outBytes, errText := i18n.NeutralizeBytes(stdout.Bytes()), i18n.Neutralize(stderr.String()) // 子进程的输出是外部原文（返工 4）
	reconcile := func(cause error) (StartResult, error) { return c.reconcileStart(ctx, r, before.Events, cause) }
	var env envelope
	if err := json.Unmarshal(outBytes, &env); err != nil {
		if runErr != nil {
			return reconcile(i18n.E("err.projectctl.run.1", StartCommand, runErr, truncate(errText, 400)))
		}
		return reconcile(i18n.E("err.projectctl.run.2", StartCommand, err))
	}
	if !env.OK {
		return reconcile(i18n.E("err.projectctl.run.3", StartCommand, truncate(env.Error, 400)))
	}
	// 回执说 ok、进程却非零退出：两边对不上就不信，不拿这个 attempt 去派（n042 attempt 1 评审次要项）。
	if runErr != nil {
		return reconcile(i18n.E("err.projectctl.start.exitbad", runErr, truncate(errText, 400)))
	}
	var res StartResult
	if err := json.Unmarshal(env.Result, &res); err != nil {
		return reconcile(i18n.E("err.projectctl.start.badreceipt", err))
	}
	if res.Attempt <= 0 || res.NodeID != r.NodeID {
		return reconcile(i18n.E("err.projectctl.start.mismatch", res.NodeID, res.Attempt))
	}
	return res, nil
}

// reconcileStart 在开工出岔子之后对账。它另起一个 context：出岔子的原因常常就是调用方的 context 到期了。
func (c *Client) reconcileStart(ctx context.Context, r StartRequest, before int, cause error) (StartResult, error) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reconcileTimeout)
	defer cancel()
	after, err := c.VerifyChain(rctx)
	if err != nil {
		return StartResult{}, i18n.E("err.projectctl.reconcile.noverify", cause, err)
	}
	if after.Events == before {
		return StartResult{}, &ErrStartNotStarted{i18n.E("err.projectctl.reconcile.notstarted", cause, before)}
	}
	p, err := LoadProjection(c.Project)
	if err == nil && p.States[r.NodeID] == StateRunning && p.Actors[r.NodeID] == r.Actor && p.Attempts[r.NodeID] > 0 {
		return StartResult{NodeID: r.NodeID, State: string(StateRunning), Attempt: p.Attempts[r.NodeID], Reconciled: true}, nil
	}
	return StartResult{}, i18n.E("err.projectctl.reconcile.unknown", cause, before, after.Events)
}
