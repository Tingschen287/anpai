// Package projectctl 是 Work Deck 与 Graph 控制器之间的唯一通道。
//
// 包外的代码不得自己去 exec projectctl、不得读 events.jsonl、不得碰 ledger-head.json。
// 想加一条新的数据来源，就加在这个包里，别处不开第二个口子。
//
// n035 起只读：run() 只认四个不写任何文件的子命令。n042 起有且只有一个写入口 Client.Start（见文件末尾），
// 由界面在 owner 选定模型、按回车之后调用；submit / review / block 等写命令仍然没有方法、run() 也拒绝。
package projectctl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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

func (e *ErrWriteCommandRefused) Error() string {
	return fmt.Sprintf("拒绝执行非只读子命令 %q：Work Deck 对 Graph 控制器只读，唯一的写入口是派工前的 Start", e.Command)
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
			return "", fmt.Errorf("WORKDECK_PROJECTCTL 指向的文件不可用：%w", err)
		}
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法确定用户主目录：%w", err)
	}
	candidate := filepath.Join(home, "ai", "01.assets", "project-control", "projectctl.py")
	if _, err := os.Stat(candidate); err != nil {
		return "", fmt.Errorf("找不到 projectctl.py，请用 WORKDECK_PROJECTCTL 指定：%w", err)
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

	// verify-chain 审计不过时退出码是 2，但 stdout 仍是合法结果，要照常解析。
	var env envelope
	if jsonErr := json.Unmarshal(stdout.Bytes(), &env); jsonErr != nil {
		if runErr != nil {
			return nil, fmt.Errorf("projectctl %s 执行失败：%w（stderr: %s）",
				command, runErr, truncate(stderr.String(), 400))
		}
		return nil, fmt.Errorf("projectctl %s 的输出不是合法 JSON：%w", command, jsonErr)
	}
	if !env.OK {
		return nil, fmt.Errorf("projectctl %s 返回失败：%s", command, truncate(env.Error, 400))
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

// StartResult 是 projectctl start 的回执。Attempt 是账本给出的号，派工就用它。
type StartResult struct {
	NodeID  string `json:"node_id"`
	State   string `json:"state"`
	Attempt int    `json:"attempt"`
	EventID string `json:"event_id"`
}

// Start 对一个节点开工，返回账本分配的 attempt。字段缺一个就不调。
func (c *Client) Start(ctx context.Context, r StartRequest) (StartResult, error) {
	for name, v := range map[string]string{"节点": r.NodeID, "actor": r.Actor, "role": r.Role,
		"route class": r.RouteClass, "model": r.Model, "reason": r.Reason} {
		if strings.TrimSpace(v) == "" {
			return StartResult{}, fmt.Errorf("开工缺少 %s，不调 projectctl start", name)
		}
	}
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	args := []string{c.Script, "--format", "json", StartCommand, r.NodeID,
		"--actor", r.Actor, "--role", r.Role, "--route-class", r.RouteClass, "--model", r.Model, "--reason", r.Reason}
	cmd := exec.CommandContext(ctx, c.Python, args...)
	cmd.Dir = c.Project
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	var env envelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		if runErr != nil {
			return StartResult{}, fmt.Errorf("projectctl start 执行失败：%w（stderr: %s）", runErr, truncate(stderr.String(), 400))
		}
		return StartResult{}, fmt.Errorf("projectctl start 的输出不是合法 JSON：%w", err)
	}
	if !env.OK {
		return StartResult{}, fmt.Errorf("projectctl start 返回失败：%s", truncate(env.Error, 400))
	}
	// 回执说 ok、进程却非零退出：两边对不上就不信，不拿这个 attempt 去派（n042 attempt 1 评审次要项）。
	if runErr != nil {
		return StartResult{}, fmt.Errorf("projectctl start 回执说成功，但进程退出失败：%w（stderr: %s）", runErr, truncate(stderr.String(), 400))
	}
	var res StartResult
	if err := json.Unmarshal(env.Result, &res); err != nil {
		return StartResult{}, fmt.Errorf("解析 projectctl start 回执失败：%w", err)
	}
	if res.Attempt <= 0 || res.NodeID != r.NodeID {
		return StartResult{}, fmt.Errorf("projectctl start 回执对不上：节点 %q attempt %d", res.NodeID, res.Attempt)
	}
	return res, nil
}
