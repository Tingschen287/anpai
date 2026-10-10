package deckd

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// deckd 把连接身份绑定到 pidfd：真机 socket 上，对端检查已接上且成立（n050 评审 R4）。
func TestPeerOfBindsTheConnectionToThePeerProcess(t *testing.T) {
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
	at := time.Now()
	peer, done, err := peerOf(srv, at)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if peer.PID != os.Getpid() || !peer.ConnectedAt.Equal(at) {
		t.Fatalf("对端应是本进程：%+v", peer)
	}
	if peer.Same == nil {
		t.Fatal("内核支持 SO_PEERPIDFD（本机 6.6），连接身份应绑定 pidfd")
	}
	if err := peer.Same(); err != nil {
		t.Fatalf("对端还活着：%v", err)
	}
}
