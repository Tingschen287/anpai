package deckd

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// net.Pipe 提供内存连接，不创建 TCP/Unix socket，不需要 bind。
type n069shutAddr struct{}

func (n069shutAddr) Network() string { return "memory" }
func (n069shutAddr) String() string  { return "review-1" }

type n069shutListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (l *n069shutListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *n069shutListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *n069shutListener) Addr() net.Addr { return n069shutAddr{} }

// 不监听 socket、不启动 deckd 或 tmux；调用原 Server.Handler、原 Store。
// 模拟生产 Run：取消 background，HTTP drain 到期后 WaitBackground，再交出 writer lock。
func TestN069PanelWorkIsPartOfShutdownFence(t *testing.T) {
	root := t.TempDir()
	dirs := deckstore.Dirs{Config: filepath.Join(root, "cfg"), State: filepath.Join(root, "state")}
	st, err := deckstore.Open(dirs)
	if err != nil {
		t.Fatal(err)
	}
	life, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	var applierCtx context.Context
	s := NewServer(Config{Dirs: dirs, PanelApplier: func(ctx context.Context, _ []deckstore.PanelDef) ([]string, []string, error) {
		if calls.Add(1) == 1 {
			applierCtx = ctx
			close(started)
			<-release
		}
		return nil, nil, nil
	}}, st, nil, nil, nil, nil)
	s.WithBackground(life)
	secondEntered := make(chan struct{})
	s.callerFn = func(r *http.Request) Caller {
		if r.URL.Path == "/v1/panels/quota" {
			close(secondEntered)
		}
		return Caller{Role: RoleOwner, Pane: "isolated|%1"}
	}
	ln := &n069shutListener{conns: make(chan net.Conn), done: make(chan struct{})}
	hs := &http.Server{Handler: s.Handler()}
	go hs.Serve(ln)
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		c, other := net.Pipe()
		select {
		case ln.conns <- other:
			return c, nil
		case <-ctx.Done():
			c.Close()
			other.Close()
			return nil, ctx.Err()
		}
	}}
	defer tr.CloseIdleConnections()
	hc := &http.Client{Transport: tr}
	post := func(path string, done chan struct{}) {
		defer close(done)
		r, e := hc.Post("http://review"+path, "application/json", strings.NewReader(map[string]string{"/v1/panels/progress": `{"on":true}`, "/v1/panels/quota": `{"on":false}`}[path]))
		if e != nil {
			t.Error(e)
			return
		}
		r.Body.Close()
	}
	go post("/v1/panels/progress", finished)
	<-started
	secondDone := make(chan struct{})
	go post("/v1/panels/quota", secondDone)
	<-secondEntered
	cancel() // 对应 Run 收到关闭信号；HTTP Shutdown 的 3s drain 结束后还在跑的请求不会被 Shutdown 终止。
	drain, cancelDrain := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelDrain()
	if err := hs.Shutdown(drain); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("应排空超时：%v", err)
	}
	waited := make(chan struct{})
	go func() { s.WaitBackground(); close(waited) }()
	escaped := false
	select {
	case <-waited:
		escaped = true
		t.Log("WaitBackground 已返回；首个挂拔仍停在 applier，第二个请求仍排在 panelMu")
	case <-time.After(100 * time.Millisecond):
	}
	ctxStillLive := applierCtx.Err() == nil
	// 修好之后：deckd 关闭时 applier 的 ctx 随生命周期取消；WaitBackground 要等进行中的挂拔做完（此刻还卡在 applier 里，所以没有返回）；
	// 排队的第二个请求醒来后不写盘、回 503（Codex 审查 1 的 R1）
	if escaped || ctxStillLive {
		t.Fatalf("关闭栅栏必须等挂拔：WaitBackground 已返回=%v，applier ctx 仍活着=%v", escaped, ctxStillLive)
	}
	close(release)
	<-finished
	<-secondDone
	select {
	case <-waited:
	case <-time.After(2 * time.Second):
		t.Fatal("挂拔做完后 WaitBackground 应返回")
	}
	for _, p := range st.Panels() {
		if p.Name == "quota" && !p.On {
			t.Fatal("deckd 关闭后排队的挂拔不该再写盘：额度被拔掉了")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("排队的请求不该走到 applier：调用数 %d", calls.Load())
	}
}

// 关闭之后到的新请求直接 503，不写盘。
func TestN069PanelRequestAfterShutdownIsRefused(t *testing.T) {
	f := newFixture(t)
	var calls atomic.Int32
	f.srv.cfg.PanelApplier = func(context.Context, []deckstore.PanelDef) ([]string, []string, error) {
		calls.Add(1)
		return nil, nil, nil
	}
	life, cancel := context.WithCancel(context.Background())
	f.srv.WithBackground(life)
	cancel()
	code, body := f.do("owner", "POST", "/v1/panels/quota", `{"on":false}`)
	if code != 503 || calls.Load() != 0 || !onOf(f.store.Panels(), "quota") {
		t.Fatalf("%d %s calls=%d", code, body, calls.Load())
	}
	f.srv.WaitBackground() // 没有登记的挂拔，立刻返回
}

// 总 deadline（30 秒）与客户端断开的区别：客户端断开不取消，deckd 生命周期取消才取消。
func TestN069PanelApplierCtxFollowsDaemonNotClient(t *testing.T) {
	f := newFixture(t)
	life, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.srv.WithBackground(life)
	var got context.Context
	f.srv.cfg.PanelApplier = func(ctx context.Context, _ []deckstore.PanelDef) ([]string, []string, error) {
		got = ctx
		return nil, nil, nil
	}
	f.do("owner", "POST", "/v1/panels/quota", `{"on":false}`)
	if got == nil {
		t.Fatal("applier 没被调用")
	}
	if dl, ok := got.Deadline(); !ok || time.Until(dl) > 31*time.Second || time.Until(dl) < 20*time.Second {
		t.Fatalf("总期限应约 30 秒：%v %v", dl, ok)
	}
	cancel()
	if got.Err() == nil {
		t.Fatal("deckd 关闭时 applier 的 ctx 应取消")
	}
}
