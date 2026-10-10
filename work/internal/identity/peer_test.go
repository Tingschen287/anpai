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

// 第 3 次实现（n050 评审 R4）：出生时间不留容差、换算不溢出、连接身份绑定到 pidfd。

// 评审复现：原调用者退出，新窗格进程在连接后 0.5 秒复用了 pid，两次读链都读到新进程。
func TestReusedPIDBornAfterConnectionIsRejectedWithoutTolerance(t *testing.T) {
	proc := fakeProc(t, map[int]int{500: 1})
	writeStat(proc, 500, 1, 1050) // 开机后 10.5 秒出生
	r := Resolver{ProcRoot: proc, Sources: []PaneSource{fakeSource{{Server: "/s/deck", PaneID: "%1", PanePID: 500, Session: "layout"}}}}
	at := time.Unix(1790000000, 0).Add(10 * time.Second)
	if p, err := r.ResolvePeer(context.Background(), Peer{PID: 500, ConnectedAt: at, Same: held}); err == nil {
		t.Fatalf("连接后出生的复用进程被认成布局窗格：%+v", p)
	}
	// 同一时刻出生（滴答取整后相等）是合法的调用者
	writeStat(proc, 500, 1, 1000)
	if _, err := r.ResolvePeer(context.Background(), Peer{PID: 500, ConnectedAt: at, Same: held}); err != nil {
		t.Fatalf("连接时刻出生的调用者应被接受：%v", err)
	}
}

// held 是一个始终成立的生命周期绑定：这些用例只考出生时间这道附加检查。
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

// 评审复现：开机三年后出生的进程，换算不溢出。
func TestBirthTimeDoesNotOverflowAfterYearsOfUptime(t *testing.T) {
	r := Resolver{ProcRoot: fakeProc(t, map[int]int{500: 1})}
	for _, seconds := range []int64{3 * 365 * 24 * 3600, 30 * 365 * 24 * 3600} {
		got, err := r.bornAt(uint64(seconds*100 + 37))
		want := time.Unix(1790000000, 0).Add(time.Duration(seconds)*time.Second + 370*time.Millisecond)
		if err != nil || !got.Equal(want) {
			t.Fatalf("开机 %d 秒后：got=%s want=%s err=%v", seconds, got, want, err)
		}
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
