package identity

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// LOCAL_PEERCRED 的核对：版本不认识、uid 不是本用户、取不到，都拒绝（fail closed）；只有版本和 uid 都对才放行。
func TestCheckXucredFailsClosed(t *testing.T) {
	me := uint32(os.Geteuid())
	cases := []struct {
		name string
		cred *unix.Xucred
		ok   bool
	}{
		{"取不到", nil, false},
		{"版本不认识", &unix.Xucred{Version: 7, Uid: me}, false},
		{"别的用户", &unix.Xucred{Version: xucredVersion, Uid: me + 1}, false},
		{"root 也不是本用户", &unix.Xucred{Version: xucredVersion, Uid: 0}, me == 0},
		{"本用户", &unix.Xucred{Version: xucredVersion, Uid: me}, true},
	}
	for _, c := range cases {
		err := checkXucred(c.cred, me)
		if c.ok != (err == nil) {
			t.Errorf("%s：ok=%v 但 err=%v", c.name, c.ok, err)
		}
	}
}

// 真实 socket 上拿 LOCAL_PEERCRED 的结果：uid 就是本用户、版本是 xucredVersion——核对函数不是只对着构造的数据通过。
func TestRealPeerCredentialsPassTheCheck(t *testing.T) {
	ln := listen(t)
	cli, err := net.Dial("unix", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	srv, err := ln.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	raw, _ := srv.SyscallConn()
	var cred *unix.Xucred
	var serr error
	raw.Control(func(fd uintptr) {
		cred, serr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	})
	if serr != nil {
		t.Fatal(serr)
	}
	if err := checkXucred(cred, uint32(os.Geteuid())); err != nil {
		t.Fatalf("本进程连来的连接，凭据核对应通过：%v（%+v）", err, cred)
	}
	if pid, err := PeerPID(srv); err != nil || pid != os.Getpid() {
		t.Fatalf("对端 pid 应是本进程：%d %v", pid, err)
	}
}

// 连接已关闭、取不到对端：PeerPID 返回错误，不返回 0 或猜一个。
func TestPeerPIDOnAClosedConnectionFailsClosed(t *testing.T) {
	ln := listen(t)
	cli, err := net.Dial("unix", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := ln.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	cli.Close()
	srv.Close() // 本端关了：fd 不可用
	if pid, err := PeerPID(srv); err == nil {
		t.Fatalf("关闭的连接不该给出对端 pid：%d", pid)
	}
}

// 生命周期绑定的 fail closed：出生时间读不到 → PeerLifetime 报错（不是 ErrNoPIDFD，也不是返回一个永远成立的检查）。
func TestPeerLifetimeFailsClosedWhenStartTimeUnavailable(t *testing.T) {
	ln := listen(t)
	cli, err := net.Dial("unix", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	srv, err := ln.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	old := startTimeOf
	defer func() { startTimeOf = old }()
	startTimeOf = func(int) (uint64, error) { return 0, errors.New("sysctl refused") }
	same, _, err := PeerLifetime(srv, os.Getpid())
	if err == nil || errors.Is(err, ErrNoPIDFD) || same != nil {
		t.Fatalf("读不到出生时间必须拒绝，且不能被当成「系统不支持」：same=%v err=%v", same != nil, err)
	}
}

// 再问一次对端 pid 对不上（核对期间连接的对端变了）：拒绝。
func TestPeerLifetimeRefusesAPIDThatIsNotThePeer(t *testing.T) {
	ln := listen(t)
	cli, err := net.Dial("unix", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	srv, err := ln.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if _, _, err := PeerLifetime(srv, os.Getpid()+1); err == nil {
		t.Fatal("传入的 pid 不是对端的，应拒绝")
	}
}

// pid 被复用：同一个 pid 现在是另一个进程（出生时间变了），Same 不成立。
func TestSameDetectsAReusedPID(t *testing.T) {
	ln := listen(t)
	cli, err := net.Dial("unix", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	srv, err := ln.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	old := startTimeOf
	defer func() { startTimeOf = old }()
	calls := 0
	startTimeOf = func(int) (uint64, error) { calls++; return uint64(1000 + calls/3), nil } // 绑定时 1000，之后变成 1001
	same, _, err := PeerLifetime(srv, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if same() == nil {
			continue
		}
		return
	}
	t.Fatal("出生时间变了，Same 仍成立")
}

// nativeStat 与进程链：自己的父进程是起测试的进程，出生时间不为 0；子进程的链上能找到本进程；不存在的 pid 报错。
func TestNativeStatAndAncestryOnRealProcesses(t *testing.T) {
	ppid, start, err := nativeStat(os.Getpid())
	if err != nil || ppid != os.Getppid() || start == 0 {
		t.Fatalf("本进程：ppid=%d（期望 %d）start=%d err=%v", ppid, os.Getppid(), start, err)
	}
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	chain, err := Resolver{}.Ancestry(cmd.Process.Pid)
	if err != nil || len(chain) < 2 || chain[0].PID != cmd.Process.Pid || chain[1].PID != os.Getpid() {
		t.Fatalf("子进程的链应是 [子进程, 本进程, …]：%+v %v", chain, err)
	}
	if _, _, err := nativeStat(1 << 22); err == nil {
		t.Fatal("不存在的 pid 应报错")
	}
	// 进程刚退出并被回收后也读不到：判定时按「对端已消失」拒绝，不当成未知
	pid := cmd.Process.Pid
	cmd.Process.Kill()
	cmd.Wait()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := (Resolver{}).Ancestry(pid); err != nil && !strings.Contains(err.Error(), "环") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("已回收的进程还读得到")
}
