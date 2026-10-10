package identity

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fakeSource []Pane

func (f fakeSource) Panes(context.Context) ([]Pane, error) { return f, nil }

// 造一个假的 /proc：pid → ppid。
func fakeProc(t *testing.T, parents map[int]int) string {
	root := t.TempDir()
	for pid, ppid := range parents {
		writeStat(root, pid, ppid, 1000)
	}
	os.WriteFile(filepath.Join(root, "stat"), []byte("cpu 1 2 3\nbtime 1790000000\n"), 0o644)
	return root
}

// writeStat 写一个假的 /proc/<pid>/stat：comm 故意带空格和右括号，第 22 列是出生滴答数。
func writeStat(root string, pid, ppid int, start uint64) {
	d := filepath.Join(root, strconv.Itoa(pid))
	os.MkdirAll(d, 0o755)
	fields := []string{"S", strconv.Itoa(ppid)}
	for i := 0; i < 17; i++ {
		fields = append(fields, "0")
	}
	fields = append(fields, strconv.FormatUint(start, 10), "0", "0")
	os.WriteFile(filepath.Join(d, "stat"), []byte(fmt.Sprintf("%d (we ird) x) %s", pid, strings.Join(fields, " "))), 0o644)
}

func TestResolveFindsNearestPaneOnTheChain(t *testing.T) {
	proc := fakeProc(t, map[int]int{500: 400, 400: 300, 300: 1})
	r := Resolver{ProcRoot: proc, Sources: []PaneSource{
		fakeSource{{Server: "/s/deck", PaneID: "%1", PanePID: 999, Session: "layout"}},
		fakeSource{{Server: "/s/agentdeck", PaneID: "%1", PanePID: 400, Session: "agentdeck_p3"}},
	}}
	p, err := r.Resolve(context.Background(), 500)
	if err != nil || p.Session != "agentdeck_p3" || p.Key() != "/s/agentdeck|%1" {
		t.Fatalf("应解析到 agentdeck_p3：%+v %v", p, err)
	}
}

// 两个 server 上相同的 pane id 不会混：键包含 server。
func TestSamePaneIDOnTwoServersIsDistinct(t *testing.T) {
	a := Pane{Server: "/s/deck", PaneID: "%1"}
	b := Pane{Server: "/s/agentdeck", PaneID: "%1"}
	if a.Key() == b.Key() {
		t.Fatal("不同 server 的同名 pane 键不该相同")
	}
}

func TestOutsideCallerIsUnknown(t *testing.T) {
	proc := fakeProc(t, map[int]int{700: 600, 600: 1})
	r := Resolver{ProcRoot: proc, Sources: []PaneSource{fakeSource{{Server: "/s", PaneID: "%1", PanePID: 400}}}}
	if _, err := r.Resolve(context.Background(), 700); !errors.Is(err, ErrUnknown) {
		t.Fatalf("链外调用者应为未知：%v", err)
	}
}

// 调用进程已经消失：不能当成未知但无害，要报错（调用方据此拒绝写入）。
func TestVanishedCallerIsAnError(t *testing.T) {
	r := Resolver{ProcRoot: t.TempDir()}
	_, err := r.Resolve(context.Background(), 12345)
	if err == nil || errors.Is(err, ErrUnknown) {
		t.Fatalf("消失的调用者应报错而非未知：%v", err)
	}
}

// 列窗格失败也是错误，不降级成未知。
type failingSource struct{}

func (failingSource) Panes(context.Context) ([]Pane, error) { return nil, errors.New("boom") }

func TestPaneListingFailureIsAnError(t *testing.T) {
	proc := fakeProc(t, map[int]int{500: 1})
	r := Resolver{ProcRoot: proc, Sources: []PaneSource{failingSource{}}}
	if _, err := r.Resolve(context.Background(), 500); err == nil || errors.Is(err, ErrUnknown) {
		t.Fatalf("列窗格失败应报错：%v", err)
	}
}

// SO_PEERCRED：同进程拨号，对端 pid 就是自己。
func TestPeerPIDOverRealUnixSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan int, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			done <- -1
			return
		}
		pid, err := PeerPID(c.(*net.UnixConn))
		if err != nil {
			pid = -2
		}
		done <- pid
		c.Close()
	}()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got := <-done; got != os.Getpid() {
		t.Fatalf("对端 pid 应为本进程 %d，得到 %d", os.Getpid(), got)
	}
}

// 真实 tmux：窗格里的孙进程被解析到该窗格；窗格外的进程是未知。
func TestRealTmuxPaneResolution(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("没有 tmux")
	}
	dir, err := os.MkdirTemp("/tmp", "idt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s")
	tm := func(a ...string) *exec.Cmd {
		return exec.Command("tmux", append([]string{"-S", sock, "-f", "/dev/null"}, a...)...)
	}
	if out, err := tm("new-session", "-d", "-s", "probe", "sh -c 'sleep 30 & wait'").CombinedOutput(); err != nil {
		t.Fatalf("起 tmux 失败：%v %s", err, out)
	}
	defer tm("kill-server").Run()
	src := TmuxSource{Socket: sock}
	var sleepPID int
	for i := 0; i < 50 && sleepPID == 0; i++ {
		time.Sleep(50 * time.Millisecond)
		panes, err := src.Panes(context.Background())
		if err != nil || len(panes) != 1 {
			continue
		}
		out, _ := exec.Command("pgrep", "-P", strconv.Itoa(panes[0].PanePID), "sleep").Output()
		if s := strings.TrimSpace(string(out)); s != "" {
			sleepPID, _ = strconv.Atoi(strings.Fields(s)[0])
		}
	}
	if sleepPID == 0 {
		t.Fatal("找不到窗格里的 sleep 进程")
	}
	r := Resolver{Sources: []PaneSource{src}}
	p, err := r.Resolve(context.Background(), sleepPID)
	if err != nil || p.Session != "probe" || p.Server != sock {
		t.Fatalf("窗格里的进程应解析到 probe：%+v %v", p, err)
	}
	if _, err := r.Resolve(context.Background(), os.Getpid()); !errors.Is(err, ErrUnknown) {
		t.Fatalf("测试进程自己应为未知：%v", err)
	}
}

// server 没在跑：空列表，不是错误。
func TestMissingServerIsEmpty(t *testing.T) {
	panes, err := TmuxSource{Socket: filepath.Join(t.TempDir(), "none")}.Panes(context.Background())
	if err != nil || len(panes) != 0 {
		t.Fatalf("没在跑的 server 应为空：%v %v", panes, err)
	}
}

// 列窗格的过程中调用进程退出、pid 被一个布局窗格的新进程复用：第二次读链时出生时间对不上，报错而不是判成 owner（B6）。
type swapSource struct {
	panes []Pane
	swap  func()
}

func (s swapSource) Panes(context.Context) ([]Pane, error) {
	s.swap()
	return s.panes, nil
}

func TestPIDReuseDuringResolutionIsDetected(t *testing.T) {
	proc := fakeProc(t, map[int]int{500: 400, 400: 1})
	src := swapSource{panes: []Pane{{Server: "/s/deck", PaneID: "%1", PanePID: 500, Session: "layout"}}, swap: func() {
		writeStat(proc, 500, 999, 5000) // 原 500 退出，新进程复用 500，父进程与出生时间都变了
	}}
	r := Resolver{ProcRoot: proc, Sources: []PaneSource{src}}
	if p, err := r.Resolve(context.Background(), 500); err == nil {
		t.Fatalf("pid 复用应报错，却判成了 %+v", p)
	}
}

// 连接之后才出生的进程：疑似 pid 复用，拒绝。
func TestProcessBornAfterConnectionIsRejected(t *testing.T) {
	proc := fakeProc(t, map[int]int{500: 1})
	writeStat(proc, 500, 1, 100*3600) // 开机后 1 小时出生
	r := Resolver{ProcRoot: proc}
	connectedAt := time.Unix(1790000000, 0).Add(30 * time.Minute) // 连接建立在开机后 30 分钟
	if _, err := r.ResolveAt(context.Background(), 500, connectedAt); err == nil || !strings.Contains(err.Error(), "复用") {
		t.Fatalf("连接之后出生的进程应被拒：%v", err)
	}
	connectedAt = time.Unix(1790000000, 0).Add(2 * time.Hour)
	if _, err := r.ResolveAt(context.Background(), 500, connectedAt); !errors.Is(err, ErrUnknown) {
		t.Fatalf("连接之前出生的进程应正常判定：%v", err)
	}
}
