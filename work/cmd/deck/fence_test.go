package main

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
)

// n049 评审 3 B1-A：deck center 起新 client 之前的栅栏，只有确认没有进行中的操作才返回。

func shortFence(t *testing.T) {
	t.Helper()
	ot, or := fenceTimeout, fenceRetry
	fenceTimeout, fenceRetry = 200*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { fenceTimeout, fenceRetry = ot, or })
}

// deckd 在、但一时没回应或答非所问：一直等，直到它确认（回了 fence）。
func TestFenceWaitsUntilDeckdConfirms(t *testing.T) {
	shortFence(t)
	d := t.TempDir()
	sock := filepath.Join(d, "deckd.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var n atomic.Int32
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch n.Add(1) {
		case 1, 2:
			time.Sleep(400 * time.Millisecond) // 超过栅栏单次的等待：有操作还没做完
		case 3:
			w.WriteHeader(503) // 答非所问：不能证明操作已结束
			w.Write([]byte(`{"error":"busy"}`))
			return
		}
		w.Write([]byte(`{"fenced":true}`))
	})}
	go srv.Serve(ln)
	defer srv.Close()
	var shown []string
	start := time.Now()
	fence(deckclient.New(sock), filepath.Join(d, "center.log"), func(m string) { shown = append(shown, m) })
	if got := n.Load(); got != 4 {
		t.Fatalf("应等到第 4 次（deckd 确认）才返回：%d 次", got)
	}
	if len(shown) != 3 || !strings.Contains(shown[0], "等 deckd 做完进行中的操作") {
		t.Fatalf("没确认的每一次都应在中间显示原因：%v", shown)
	}
	if time.Since(start) < 400*time.Millisecond {
		t.Fatal("返回得太早")
	}
	log, _ := os.ReadFile(filepath.Join(d, "center.log"))
	if !strings.Contains(string(log), "不起新 client") {
		t.Fatalf("应记进 center.log：%s", log)
	}
}

// deckd 根本没在运行（socket 不存在、或在但没人听）：不可能有进行中的操作，立刻返回。
func TestFenceReturnsWhenDeckdIsNotRunning(t *testing.T) {
	shortFence(t)
	d := t.TempDir()
	dead := filepath.Join(d, "dead.sock")
	ln, err := net.Listen("unix", dead)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close() // 留下 socket 文件，但没人听：连接被拒
	for _, sock := range []string{filepath.Join(d, "missing.sock"), dead} {
		done := make(chan struct{})
		go func() {
			fence(deckclient.New(sock), filepath.Join(d, "center.log"), func(string) { t.Error("deckd 没在运行时不该等") })
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s：deckd 没在运行应立刻返回", filepath.Base(sock))
		}
	}
}
