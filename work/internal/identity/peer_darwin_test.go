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

// 生命周期绑定的 fail closed：pid 的代际号读不到 → PeerLifetime 报错（不是 ErrNoPIDFD，也不是返回一个永远成立的检查）。
func TestPeerLifetimeFailsClosedWhenStartTimeUnavailable(t *testing.T) {
	srv := connectedPair(t)
	old := pidVersionOf
	defer func() { pidVersionOf = old }()
	pidVersionOf = func(int) (uint32, error) { return 0, errors.New("proc_info refused") }
	same, _, err := PeerLifetime(srv, os.Getpid())
	if err == nil || errors.Is(err, ErrNoPIDFD) || same != nil {
		t.Fatalf("读不到代际号必须拒绝，且不能被当成「系统不支持」：same=%v err=%v", same != nil, err)
	}
}

// 再问一次对端 pid 对不上（核对期间连接的对端变了）：拒绝。
func TestPeerLifetimeRefusesAPIDThatIsNotThePeer(t *testing.T) {
	srv := connectedPair(t)
	if _, _, err := PeerLifetime(srv, os.Getpid()+1); err == nil {
		t.Fatal("传入的 pid 不是对端的，应拒绝")
	}
}

// pid 被复用：同一个 pid 现在是另一个进程（代际号变了），Same 不成立。
func TestSameDetectsAReusedPID(t *testing.T) {
	srv := connectedPair(t)
	old := pidVersionOf
	defer func() { pidVersionOf = old }()
	real, err := old(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	cur := real
	pidVersionOf = func(int) (uint32, error) { return cur, nil }
	same, _, err := PeerLifetime(srv, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := same(); err != nil {
		t.Fatalf("代际号没变时应成立：%v", err)
	}
	cur = real + 1 // 对端退出、pid 被另一个进程拿走
	if same() == nil {
		t.Fatal("代际号变了，Same 仍成立")
	}
	pidVersionOf = func(int) (uint32, error) { return 0, errors.New("no such process") }
	if same() == nil {
		t.Fatal("进程没了，Same 仍成立")
	}
}

// 连接建立之后、绑定之前 pid 就被复用了（旧做法的空窗：accept 之后才记下出生时间，记下的是新进程）：
// 绑定的是连接 token 里的代际号，所以绑定当时就发现 pid 现在不是建立这条连接的那个进程。
func TestPeerLifetimeRefusesAPIDReusedBeforeBinding(t *testing.T) {
	srv := connectedPair(t)
	old := pidVersionOf
	defer func() { pidVersionOf = old }()
	real, err := old(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	pidVersionOf = func(int) (uint32, error) { return real + 1, nil }
	same, _, err := PeerLifetime(srv, os.Getpid())
	if err == nil || errors.Is(err, ErrNoPIDFD) || same != nil {
		t.Fatalf("pid 在绑定前已被复用，必须拒绝：same=%v err=%v", same != nil, err)
	}
}

// 取不到连接的 audit token：PeerPID、PeerLifetime 都拒绝，不退回别的办法猜。
func TestPeerFailsClosedWhenTokenUnavailable(t *testing.T) {
	srv := connectedPair(t)
	old := peerTokenOf
	defer func() { peerTokenOf = old }()
	peerTokenOf = func(*net.UnixConn) (auditToken, error) { return auditToken{}, errors.New("LOCAL_PEERTOKEN refused") }
	if pid, err := PeerPID(srv); err == nil {
		t.Fatalf("取不到 token 不该给出对端 pid：%d", pid)
	}
	same, _, err := PeerLifetime(srv, os.Getpid())
	if err == nil || errors.Is(err, ErrNoPIDFD) || same != nil {
		t.Fatalf("取不到 token 必须拒绝，且不能被当成「系统不支持」：same=%v err=%v", same != nil, err)
	}
}

// token 内容不可信时拒绝：别的用户、pid 与 LOCAL_PEERPID 对不上、pid 为 0。
func TestPeerRefusesAnInconsistentToken(t *testing.T) {
	srv := connectedPair(t)
	old := peerTokenOf
	defer func() { peerTokenOf = old }()
	real, err := old(srv)
	if err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*auditToken){
		"别的用户":             func(k *auditToken) { k.EUID++ },
		"pid 对不上":          func(k *auditToken) { k.PID++ },
		"pid 为 0":          func(k *auditToken) { k.PID = 0 },
		"pid 超出 int32 的范围": func(k *auditToken) { k.PID = 1 << 31 },
	} {
		tok := real
		mut(&tok)
		peerTokenOf = func(*net.UnixConn) (auditToken, error) { return tok, nil }
		if pid, err := PeerPID(srv); err == nil {
			t.Errorf("%s：token 不可信却给出了对端 pid %d", name, pid)
		}
		if same, _, err := PeerLifetime(srv, os.Getpid()); err == nil || same != nil {
			t.Errorf("%s：token 不可信却绑定成功", name)
		}
	}
}

// 真实 token：来自别的进程的连接，token 里的 pid 就是那个子进程，pidversion 等于 proc_info 此刻读到的值，euid 是本用户。
func TestRealPeerTokenMatchesTheConnectingProcess(t *testing.T) {
	srv, cmd := startPeer(t, listen(t))
	tok, err := peerTokenOf(srv)
	if err != nil {
		t.Fatal(err)
	}
	if int(tok.PID) != cmd.Process.Pid || tok.EUID != uint32(os.Geteuid()) {
		t.Fatalf("token 应属于子进程 %d、本用户：%+v", cmd.Process.Pid, tok)
	}
	v, err := pidVersionOf(cmd.Process.Pid)
	if err != nil || v != tok.PIDVersion {
		t.Fatalf("proc_info 的代际号 %d（%v）应等于 token 里的 %d", v, err, tok.PIDVersion)
	}
}

// 对端连上之后换了进程映像（exec）：代际号变了，这条连接绑定的那个进程已经不是它，Same 不成立。
// 这是用真进程演示「绑定的是连接对端的代际，不是 pid」：pid 没变，旧做法（记 pid 的出生时间）发现不了。
func TestSameFailsAfterThePeerExecs(t *testing.T) {
	ln := listen(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestPeerExecHelperProcess$")
	cmd.Env = append(os.Environ(), "N067_PEER_EXEC_SOCKET="+ln.Addr().String())
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	ln.SetDeadline(time.Now().Add(10 * time.Second))
	srv, err := ln.AcceptUnix()
	if err != nil {
		t.Fatalf("对端没连上：%v", err)
	}
	t.Cleanup(func() { srv.Close() })
	pid, err := PeerPID(srv)
	if err != nil || pid != cmd.Process.Pid {
		t.Fatalf("对端 pid：%d %v", pid, err)
	}
	same, _, err := PeerLifetime(srv, pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := same(); err != nil {
		t.Fatalf("exec 之前应成立：%v", err)
	}
	// 通知对端 exec（它收到一个字节后 exec 成 sleep），等 pid 的映像换掉
	if _, err := srv.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if same() != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("对端 exec 之后（pid 不变）Same 仍成立")
}

// TestPeerExecHelperProcess 不是测试：连上 socket，等一个字节，然后 exec 成 sleep（pid 不变，映像换掉）。
func TestPeerExecHelperProcess(t *testing.T) {
	path := os.Getenv("N067_PEER_EXEC_SOCKET")
	if path == "" {
		return
	}
	c, err := net.Dial("unix", path)
	if err != nil {
		os.Exit(2)
	}
	var b [1]byte
	if _, err := c.Read(b[:]); err != nil {
		os.Exit(3)
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		os.Exit(4)
	}
	os.Exit(1 + int(unix.Exec(sleep, []string{"sleep", "30"}, os.Environ()).(unix.Errno)))
}

// connectedPair 在本进程内连一对 unix socket，返回 accept 一侧（对端就是本进程）。
func connectedPair(t *testing.T) *net.UnixConn {
	t.Helper()
	ln := listen(t)
	cli, err := net.Dial("unix", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Close() })
	srv, err := ln.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
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
