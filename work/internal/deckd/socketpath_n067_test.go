package deckd

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/sockpath"
	"github.com/Tingschen287/terminal-deck/work/internal/testsock"
)

// 状态目录很长（隔离测试、沙箱里常见）：socket 退到 /tmp/deck-<uid>/<哈希>/，锁文件跟着 socket 走；
// 状态目录不同，退路目录也不同。
func TestSocketPathFallsBackWhenTheStateDirIsTooLong(t *testing.T) {
	root := testsock.Dir(t)
	long := func(name string) deckstore.Dirs {
		return deckstore.Dirs{State: filepath.Join(root, name, strings.Repeat("s", sockpath.Max))}
	}
	a, b := long("a"), long("b")
	sa, sb := SocketPath(a), SocketPath(b)
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(sa)); os.RemoveAll(filepath.Dir(sb)) })
	if len(sa) > sockpath.Max || filepath.Base(sa) != "deckd.sock" {
		t.Fatalf("应退到放得下的路径、文件名不变：%q（%d 字节）", sa, len(sa))
	}
	if sa == sb || LockPathForSocket(sa) == LockPathForSocket(sb) || filepath.Dir(LockPathForSocket(sa)) != filepath.Dir(sa) {
		t.Fatalf("不同状态目录各得其所，锁与 socket 同目录：%q %q", sa, sb)
	}
	if again := SocketPath(a); again != sa {
		t.Fatalf("同一个状态目录应永远得到同一条路径：%q %q", sa, again)
	}
	// 状态目录够短时，路径和以前完全一样
	short := deckstore.Dirs{State: filepath.Join(root, "state")}
	if got := SocketPath(short); got != filepath.Join(short.State, "deckd.sock") {
		t.Fatalf("短路径不该变：%q", got)
	}
}

// 真实守护进程在超长的状态目录下起得来：监听退路上的 socket，对端 uid/pid 核对照常通过（macOS 上走 LOCAL_PEERCRED），
// 单实例锁在退路目录里，第二个实例起不来。
func TestRealDaemonWithTooLongStateDirListensOnTheFallbackSocket(t *testing.T) {
	root := testsock.Dir(t)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "xdgstate"))
	cfg := Config{Dirs: deckstore.Dirs{Config: filepath.Join(root, "cfg"), State: filepath.Join(root, strings.Repeat("s", sockpath.Max), "state")},
		LayoutSocket: filepath.Join(root, "no-layout"), AgentSocket: filepath.Join(root, "no-agent"), DashboardURL: "http://127.0.0.1:1"}
	sock, err := SocketPathE(cfg.Dirs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(sock)) })
	if len(sock) > sockpath.Max {
		t.Fatalf("前提：应退到短路径：%q", sock)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, nil) }()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	c := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	resp, err := c.Get("http://deckd/v1/whoami")
	if err != nil {
		cancel()
		t.Fatalf("连不上退路上的 socket：%v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"role":"unknown"`) {
		t.Errorf("测试进程不在任何窗格里，应判为未知：%d %s", resp.StatusCode, body)
	}
	if _, err := os.Stat(LockPathForSocket(sock)); err != nil {
		t.Errorf("单实例锁应在退路目录里：%v", err)
	}
	if err := Run(context.Background(), cfg, nil); err == nil || !strings.Contains(err.Error(), "已经在运行") {
		t.Errorf("第二个 deckd 应拒绝启动：%v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("deckd 退出出错：%v", err)
	}
}

// 退路目录被别人占着：SocketPathE 与 Run 都带着原因报错，不起服务、不拿超长路径去 bind。
func TestSocketPathEAndRunReportWhyTheFallbackWasRefused(t *testing.T) {
	root := testsock.Dir(t)
	defer sockpath.SetFallbackRoot(root)()
	if err := os.Symlink(testsock.Dir(t), sockpath.FallbackDir()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "xdgstate"))
	cfg := Config{Dirs: deckstore.Dirs{Config: filepath.Join(root, "cfg"), State: filepath.Join(root, strings.Repeat("s", sockpath.Max), "state")}}
	if p, err := SocketPathE(cfg.Dirs); err == nil || !strings.Contains(err.Error(), "不是目录") {
		t.Fatalf("SocketPathE 应带着原因报错：%q %v", p, err)
	}
	if err := Run(context.Background(), cfg, nil); err == nil || !strings.Contains(err.Error(), "不是目录") {
		t.Fatalf("Run 应把原因原样传出来：%v", err)
	}
}
