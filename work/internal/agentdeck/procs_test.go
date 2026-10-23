package agentdeck

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// TestProcHelperSleep 不是测试：被下面的测试用测试二进制自己重新执行，造出一个命令行可控的进程
// （argv[0] 想叫 agent-deck、参数里带 notify-daemon，都由调用方决定）。
func TestProcHelperSleep(t *testing.T) {
	if os.Getenv("N067_PROC_HELPER") == "" {
		return
	}
	time.Sleep(60 * time.Second)
}

// startHelper 用 argv0 作为 argv[0]、extra 作为多出来的参数，起一个会睡 60 秒的测试二进制副本。
func startHelper(t *testing.T, argv0 string, extra ...string) *exec.Cmd {
	t.Helper()
	// 「--」之后的都是位置参数：测试二进制不会因为不认识 --json 之类的参数而立刻退出
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestProcHelperSleep$", "--"}, extra...)...)
	cmd.Args[0] = argv0
	cmd.Env = append(os.Environ(), "N067_PROC_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd
}

// 本平台的进程表（Linux /proc，macOS sysctl）：能列出自己，读出的命令行和 os.Args 逐个参数一致，父子关系对得上。
func TestNativeProcessTableSeesOurOwnProcess(t *testing.T) {
	pids, err := procPIDs("")
	if err != nil || !slices.Contains(pids, os.Getpid()) {
		t.Fatalf("进程表里应有本进程 %d：%v（共 %d 个）", os.Getpid(), err, len(pids))
	}
	argv, ok := procArgv("", os.Getpid())
	if !ok || !slices.Equal(argv, os.Args) {
		t.Fatalf("本进程的命令行应是 os.Args：\n got %q\nwant %q（读到=%v）", argv, os.Args, ok)
	}
	child := startHelper(t, "n067-child", "--n067-marker=1")
	deadline := time.Now().Add(2 * time.Second)
	for {
		if slices.Contains(procChildren("")[os.Getpid()], child.Process.Pid) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("子进程 %d 不在本进程的子进程表里", child.Process.Pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, ok := procArgv("", child.Process.Pid)
	if !ok || len(got) < 3 || got[0] != "n067-child" || got[len(got)-1] != "--n067-marker=1" {
		t.Fatalf("子进程的命令行（含自定义的 argv[0]）读错了：%q", got)
	}
	// 已退出的进程读不到：返回 false，不是错误
	child.Process.Kill()
	child.Wait()
	if _, ok := procArgv("", child.Process.Pid); ok {
		t.Fatal("已回收的进程不该读到命令行")
	}
}

// NotifyDaemonRunning 用本平台的进程表找到 `agent-deck … notify-daemon`。本机可能本来就有真的 notify-daemon 在跑，
// 所以不假设起点是 false：起点是 true 时只跳过「从无到有」那一半。
func TestNotifyDaemonRunningOnTheNativeProcessTable(t *testing.T) {
	old := procRoot
	procRoot = ""
	defer func() { procRoot = old }()
	c := &Client{}
	before, err := c.NotifyDaemonRunning(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if before {
		t.Skip("本机已经有真的 agent-deck notify-daemon 在跑，没法判断「起一个之后才认出来」")
	}
	startHelper(t, filepath.Join(t.TempDir(), "agent-deck"), "notify-daemon")
	deadline := time.Now().Add(2 * time.Second)
	for {
		running, err := c.NotifyDaemonRunning(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("起了一个 argv[0] 叫 agent-deck、参数含 notify-daemon 的进程，却没认出来")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// 名字像但参数不含 notify-daemon 的，不算：起这样一个进程不改变答案（本机有没有真的 notify-daemon 都成立）。
func TestNotifyDaemonRunningIgnoresOtherAgentDeckProcesses(t *testing.T) {
	old := procRoot
	procRoot = ""
	defer func() { procRoot = old }()
	c := &Client{}
	before, err := c.NotifyDaemonRunning(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h := startHelper(t, filepath.Join(t.TempDir(), "agent-deck"), "list", "--json")
	deadline := time.Now().Add(2 * time.Second)
	for { // 先确认进程表里真的有这个 agent-deck list 进程，再断言它不改变答案
		if argv, ok := procArgv("", h.Process.Pid); ok && len(argv) > 0 && filepath.Base(argv[0]) == "agent-deck" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("进程表里读不到刚起的 agent-deck list 进程")
		}
		time.Sleep(20 * time.Millisecond)
	}
	after, err := c.NotifyDaemonRunning(context.Background())
	if err != nil || after != before {
		t.Fatalf("agent-deck list 不是 notify-daemon，不该改变答案：before=%v after=%v err=%v", before, after, err)
	}
}
