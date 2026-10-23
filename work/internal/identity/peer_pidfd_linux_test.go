//go:build linux

package identity

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/testsock"
)

// 真机：经 unix socket 取对端 pidfd，指向的就是本进程。
func TestPeerPIDFDOnRealSocket(t *testing.T) {
	path := testsock.Path(t, "s")
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
	path := testsock.Path(t, "s")
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
