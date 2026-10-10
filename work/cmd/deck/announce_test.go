package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
)

// n049 评审 1 B1：announce 的通知只属于它那个 client。

type announceWorld struct {
	dir, gone string
	mu        sync.Mutex
	bodies    []map[string]any
	status    func(n int) int // 第 n 次请求返回的状态码
	c         *deckclient.Client
}

// 假 tmux：gone 文件不存在时列出 pid 123 的 client。假 deckd：记下请求体。
func newAnnounceWorld(t *testing.T, status func(n int) int, onRequest func(n int)) *announceWorld {
	t.Helper()
	d := t.TempDir()
	w := &announceWorld{dir: d, gone: filepath.Join(d, "gone"), status: status}
	script := "#!/bin/sh\nif [ ! -f '" + w.gone + "' ]; then printf '/dev/pts/123\\t123\\tdeck-idle\\t1\\t1\\n'; fi\n"
	if err := os.WriteFile(filepath.Join(d, "tmux"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", d)
	sock := filepath.Join(d, "http.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		w.mu.Lock()
		w.bodies = append(w.bodies, b)
		n := len(w.bodies)
		w.mu.Unlock()
		if onRequest != nil {
			onRequest(n)
		}
		code := w.status(n)
		rw.WriteHeader(code)
		if code != 200 {
			rw.Write([]byte(`{"error":"x"}`))
			return
		}
		rw.Write([]byte(`{}`))
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	w.c = deckclient.New(sock)
	return w
}

func (w *announceWorld) requests() []map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]map[string]any(nil), w.bodies...)
}

// 评审复现：第一次请求 503（deckd 还在启动），之后这个 client 退出了：不再重发。
func TestAnnounceStopsWhenItsClientIsGone(t *testing.T) {
	var w *announceWorld
	w = newAnnounceWorld(t, func(n int) int {
		if n == 1 {
			return 503
		}
		return 200
	}, func(n int) {
		if n == 1 {
			os.WriteFile(w.gone, []byte("gone"), 0o600)
		}
	})
	announce(context.Background(), w.c, filepath.Join(w.dir, "center.log"), filepath.Join(w.dir, "agent"), "reset", 123)
	if got := w.requests(); len(got) != 1 {
		t.Fatalf("client 退出后又发了通知（共 %d 次），可能隐藏新 client 的焦点", len(got))
	}
	log, _ := os.ReadFile(filepath.Join(w.dir, "center.log"))
	if len(log) == 0 {
		t.Fatal("应在 center.log 里记下没再通知的原因")
	}
}

// 请求带上 client 进程号，deckd 据此核对。
func TestAnnounceSendsItsClientPID(t *testing.T) {
	w := newAnnounceWorld(t, func(int) int { return 200 }, nil)
	announce(context.Background(), w.c, filepath.Join(w.dir, "center.log"), filepath.Join(w.dir, "agent"), "resync", 123)
	got := w.requests()
	if len(got) != 1 || got[0]["op"] != "resync" || got[0]["client"] != float64(123) {
		t.Fatalf("请求应带 op 与 client 进程号：%v", got)
	}
}

// client 退出（ctx 取消）后，还在等 deckd 的通知立刻停下。
func TestAnnounceStopsWhenCancelled(t *testing.T) {
	w := newAnnounceWorld(t, func(int) int { return 503 }, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		announce(ctx, w.c, filepath.Join(w.dir, "center.log"), filepath.Join(w.dir, "agent"), "reset", 123)
		close(done)
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("取消后 announce 没有停")
	}
	n := len(w.requests())
	time.Sleep(400 * time.Millisecond)
	if len(w.requests()) != n {
		t.Fatal("取消后还在发")
	}
}

// deckd 明确拒绝（例如 409：已不是中间现在的 client）：不重发。
func TestAnnounceDoesNotRetryRefusal(t *testing.T) {
	w := newAnnounceWorld(t, func(int) int { return 409 }, nil)
	announce(context.Background(), w.c, filepath.Join(w.dir, "center.log"), filepath.Join(w.dir, "agent"), "reset", 123)
	if got := w.requests(); len(got) != 1 {
		t.Fatalf("被拒绝后不应重发：%d", len(got))
	}
}
