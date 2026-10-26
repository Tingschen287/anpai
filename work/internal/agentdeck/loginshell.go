package agentdeck

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"syscall"
	"time"
)

// RunLoginShell 运行解析会话 PATH 用的登录交互 shell（sessionenv.RunFunc 的生产实现，n060 段 4）。
// exec 只许出现在两个 adapter 包里，所以起进程写在这里，sessionenv 只拿它的输出。
//
//   - stdin 是 /dev/null；标准错误丢掉；标准输出最多 64 KiB，超了当失败；
//   - 新会话（Setsid）：既是新的进程组，超时、取消时整组杀掉（rc 里起的后台进程也一起收拾），又没有控制终端——
//     deckd 跑在 tmux 窗格里（有控制终端），交互式 zsh 只开新进程组而留在原会话时，碰终端会被 SIGTTOU 停住，一直等到超时（e2e61 实测）；
//   - WaitDelay：后台进程占着标准输出管道也不会让 Wait 一直等。
//
// shell、args 由 sessionenv 给出（shell 是它从 $SHELL 与系统常见位置里挑出的、名字是认得的 shell）。
func RunLoginShell(ctx context.Context, shell string, args []string, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, shell, args...)
	cmd.Env = env
	cmd.Stdin = nil // /dev/null
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = 500 * time.Millisecond
	var out loginBuf
	out.max = maxLoginShellOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return out.b.Bytes(), err
	}
	if out.over {
		return nil, errors.New("output too large")
	}
	return out.b.Bytes(), nil
}

// maxLoginShellOutput 是登录 shell 输出的上限。
const maxLoginShellOutput = 64 << 10

// loginBuf 只收前 max 个字节，超出的丢掉并记下。
type loginBuf struct {
	b    bytes.Buffer
	max  int
	over bool
}

func (l *loginBuf) Write(p []byte) (int, error) {
	if room := l.max - l.b.Len(); room > 0 {
		if len(p) > room {
			l.b.Write(p[:room])
			l.over = true
		} else {
			l.b.Write(p)
		}
	} else if len(p) > 0 {
		l.over = true
	}
	return len(p), nil
}
