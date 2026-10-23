package main

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/testsock"
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
	d := testsock.Dir(t)
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
	if len(shown) != 3 || !strings.Contains(shown[0], "等 deckd 确认没有进行中的操作") {
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
	d := testsock.Dir(t)
	// deckd 起来过、已经退出：单实例锁文件还在，但没人持有
	if err := os.WriteFile(filepath.Join(d, "deckd.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
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
			t.Fatalf("%s：单实例锁空闲（deckd 没在运行）应立刻返回", filepath.Base(sock))
		}
	}
}

// n049 评审 4：连不上 deckd 但单实例锁还被持有（deckd 正在关闭、手上还有请求），或锁文件根本不存在
// （deckd 还没起来），都不能放行；锁一放开就放行。
func TestFenceWaitsWhileDeckdLockIsHeldOrMissing(t *testing.T) {
	shortFence(t)
	d := testsock.Dir(t)
	sock := filepath.Join(d, "deckd.sock") // 不存在：连接失败
	shown := make(chan string, 64)
	done := make(chan struct{})
	go func() {
		fence(deckclient.New(sock), filepath.Join(d, "center.log"), func(m string) {
			select {
			case shown <- m:
			default:
			}
		})
		close(done)
	}()
	waitShown := func(want string) {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			select {
			case m := <-shown:
				if strings.Contains(m, want) {
					return
				}
			case <-done:
				t.Fatalf("不该放行（期待提示 %q）", want)
			case <-deadline:
				t.Fatalf("没看到提示 %q", want)
			}
		}
	}
	waitShown("还没起来") // 锁文件不存在
	lock := filepath.Join(d, "deckd.lock")
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	waitShown("还被持有") // deckd 正在关闭
	f.Close()         // deckd 退出：锁释放
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("锁释放后应放行")
	}
}
