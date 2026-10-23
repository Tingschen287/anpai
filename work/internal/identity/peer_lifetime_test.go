package identity

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/testsock"
)

// TestPeerHelperProcess 不是测试：被下面的测试用测试二进制自己重新执行出一个「连上 socket 后睡着」的对端进程
// （以前用 python3 -c 做这件事：macOS 自带的 python3 会弹 Xcode 安装窗口，也不必依赖它）。
func TestPeerHelperProcess(t *testing.T) {
	path := os.Getenv("N067_PEER_HELPER_SOCKET")
	if path == "" {
		return
	}
	c, err := net.Dial("unix", path)
	if err != nil {
		os.Exit(2)
	}
	defer c.Close()
	time.Sleep(60 * time.Second)
	os.Exit(0)
}

// startPeer 起一个对端进程连上 ln，返回 deckd 一侧接受到的连接与对端的 *exec.Cmd。
func startPeer(t *testing.T, ln *net.UnixListener) (*net.UnixConn, *exec.Cmd) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPeerHelperProcess$")
	cmd.Env = append(os.Environ(), "N067_PEER_HELPER_SOCKET="+ln.Addr().String())
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
	return srv, cmd
}

func listen(t *testing.T) *net.UnixListener {
	t.Helper()
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: testsock.Path(t, "s"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

// 两个平台通用：别的进程连上来，PeerPID 给出它的 pid；PeerLifetime 绑定后对端活着时成立、对端被杀并回收后不成立。
func TestPeerLifetimeFollowsTheConnectedProcess(t *testing.T) {
	srv, cmd := startPeer(t, listen(t))
	pid, err := PeerPID(srv)
	if err != nil || pid != cmd.Process.Pid {
		t.Fatalf("对端 pid 应是子进程 %d：%d %v", cmd.Process.Pid, pid, err)
	}
	same, release, err := PeerLifetime(srv, pid)
	if errors.Is(err, ErrNoPIDFD) {
		t.Skip("内核不支持 SO_PEERPIDFD：deckd 对这样的连接不授予任何角色")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := same(); err != nil {
		t.Fatalf("对端还活着：%v", err)
	}
	cmd.Process.Kill()
	cmd.Wait()
	if err := same(); err == nil {
		t.Fatal("对端已退出并被回收，生命周期检查仍成立")
	}
}

// 连接一旦被绑定，用它判定窗格的整条路径都要带上 Same：没有 Same 的连接被拒绝（fail closed）。
func TestResolveRefusesAConnectionWithoutLifetimeBindingEvenWithRealPeer(t *testing.T) {
	srv, _ := startPeer(t, listen(t))
	pid, err := PeerPID(srv)
	if err != nil {
		t.Fatal(err)
	}
	r := Resolver{Sources: []PaneSource{fakeSource{{Server: "/s/deck", PaneID: "%1", PanePID: pid, Session: "layout"}}}}
	if p, err := r.ResolvePeer(t.Context(), Peer{PID: pid, ConnectedAt: time.Now()}); !errors.Is(err, ErrNoLifetimeBinding) {
		t.Fatalf("没有绑定的连接被判成了 %+v（%v）", p, err)
	}
}
