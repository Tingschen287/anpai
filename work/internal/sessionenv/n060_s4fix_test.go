package sessionenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// n060 段 4 返工：触发时机（启动读一次，之后按键刷新）与路径绑定（FrontDir）。登录 shell 一律是假的 RunFunc。

type ctl struct {
	n     atomic.Int32
	mu    sync.Mutex
	login string
	fail  bool
	gate  chan struct{}
	in    chan struct{}
}

func (c *ctl) run(t *testing.T) RunFunc {
	return func(ctx context.Context, _ string, args, _ []string) ([]byte, error) {
		c.n.Add(1)
		c.mu.Lock()
		gate, in, login, fail := c.gate, c.in, c.login, c.fail
		c.mu.Unlock()
		if in != nil {
			in <- struct{}{}
		}
		if gate != nil {
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if fail {
			return nil, errors.New("exit status 1")
		}
		m := markFrom(t, args)
		return []byte(m + "B" + login + m + "E"), nil
	}
}

func (c *ctl) set(login string, fail bool) { c.mu.Lock(); c.login, c.fail = login, fail; c.mu.Unlock() }
func (c *ctl) hold() (release func()) {
	c.mu.Lock()
	g := make(chan struct{})
	c.gate, c.in = g, make(chan struct{}, 16)
	c.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { c.mu.Lock(); c.gate = nil; c.mu.Unlock(); close(g) }) }
}

func newCtlResolver(t *testing.T, c *ctl) *Resolver {
	return fakeResolver(map[string]string{"SHELL": okShell(t), "PATH": "/usr/bin"}, c.run(t))
}

// 取快照从不运行登录 shell：Start 读一次，Get 与 State 无论调多少次、过多久都不再读。
func TestN060S4FixGetNeverRunsTheShellAgain(t *testing.T) {
	c := &ctl{login: "/a"}
	r := newCtlResolver(t, c)
	now := time.Unix(1_000, 0)
	var mu sync.Mutex
	r.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	r.Start()
	r.Start() // 重复 Start 不叠加
	for i := 0; i < 50; i++ {
		mu.Lock()
		now = now.Add(time.Hour)
		mu.Unlock()
		r.Get(context.Background())
		r.State(context.Background())
	}
	if n := c.n.Load(); n != 1 {
		t.Fatalf("启动读一次，之后不再读：%d", n)
	}
	// 没人 Start 过：第一个要用的人触发那唯一一次
	c2 := &ctl{login: "/b"}
	r2 := newCtlResolver(t, c2)
	r2.Get(context.Background())
	r2.Get(context.Background())
	if c2.n.Load() != 1 {
		t.Fatalf("%d", c2.n.Load())
	}
}

// 刷新只有一个在跑：读的过程中再按（连按、多个窗口）合并；读完之后再按才再读。
func TestN060S4FixRefreshCoalescesAndRunsOneAtATime(t *testing.T) {
	c := &ctl{login: "/a"}
	r := newCtlResolver(t, c)
	r.Get(context.Background())
	release := c.hold()
	defer release()
	c.set("/b", false)
	var started atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r.Refresh() {
				started.Add(1)
			}
		}()
	}
	wg.Wait()
	<-c.in
	if started.Load() != 1 || c.n.Load() != 2 {
		t.Fatalf("20 次同时按只起一次：起了 %d 次，运行 %d 次", started.Load(), c.n.Load())
	}
	if !r.State(context.Background()).Refreshing {
		t.Fatal("读在跑时应报告正在刷新")
	}
	if r.Refresh() {
		t.Fatal("读在跑时再按应合并")
	}
	release()
	waitIdle(t, r)
	if s := r.Get(context.Background()); s.Path != "/b:/usr/bin" {
		t.Fatalf("读完换成新的：%q", s.Path)
	}
	if !r.Refresh() {
		t.Fatal("读完之后再按应能再读")
	}
	waitIdle(t, r)
	if c.n.Load() != 3 {
		t.Fatalf("%d", c.n.Load())
	}
}

// 刷新期间 Get / State 不等：立刻返回旧快照（慢 rc 拖不住派工）。
func TestN060S4FixGetDoesNotWaitForARefresh(t *testing.T) {
	c := &ctl{login: "/old"}
	r := newCtlResolver(t, c)
	r.Wait = time.Minute
	r.Get(context.Background())
	release := c.hold()
	defer release()
	c.set("/new", false)
	r.Refresh()
	<-c.in
	done := make(chan Snapshot, 1)
	go func() { done <- r.Get(context.Background()) }()
	select {
	case s := <-done:
		if s.Path != "/old:/usr/bin" {
			t.Fatalf("刷新期间用旧快照：%q", s.Path)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Get 被正在跑的刷新卡住了")
	}
	release()
	waitIdle(t, r)
	if s := r.Get(context.Background()); s.Path != "/new:/usr/bin" {
		t.Fatalf("%q", s.Path)
	}
}

// 第一次读还没完成就有人要用：等这一次（不另起读）；有上限：读超过探针超时就退回进程自己的 PATH，不会一直等。
func TestN060S4FixFirstGetWaitsForTheFirstReadWithinALimit(t *testing.T) {
	c := &ctl{login: "/a"}
	release := c.hold()
	r := newCtlResolver(t, c)
	r.Start()
	<-c.in
	got := make(chan Snapshot, 1)
	go func() { got <- r.Get(context.Background()) }()
	select {
	case <-got:
		t.Fatal("第一次读没完成，Get 应等")
	case <-time.After(100 * time.Millisecond):
	}
	release()
	if s := <-got; s.Source != SourceLoginShell || s.Path != "/a:/usr/bin" || c.n.Load() != 1 {
		t.Fatalf("等到第一次读的结果，没有另起一次：%+v n=%d", s, c.n.Load())
	}
	// 上限
	c2 := &ctl{login: "/a"}
	c2.hold()
	r2 := newCtlResolver(t, c2)
	r2.Wait = 150 * time.Millisecond
	start := time.Now()
	s := r2.Get(context.Background())
	var ce *i18n.Error
	if time.Since(start) > 3*time.Second || s.Source != SourceProcess || !errors.As(s.Fallback, &ce) || ce.Key != "err.sessionenv.timeout" {
		t.Fatalf("第一次读超过上限：应在上限内退回并说明：%v %+v", time.Since(start), s)
	}
}

// 启动读失败：回退并说明；按键成功后恢复。按键刷新失败时保留好快照，原因记在 RefreshErr；再成功后清掉。
func TestN060S4FixFailedStartupAndFailedRefreshKeepTheRightSnapshot(t *testing.T) {
	c := &ctl{fail: true}
	r := newCtlResolver(t, c)
	st := r.State(context.Background())
	if st.Snapshot.Source != SourceProcess || st.Snapshot.Fallback == nil || st.RefreshErr != nil || c.n.Load() != 1 {
		t.Fatalf("启动读失败：%+v", st)
	}
	c.set("/good", false)
	r.Refresh()
	waitIdle(t, r)
	st = r.State(context.Background())
	if st.Snapshot.Source != SourceLoginShell || st.Snapshot.Fallback != nil || st.RefreshErr != nil {
		t.Fatalf("按键恢复：%+v", st)
	}
	good := st.Snapshot
	c.set("", true)
	r.Refresh()
	waitIdle(t, r)
	st = r.State(context.Background())
	if st.Snapshot.Path != good.Path || st.Snapshot.Fallback != nil || st.RefreshErr == nil {
		t.Fatalf("刷新失败：保留上一份好快照、记下原因：%+v", st)
	}
	c.set("/better", false)
	r.Refresh()
	waitIdle(t, r)
	if st = r.State(context.Background()); st.RefreshErr != nil || !strings.HasPrefix(st.Snapshot.Path, "/better") {
		t.Fatalf("再成功：%+v", st)
	}
	// 启动失败、按键又失败：快照仍是回退，原因换成最新一次的
	c3 := &ctl{fail: true}
	r3 := newCtlResolver(t, c3)
	r3.Get(context.Background())
	r3.Refresh()
	waitIdle(t, r3)
	if st := r3.State(context.Background()); st.Snapshot.Fallback == nil || st.RefreshErr == nil || st.Snapshot.Source != SourceProcess {
		t.Fatalf("%+v", st)
	}
}

// 没开解析的（NewStatic、Run 为 nil、nil 指针）：Refresh 什么都不做，Enabled 为假，不会起任何读。
func TestN060S4FixDisabledResolverNeverRefreshes(t *testing.T) {
	for name, r := range map[string]*Resolver{"static": NewStatic(), "norun": NewLoginResolver(nil), "nil": nil} {
		if r.Enabled() || r.Refresh() {
			t.Errorf("%s：没开解析", name)
		}
		r.Start()
	}
	if !newCtlResolver(t, &ctl{}).Enabled() {
		t.Fatal("开了解析的 Enabled 为真")
	}
}

// 关闭 deckd（Base 取消）时正在跑的读被取消，不会留着。
func TestN060S4FixBaseCancelStopsTheRead(t *testing.T) {
	c := &ctl{login: "/a"}
	c.hold()
	r := newCtlResolver(t, c)
	r.Wait = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	r.Base = ctx
	r.Start()
	<-c.in
	cancel()
	waitIdle(t, r)
}

// FrontDir：核准文件所在的目录放到最前，其余顺序不变、去重（含 dir 在后面的那一处）、丢相对目录。
func TestN060S4FixFrontDirShapes(t *testing.T) {
	for name, tc := range map[string]struct{ path, dir, want string }{
		"目录在中间":     {"/a:/b:/c", "/b", "/b:/a:/c"},
		"已经在最前":     {"/a:/b", "/a", "/a:/b"},
		"目录不在 PATH": {"/a:/b", "/x", "/x:/a:/b"},
		"重复的目录":     {"/a:/b:/a:/c:/b", "/c", "/c:/a:/b"},
		"带多余斜杠与点":   {"/a/:/b/./", "/b/", "/b:/a"},
		"相对目录丢掉":    {"rel:/a:.::/b", "/b", "/b:/a"},
		"空 PATH":    {"", "/x", "/x"},
	} {
		got, ok := FrontDir(tc.path, tc.dir)
		if !ok || got != tc.want {
			t.Errorf("%s：FrontDir(%q, %q) = %q %v，应为 %q", name, tc.path, tc.dir, got, ok, tc.want)
		}
	}
	for name, dir := range map[string]string{"相对": "rel/dir", "空": "", "控制字符": "/a\x01b", "过长": "/" + strings.Repeat("a", maxEntry+1)} {
		if got, ok := FrontDir("/a:/b", dir); ok || got != "/a:/b" {
			t.Errorf("%s：不合格的目录不能前置：%q %v", name, got, ok)
		}
	}
}

// 前置之后，按这份 PATH 找到的就是核准的那个文件：前面目录里新装的同名文件顶替不了；符号链接目录按路径本身算（不解开）。
func TestN060S4FixFrontDirBindsWhichFileLookPathFinds(t *testing.T) {
	root := t.TempDir()
	early, old := filepath.Join(root, "early"), filepath.Join(root, "old")
	for _, d := range []string{early, old} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mk := func(dir string) string {
		p := filepath.Join(dir, "codex")
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	approved := mk(old)
	path := early + ":" + old
	if LookPath(path, "codex") != approved {
		t.Fatal("前提：此刻解析到核准的文件")
	}
	shadow := mk(early) // 核准之后、启动之前：前面的目录里新装了同名文件
	if LookPath(path, "codex") != shadow {
		t.Fatal("前提：不绑定时被顶替")
	}
	bound, ok := FrontDir(path, filepath.Dir(approved))
	if !ok || LookPath(bound, "codex") != approved {
		t.Fatalf("绑定之后仍解析到核准的文件：%q %v", bound, ok)
	}
	// 符号链接目录：路径按字面算，不解开
	link := filepath.Join(root, "link")
	if err := os.Symlink(old, link); err != nil {
		t.Fatal(err)
	}
	bound, _ = FrontDir(early+":"+link+":"+old, link)
	if LookPath(bound, "codex") != filepath.Join(link, "codex") || !strings.HasPrefix(bound, link+":") {
		t.Fatalf("符号链接目录按字面前置：%q", bound)
	}
	// 核准的文件不再可执行：按绑定后的 PATH 找到的不是它（调用方据此拒绝启动）
	if err := os.Chmod(approved, 0o644); err != nil {
		t.Fatal(err)
	}
	bound, _ = FrontDir(path, filepath.Dir(approved))
	if LookPath(bound, "codex") == approved {
		t.Fatal("不可执行的文件不会被解析到")
	}
}
