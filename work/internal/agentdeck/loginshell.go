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
//   - 新会话（Setsid）：shell 是新会话、新进程组的首进程（pid 就是进程组号），又没有控制终端——
//     deckd 跑在 tmux 窗格里（有控制终端），交互式 zsh 只开新进程组而留在原会话时，碰终端会被 SIGTTOU 停住，一直等到超时（e2e61 实测）；
//   - WaitDelay：后台进程占着标准输出管道也不会让 Wait 一直等。
//
// 清理（返工 R2）：不论怎么结束——正常退出、非零退出、I/O 超时（WaitDelay 到期，Run 返回 exec.ErrWaitDelay）、输出超限、ctx 超时或取消——
// 函数返回前一律向原进程组发 SIGKILL，rc 里起的后台进程（`sleep 30 &` 之类）不会残留。不能只靠 CommandContext 的 Cancel：
// 它只在 ctx 结束时才触发，shell 已经退出、后台进程占着管道而 WaitDelay 到期时它根本不会被调用。也不靠延长等待来掩盖。
// 输出一超限就立刻取消（不等 shell 自己结束）。
//
// 能力边界（如实）：只清理原进程组。rc 里主动 setsid / 双 fork / 交给 launchd 之类脱离进程组的后代已经不属于这个组，这里清不掉，
// 也不承诺清掉整棵派生树；真要保证需要平台级的进程生命周期管理（macOS 没有 cgroup），不在本函数的能力内。
// 另：向进程组发信号发生在 shell 被回收之后；只要组里还有进程，内核不会把这个号再分给别的进程，组里已经没有进程时 kill 返回 ESRCH，什么都不会发生。
//
// shell、args 由 sessionenv 给出（shell 是它从 $SHELL 与系统常见位置里挑出的、名字是认得的 shell）。
func RunLoginShell(ctx context.Context, shell string, args []string, env []string) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
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
	out.onOver = cancel // 超限：马上收拾整组，不等 shell 自己结束
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	pgid := cmd.Process.Pid
	// 任何结束路径都清理原进程组（Wait 之后才发：此时 shell 已回收，组里剩下的只可能是它留下的后台进程）
	defer func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) }()
	err := cmd.Wait()
	if out.over {
		return nil, errors.New("output too large")
	}
	if err != nil {
		return out.b.Bytes(), err
	}
	return out.b.Bytes(), nil
}

// maxLoginShellOutput 是登录 shell 输出的上限。
const maxLoginShellOutput = 64 << 10

// loginBuf 只收前 max 个字节，超出的丢掉并记下。
type loginBuf struct {
	b      bytes.Buffer
	max    int
	over   bool
	onOver func() // 第一次超限时调用一次
}

func (l *loginBuf) Write(p []byte) (int, error) {
	defer func() {
		if l.over && l.onOver != nil {
			f := l.onOver
			l.onOver = nil
			f()
		}
	}()
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
