// Package projectctl 是 Work Deck 与 Graph 控制器之间的唯一通道。
//
// 包外的代码不得自己去 exec projectctl、不得读 events.jsonl、不得碰 ledger-head.json。
// 想加一条新的数据来源，就加在这个包里，别处不开第二个口子。
//
// 本轮（n035）是只读的：这个包只认四个不写任何文件的子命令，写命令在这里根本没有
// 对应的方法，调用方即使想写也没有入口。
package projectctl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ReadOnlyCommands 是本包允许执行的全部子命令。
//
// 这四个命令都经过实测：跑完之后项目目录的全文件哈希不变。start / submit / review /
// block / resume / fail / init / status 会写账本或重写状态投影，一律不在此列，
// 本包也不提供调用它们的任何方法。
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
	return fmt.Sprintf("拒绝执行非只读子命令 %q：Work Deck 本轮只读，不写 Graph 账本", e.Command)
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
	Timeout time.Duration
}

// NewClient 按给定项目根构造一个客户端，其余字段取默认值。
func NewClient(project, script string) *Client {
	return &Client{
		Python:  "python3",
		Script:  script,
		Project: project,
		Timeout: 20 * time.Second,
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
