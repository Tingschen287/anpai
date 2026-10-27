package deckd

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/identity"
	"github.com/Tingschen287/terminal-deck/work/internal/testsock"
)

type paneOf identity.Pane

func (p paneOf) ResolvePeer(context.Context, identity.Peer) (identity.Pane, error) {
	return identity.Pane(p), nil
}

// 没有 pidfd 绑定（旧内核）时不授予任何角色：哪怕进程链指向布局窗格，也按未知调用者处理且不能写（n050 评审 A3-1）。
// 有绑定时照常判为 owner。
func TestCallerWithoutPIDFDGetsNoRole(t *testing.T) {
	f := newFixture(t)
	srv := NewServer(Config{Dirs: deckstore.Dirs{Config: t.TempDir(), State: t.TempDir()}, LayoutSocket: "/s/deck"}, f.store,
		paneOf{Server: "/s/deck", PaneID: "%1", PanePID: 500, Session: "layout"}, nil, nil, nil)
	c := srv.classify(context.Background(), identity.Peer{PID: 500, ConnectedAt: time.Now()})
	if c.Role != RoleUnknown || !errors.Is(c.Err, identity.ErrNoLifetimeBinding) {
		t.Fatalf("没有 pidfd 绑定应是未知调用者且带错误：%+v", c)
	}
	c = srv.classify(context.Background(), identity.Peer{PID: 500, Same: func() error { return nil }})
	if c.Role != RoleOwner || c.Err != nil {
		t.Fatalf("有 pidfd 绑定应判为 owner：%+v", c)
	}
}

// deckd 把连接身份绑定到 pidfd：真机 socket 上，对端检查已接上且成立（n050 评审 R4）。
// 内核不支持（6.5 以前）时跳过：那种机器上 deckd 不授予任何写权限（见上一条测试）。
func TestPeerOfBindsTheConnectionToThePeerProcess(t *testing.T) {
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
	at := time.Now()
	if _, release, err := identity.PeerLifetime(srv, os.Getpid()); errors.Is(err, identity.ErrNoPIDFD) {
		t.Skip("内核不支持 SO_PEERPIDFD")
	} else if err == nil {
		release()
	}
	peer, done, err := peerOf(srv, at)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if peer.PID != os.Getpid() || !peer.ConnectedAt.Equal(at) {
		t.Fatalf("对端应是本进程：%+v", peer)
	}
	if peer.Same == nil {
		t.Fatal("内核支持 SO_PEERPIDFD，连接身份应绑定 pidfd")
	}
	if err := peer.Same(); err != nil {
		t.Fatalf("对端还活着：%v", err)
	}
}
