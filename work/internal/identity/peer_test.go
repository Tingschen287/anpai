package identity

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// 进程号复用由 pidfd 发现：原调用者退出、新窗格进程复用了 pid，两次读链都读到新进程，但 pidfd 指向的进程已不再持有它。
func TestReusedPIDIsCaughtByLifetimeBinding(t *testing.T) {
	proc := fakeProc(t, map[int]int{500: 1})
	r := Resolver{ProcRoot: proc, Sources: []PaneSource{fakeSource{{Server: "/s/deck", PaneID: "%1", PanePID: 500, Session: "layout"}}}}
	gone := func() error { return errors.New("pidfd 现在指向 pid -1（已退出）") }
	if p, err := r.ResolvePeer(context.Background(), Peer{PID: 500, ConnectedAt: time.Now(), Same: gone}); err == nil {
		t.Fatalf("复用 pid 的进程被认成布局窗格：%+v", p)
	}
}

// 墙钟出生时间不参与判定（n049）：WSL 校时会让 btime 移动，同一个进程会被算成「连接后才出生」。
// 这里构造正是那种情形——按墙钟算出生在连接之后——有 pidfd 证明是同一个进程，就应接受。
func TestWallClockBirthTimeIsNotUsed(t *testing.T) {
	proc := fakeProc(t, map[int]int{500: 1})
	writeStat(proc, 500, 1, 100*3600) // 按 btime 换算是开机后 1 小时
	r := Resolver{ProcRoot: proc, Sources: []PaneSource{fakeSource{{Server: "/s/deck", PaneID: "%1", PanePID: 500, Session: "layout"}}}}
	at := time.Unix(1790000000, 0).Add(30 * time.Minute) // 连接按墙钟在开机后 30 分钟
	if _, err := r.ResolvePeer(context.Background(), Peer{PID: 500, ConnectedAt: at, Same: held}); err != nil {
		t.Fatalf("有 pidfd 证明是同一个进程时，不应因墙钟出生时间拒绝：%v", err)
	}
}

// held 是一个始终成立的生命周期绑定。
func held() error { return nil }

// 第 4 次实现（评审 A3-1 的复现）：连接没有生命周期绑定时，不据进程号判定窗格——
// 无论开机时间取整（替代进程实际在连接后出生、换算值却更早），还是接受连接有延迟（替代进程在连接与接受之间出生）。
func TestConnectionWithoutLifetimeBindingIsNotResolved(t *testing.T) {
	for _, tc := range []struct {
		name     string
		start    uint64
		accepted time.Duration
	}{
		{"开机时间取整", 1050, 10900 * time.Millisecond},
		{"接受连接延迟", 1100, 12 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc := fakeProc(t, map[int]int{500: 1})
			writeStat(proc, 500, 1, tc.start)
			r := Resolver{ProcRoot: proc, Sources: []PaneSource{fakeSource{{Server: "/s/deck", PaneID: "%1", PanePID: 500, Session: "layout"}}}}
			p, err := r.ResolvePeer(context.Background(), Peer{PID: 500, ConnectedAt: time.Unix(1790000000, 0).Add(tc.accepted)})
			if !errors.Is(err, ErrNoLifetimeBinding) {
				t.Fatalf("没有绑定的连接被判成了 %+v（%v）", p, err)
			}
		})
	}
}

// Same 在判定前后各查一次：任一次不成立都拒绝。
func TestSameIsCheckedBeforeAndAfterResolution(t *testing.T) {
	proc := fakeProc(t, map[int]int{500: 1})
	r := Resolver{ProcRoot: proc, Sources: []PaneSource{fakeSource{{Server: "/s/deck", PaneID: "%1", PanePID: 500, Session: "layout"}}}}
	for _, failAt := range []int{1, 2} {
		calls := 0
		same := func() error {
			calls++
			if calls == failAt {
				return errors.New("pidfd 现在指向 pid -1")
			}
			return nil
		}
		if _, err := r.ResolvePeer(context.Background(), Peer{PID: 500, Same: same}); err == nil {
			t.Fatalf("第 %d 次检查不成立仍被接受", failAt)
		}
	}
	calls := 0
	p, err := r.ResolvePeer(context.Background(), Peer{PID: 500, Same: func() error { calls++; return nil }})
	if err != nil || p.PaneID != "%1" || calls != 2 {
		t.Fatalf("两次都成立应接受：%+v %v calls=%d", p, err, calls)
	}
}

// 真机：经 unix socket 取对端 pidfd，指向的就是本进程。
func TestPeerPIDFDOnRealSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cli, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	srv, err := ln.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	fd, err := PeerPIDFD(srv)
	if errors.Is(err, ErrNoPIDFD) {
		t.Skip("内核不支持 SO_PEERPIDFD：deckd 退回出生时间检查")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	if err := PIDFDHolds(fd, os.Getpid())(); err != nil {
		t.Fatalf("对端就是本进程：%v", err)
	}
	if err := PIDFDHolds(fd, os.Getpid()+1)(); err == nil {
		t.Fatal("pid 不同应不成立")
	}
}

// 真机：对端进程退出并被回收后，pidfd 的检查不成立。
func TestPIDFDHoldsFailsAfterPeerExits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	// 子进程连上 socket 后一直睡着；连接的对端就是它
	cmd := exec.Command("python3", "-c", "import socket,sys,time\ns=socket.socket(socket.AF_UNIX);s.connect(sys.argv[1]);time.sleep(30)", path)
	if err := cmd.Start(); err != nil {
		t.Skip("没有 python3：", err)
	}
	ln.SetDeadline(time.Now().Add(10 * time.Second))
	srv, err := ln.AcceptUnix()
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatal(err)
	}
	defer srv.Close()
	fd, err := PeerPIDFD(srv)
	if errors.Is(err, ErrNoPIDFD) {
		cmd.Process.Kill()
		cmd.Wait()
		t.Skip("内核不支持 SO_PEERPIDFD")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	pid := cmd.Process.Pid
	if err := PIDFDHolds(fd, pid)(); err != nil {
		t.Fatalf("子进程活着时应成立：%v", err)
	}
	cmd.Process.Kill()
	cmd.Wait()
	if err := PIDFDHolds(fd, pid)(); err == nil {
		t.Fatal("子进程已回收，检查仍成立")
	}
}
