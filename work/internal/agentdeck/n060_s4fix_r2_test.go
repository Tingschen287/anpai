package agentdeck

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// n060 段 4 返工 R2：登录 shell 探针在任何结束路径上都要清理原进程组里的子进程。
//
// 以下测试用真实的 /bin/zsh，但只用隔离的 HOME 与 ZDOTDIR 和最小环境（不读 owner 的任何 rc 文件，不继承 owner 的环境）。
// rc 里起的后台进程是 `/bin/sleep 30`，pid 写进临时文件，结束后按 pid 检查它还活没活。
// 主动 setsid/daemonize 脱离进程组的后代不在这里：它们已经不属于原进程组，清不掉是能力边界（见 loginshell.go 的注释）。

// zshProbe 用隔离环境跑 RunLoginShell：rc 是 .zshrc 的内容（里面用 {PID} 代表 pid 文件路径）。返回错误与后台子进程的 pid。
func zshProbe(t *testing.T, rc string, ctxTimeout time.Duration) (out []byte, err error, pid int, took time.Duration) {
	t.Helper()
	if _, e := os.Stat("/bin/zsh"); e != nil {
		t.Skip("没有 /bin/zsh")
	}
	home := t.TempDir()
	pidfile := filepath.Join(home, "child.pid")
	rc = strings.ReplaceAll(rc, "{PID}", pidfile)
	if e := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(rc), 0o600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), ctxTimeout)
	defer cancel()
	start := time.Now()
	// 最小环境：不带 owner 的 HOME、ZDOTDIR、SHELL 之外的任何变量
	out, err = RunLoginShell(ctx, "/bin/zsh", []string{"-l", "-i", "-c", "printf done"},
		[]string{"HOME=" + home, "ZDOTDIR=" + home, "PATH=/usr/bin:/bin", "SHELL=/bin/zsh", "ANPAI_PATH_PROBE=1"})
	took = time.Since(start)
	b, e := os.ReadFile(pidfile)
	if e != nil {
		t.Fatalf("rc 没有写出后台子进程的 pid（rc 没跑到？）：%v 输出=%q err=%v", e, out, err)
	}
	pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	if pid <= 1 {
		t.Fatalf("pid 不对：%q", b)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return out, err, pid, took
}

func waitGone(pid int) bool {
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
	}
	return false
}

// shell 已经正常退出，rc 里的后台进程还占着输出管道：WaitDelay 到期返回 ErrWaitDelay，后台进程不能残留（Codex 审查 1 的反例）。
func TestN060S4FixLoginPipeWaitCleanup(t *testing.T) {
	_, err, pid, took := zshProbe(t, "/bin/sleep 30 &\necho $! > {PID}\n", 5*time.Second)
	t.Logf("err=%v took=%v pid=%d", err, took, pid)
	if !waitGone(pid) {
		t.Fatalf("shell 已退出、后台进程占着管道，WaitDelay 到期后后台进程 %d 还活着", pid)
	}
	if took > 3*time.Second {
		t.Fatalf("不能靠加长等待来掩盖：%v", took)
	}
}

// 正常退出且后台进程不占管道（输出重定向走了）：没有任何错误，后台进程同样要清理。
func TestN060S4FixLoginNormalExitCleansBackgroundChildren(t *testing.T) {
	out, err, pid, _ := zshProbe(t, "/bin/sleep 30 </dev/null >/dev/null 2>&1 &\necho $! > {PID}\n", 5*time.Second)
	if err != nil || !strings.Contains(string(out), "done") {
		t.Fatalf("正常退出不该有错：err=%v out=%q", err, out)
	}
	if !waitGone(pid) {
		t.Fatalf("正常退出后后台进程 %d 还活着", pid)
	}
}

// 非零退出：shell 退出码不为 0，后台进程（占着管道或不占）都要清理。
func TestN060S4FixLoginNonZeroExitCleansBackgroundChildren(t *testing.T) {
	for name, rc := range map[string]string{
		"占着管道": "/bin/sleep 30 &\necho $! > {PID}\nexit 3\n",
		"不占管道": "/bin/sleep 30 </dev/null >/dev/null 2>&1 &\necho $! > {PID}\nexit 3\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err, pid, took := zshProbe(t, rc, 5*time.Second)
			ee, ok := err.(*exec.ExitError)
			if !ok || ee.ExitCode() != 3 {
				if err == nil || !strings.Contains(err.Error(), "WaitDelay") { // 占着管道时 Go 可能先报 ErrWaitDelay，两者都算非零结束
					t.Fatalf("应是非零退出：%v", err)
				}
			}
			if !waitGone(pid) {
				t.Fatalf("非零退出后后台进程 %d 还活着", pid)
			}
			if took > 3*time.Second {
				t.Fatalf("太慢：%v", took)
			}
		})
	}
}

// 输出超限：一超过上限就当失败，立刻收拾整组（不等 shell 自己结束），后台进程不残留。
func TestN060S4FixLoginOutputOverLimitCleansBackgroundChildren(t *testing.T) {
	rc := "/bin/sleep 30 </dev/null >/dev/null 2>&1 &\necho $! > {PID}\n/usr/bin/head -c 300000 /dev/zero | /usr/bin/tr '\\0' x\n/bin/sleep 30\n"
	_, err, pid, took := zshProbe(t, rc, 10*time.Second)
	if err == nil || !strings.Contains(err.Error(), "output too large") {
		t.Fatalf("输出超限应报 output too large：%v", err)
	}
	if !waitGone(pid) {
		t.Fatalf("输出超限后后台进程 %d 还活着", pid)
	}
	if took > 4*time.Second {
		t.Fatalf("超限后应立刻收拾，不等 shell 自己结束：%v", took)
	}
}

// ctx 超时：交互 shell 卡在 rc 里，超时后整组被杀，rc 起的后台进程不残留（Codex 审查 1 里已经通过的情形，保留）。
func TestN060S4FixLoginTimeoutKillsRcChildren(t *testing.T) {
	_, err, pid, took := zshProbe(t, "/bin/sleep 30 &\necho $! > {PID}\n/bin/sleep 30\n", 300*time.Millisecond)
	t.Logf("err=%v took=%v", err, took)
	if err == nil {
		t.Fatal("卡住的 shell 应以错误结束")
	}
	if !waitGone(pid) {
		t.Fatalf("超时后后台进程 %d 还活着", pid)
	}
	if took > 3*time.Second {
		t.Fatalf("超时没有按时返回：%v", took)
	}
}

// 能力边界的说明性检查（不是承诺）：主动 setsid 脱离原进程组的后代不属于原进程组，本函数清不掉。
// 这里只确认 RunLoginShell 清理的对象是「原进程组」：另一个进程组里的无关进程不会被误杀。
func TestN060S4FixLoginDoesNotTouchOtherProcessGroups(t *testing.T) {
	other := exec.Command("/bin/sleep", "30")
	other.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Process.Kill(); _ = other.Wait() }()
	_, _, _, _ = zshProbe(t, "/bin/sleep 30 &\necho $! > {PID}\n", 5*time.Second)
	if syscall.Kill(other.Process.Pid, 0) != nil {
		t.Fatal(fmt.Sprintf("无关进程组里的进程 %d 被误杀", other.Process.Pid))
	}
}
