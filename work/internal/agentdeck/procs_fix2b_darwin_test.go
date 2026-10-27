package agentdeck

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// n067 返工 2 的接缝测试：经 sysctlProcargs、selfCheckHook 两个变量注入数据，验证
// 「读不到」与「确认不了」分开处理，CheckSessionArgs 对后者按确认不了拒绝，运行时自检 fail closed。
// 这些测试依赖新增的接缝，在 17ce071 上编译不过；会在旧代码上失败的测试在 procs_fix2_darwin_test.go、argallow_glue_test.go。

func withSysctl(t *testing.T, fn func(pid int) ([]byte, error)) {
	t.Helper()
	old := sysctlProcargs
	sysctlProcargs = fn
	t.Cleanup(func() { sysctlProcargs = old })
}

// withSelfCheck 换掉运行时自检并让它重新跑一次；测试结束恢复。
func withSelfCheck(t *testing.T, hook func() error) {
	t.Helper()
	oldHook := selfCheckHook
	selfCheckHook = hook
	procargsSelf.once, procargsSelf.err = sync.Once{}, nil
	t.Cleanup(func() {
		selfCheckHook = oldHook
		procargsSelf.once, procargsSelf.err = sync.Once{}, nil
	})
}

// 读不到（进程没了、没权限）：procArgsOK 不当错误；读到了但截断：当错误，且是 errProcArgsUnconfirmable。
func TestProcArgsOKTreatsUnreadableAsGoneAndTruncatedAsAnError(t *testing.T) {
	want := LaunchArgs{Tool: "claude", Binary: "claude"}
	truncated := realProcargs2(3, "/bin/x", []string{"x", "yy", "zzz"}, nil)
	truncated = truncated[:strings.Index(string(truncated), "yy")]
	withSysctl(t, func(pid int) ([]byte, error) {
		switch pid {
		case 1001:
			return nil, syscall.ESRCH
		case 1002:
			return nil, syscall.EPERM
		case 1003:
			return nil, syscall.EINVAL
		case 1004:
			return truncated, nil
		case 1005:
			return realProcargs2(2, "/bin/x", []string{"x", "y"}, []string{"A=b"}), nil
		}
		return unix.SysctlRaw("kern.procargs2", pid)
	})
	for _, pid := range []int{1001, 1002, 1003} {
		if ok, err := procArgsOK("", pid, want); ok || err != nil {
			t.Errorf("pid %d 读不到应跳过：%v %v", pid, ok, err)
		}
	}
	if _, err := procArgsOK("", 1004, want); !errors.Is(err, errProcArgsUnconfirmable) {
		t.Errorf("截断的命令行应报确认不了：%v", err)
	}
	if ok, err := procArgsOK("", 1005, want); ok || err != nil {
		t.Errorf("完整的非 CLI 命令行应通过：%v %v", ok, err)
	}
	// procArgv 沿用「读不到就是 false」，不把半截命令行交出去
	if argv, ok := procArgv("", 1004); ok || argv != nil {
		t.Errorf("截断时 procArgv 不该返回命令行：%q %v", argv, ok)
	}
}

// CheckSessionArgs 端到端：会话里有一个进程的命令行读到一半（截断），整个核对按确认不了拒绝；同一会话不截断时通过。
func TestCheckSessionArgsRejectsWhenAnArgvCannotBeConfirmed(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("没有 tmux")
	}
	// tmux 的 socket 路径在 macOS 上不能超过 104 字节，t.TempDir() 在 /var/folders 下太长：用 /tmp 下的短目录
	sockDir, err := os.MkdirTemp("/tmp", "n67s")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "s")
	run := func(a ...string) string {
		out, err := exec.Command("tmux", append([]string{"-S", sock, "-f", "/dev/null"}, a...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v：%v %s", a, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	t.Cleanup(func() { exec.Command("tmux", "-S", sock, "kill-server").Run() })
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/bash\nsleep 30\n"), 0o755)
	run("new-session", "-d", "-s", "s1", "bash", "-c", filepath.Join(bin, "claude")+" --session-id u-1 --model m")
	time.Sleep(300 * time.Millisecond)
	panePID, err := strconv.Atoi(run("display-message", "-p", "-t", "s1", "#{pane_pid}"))
	if err != nil {
		t.Fatal(err)
	}
	defer func(d time.Duration) { CLIWait = d }(CLIWait)
	CLIWait = 500 * time.Millisecond
	want := LaunchArgs{Tool: "claude", Binary: "claude"}
	ctx := context.Background()
	if err := CheckSessionArgs(ctx, sock, "s1", want); err != nil {
		t.Fatalf("不截断时应通过：%v", err)
	}
	withSysctl(t, func(pid int) ([]byte, error) {
		buf, err := unix.SysctlRaw("kern.procargs2", pid)
		if pid == panePID && err == nil {
			return buf[:len(buf)/2], nil // 拦腰截断
		}
		return buf, err
	})
	err = CheckSessionArgs(ctx, sock, "s1", want)
	if !errors.Is(err, errProcArgsUnconfirmable) {
		t.Fatalf("命令行截断时应按确认不了拒绝：%v", err)
	}
}

// 运行时自检：解析结果和 os.Args 一致时通过（本机内核版式正确）；对不上就失败。
func TestSelfCheckProcargsMatchesOsArgs(t *testing.T) {
	if err := selfCheckProcargs(); err != nil {
		t.Fatalf("本机的 procargs2 解析和 os.Args 对不上：%v", err)
	}
	// 解析规则和内核版式对不上的模拟：多出一个参数、少一个参数、读不到、数据截断
	withSysctl(t, func(int) ([]byte, error) {
		return realProcargs2(int32(len(initialArgs)+1), "/bin/x", append(append([]string{}, initialArgs...), "extra"), nil), nil
	})
	if err := selfCheckProcargs(); err == nil {
		t.Error("多出一个参数应自检失败")
	}
	withSysctl(t, func(int) ([]byte, error) {
		return realProcargs2(int32(len(initialArgs)-1), "/bin/x", initialArgs[:len(initialArgs)-1], nil), nil
	})
	if err := selfCheckProcargs(); err == nil {
		t.Error("少一个参数应自检失败")
	}
	withSysctl(t, func(int) ([]byte, error) { return nil, syscall.EPERM })
	if err := selfCheckProcargs(); err == nil {
		t.Error("读不了自己的 procargs2 应自检失败")
	}
	withSysctl(t, func(int) ([]byte, error) { return []byte{1, 0}, nil })
	if err := selfCheckProcargs(); err == nil {
		t.Error("数据太短应自检失败")
	}
}

// 自检没过：nativeCmdline 对任何进程（包括自己）一律报确认不了，CheckSessionArgs 的核对据此拒绝；NotifyDaemonRunning 报错，不当作「没在跑」。
func TestFailedSelfCheckMakesEveryReadFailClosed(t *testing.T) {
	withSelfCheck(t, func() error { return errors.New("layout mismatch (injected)") })
	for _, pid := range []int{os.Getpid(), 1} {
		_, err := nativeCmdline(pid)
		if !errors.Is(err, errProcArgsUnconfirmable) || !errors.Is(err, errProcArgsSelfCheck) {
			t.Errorf("pid %d：自检没过应一律报确认不了：%v", pid, err)
		}
	}
	if _, err := procArgsOK("", os.Getpid(), LaunchArgs{Tool: "claude"}); !errors.Is(err, errProcArgsUnconfirmable) {
		t.Errorf("procArgsOK 应拒绝：%v", err)
	}
	oldRoot := procRoot
	procRoot = ""
	defer func() { procRoot = oldRoot }()
	if running, err := (&Client{}).NotifyDaemonRunning(context.Background()); err == nil || running {
		t.Errorf("自检没过时 NotifyDaemonRunning 应报错：%v %v", running, err)
	}
	// 自检只跑一次：之后换成通过的钩子也不再重跑（本测试的 Cleanup 才会重置）
	selfCheckHook = func() error { return nil }
	if _, err := nativeCmdline(os.Getpid()); err == nil {
		t.Error("自检结果应缓存")
	}
}

// 自检通过时，单个进程的命令行确认不了不影响 NotifyDaemonRunning 去看别的进程（那个进程不是我们要找的样子，跳过）。
func TestNotifyDaemonRunningSkipsAnUnconfirmableProcess(t *testing.T) {
	pids, err := procPIDs("")
	if err != nil || len(pids) == 0 {
		t.Skip("读不到进程表")
	}
	bad := pids[len(pids)-1]
	withSysctl(t, func(pid int) ([]byte, error) {
		if pid == bad {
			return []byte{9, 0, 0}, nil
		}
		return unix.SysctlRaw("kern.procargs2", pid)
	})
	oldRoot := procRoot
	procRoot = ""
	defer func() { procRoot = oldRoot }()
	if _, err := (&Client{}).NotifyDaemonRunning(context.Background()); err != nil {
		t.Fatalf("单个进程确认不了不该让整个查询失败：%v", err)
	}
}
