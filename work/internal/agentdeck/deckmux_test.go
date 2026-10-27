package agentdeck

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 真 tmux（n049）：两台隔离 server，中间 client 就跑在布局 server 的窗格里——与 deck up 的结构一致。
func muxFixture(t *testing.T) (layout, agent string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("没有 tmux")
	}
	dir, err := os.MkdirTemp("", "dmx")
	if err != nil {
		t.Fatal(err)
	}
	layout, agent = filepath.Join(dir, "deck"), filepath.Join(dir, "agent")
	t.Cleanup(func() {
		for _, s := range []string{layout, agent} {
			c := exec.Command("tmux", "-S", s, "kill-server")
			c.Env = withoutTmux(os.Environ())
			c.Run()
		}
		os.RemoveAll(dir)
	})
	return layout, agent
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等不到：%s", what)
}

func TestDeckMuxCenterClientLifecycle(t *testing.T) {
	ctx := context.Background()
	layout, agent := muxFixture(t)
	if err := EnsureSession(ctx, agent, "deck-idle", "sleep 600"); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSession(ctx, agent, "deck-idle", "sleep 600"); err != nil {
		t.Fatal("再确保一次应无副作用：", err)
	}
	if err := EnsureSession(ctx, agent, "agentdeck_a", "printf 'hello from a\\n'; sleep 600"); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(filepath.Dir(layout), "deck.conf")
	os.WriteFile(conf, []byte("set -g prefix None\nset -g status off\n"), 0o600)
	center, err := LayoutNewSession(ctx, layout, conf, "deck", "main", "160", "40",
		"tmux -S "+agent+" attach-session -r -t \"=deck-idle\"") // 目标加引号：macOS 的 zsh 会把裸的 =name 当命令路径展开（n067）
	if err != nil {
		t.Fatal(err)
	}
	if !LayoutHasSession(ctx, layout, "deck") || LayoutHasSession(ctx, layout, "dec") {
		t.Fatal("has-session 应精确匹配")
	}
	if err := LayoutSetRole(ctx, layout, center, "center"); err != nil {
		t.Fatal(err)
	}
	var c TmuxClient
	waitFor(t, "中间 client 连上 agent server", func() bool {
		list, _ := ListTmuxClients(ctx, agent)
		if len(list) == 1 {
			c = list[0]
			return true
		}
		return false
	})
	if !c.ReadOnly || c.Session != "deck-idle" {
		t.Fatalf("中间 client 应以只读连到 deck-idle：%+v", c)
	}
	// client 的进程是中间窗格进程本身（窗格直接跑 tmux attach）
	panes, err := LayoutPanes(ctx, layout)
	if err != nil || len(panes) != 1 || panes[0].Role != "center" || panes[0].PID != c.PID {
		t.Fatalf("布局窗格应是 center 且就是 client 进程：%+v %v（client pid %d）", panes, err, c.PID)
	}
	// 只读翻转两次回到原状
	if err := ToggleClientReadOnly(ctx, agent, c.TTY, c.Session); err != nil {
		t.Fatal(err)
	}
	if s, _ := ClientState(ctx, agent, c.TTY); s.ReadOnly || s.Session != "deck-idle" {
		// 有多个会话时，只写 -r 不带 -t 会把 client 切到「最近用过」的 agentdeck_a（n049 实测）
		t.Fatalf("翻转后应可写，且仍在 deck-idle：%+v", s)
	}
	ToggleClientReadOnly(ctx, agent, c.TTY, c.Session)
	if s, _ := ClientState(ctx, agent, c.TTY); !s.ReadOnly {
		t.Fatal("再翻转应回到只读")
	}
	// 切会话：精确匹配，前缀不算
	if err := SwitchClient(ctx, agent, c.TTY, "agentdeck"); err == nil {
		t.Fatal("会话名前缀不应切过去")
	}
	if err := SwitchClient(ctx, agent, c.TTY, "agentdeck_a"); err != nil {
		t.Fatal(err)
	}
	if s, _ := ClientState(ctx, agent, c.TTY); s.Session != "agentdeck_a" || !s.ReadOnly {
		t.Fatalf("切换后应在 agentdeck_a 且仍只读：%+v", s)
	}
	lines, err := CaptureSession(ctx, agent, "agentdeck_a")
	if err != nil || !strings.Contains(strings.Join(lines, "\n"), "hello from a") {
		t.Fatalf("应抓到会话画面：%q %v", lines, err)
	}
	if _, err := ClientState(ctx, agent, "/dev/pts/nonexistent"); err == nil {
		t.Fatal("不存在的 client 应报错")
	}
}

func TestDeckMuxLayoutSplitParkUnpark(t *testing.T) {
	ctx := context.Background()
	layout, _ := muxFixture(t)
	conf := filepath.Join(filepath.Dir(layout), "deck.conf")
	os.WriteFile(conf, []byte("set -g prefix None\n"), 0o600)
	center, err := LayoutNewSession(ctx, layout, conf, "deck", "main", "160", "40", "sleep 600")
	if err != nil {
		t.Fatal(err)
	}
	side, err := LayoutSplit(ctx, layout, center, SplitSpec{Horizontal: true, Before: true, Full: true, Size: "30"}, "sleep 600")
	if err != nil {
		t.Fatal(err)
	}
	right, err := LayoutSplit(ctx, layout, center, SplitSpec{Horizontal: true, Size: "40"}, "sleep 600")
	if err != nil {
		t.Fatal(err)
	}
	LayoutSetRole(ctx, layout, center, "center")
	LayoutSetRole(ctx, layout, side, "sidebar")
	LayoutSetRole(ctx, layout, right, "panel:decisions")
	roles := func() map[string]LayoutPane {
		panes, err := LayoutPanes(ctx, layout)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]LayoutPane{}
		for _, p := range panes {
			m[p.Role] = p
		}
		return m
	}
	m := roles()
	if m["sidebar"].Width != 30 || m["panel:decisions"].Width != 40 || m["center"].Width != 160-30-40-2 {
		t.Fatalf("宽度不对：%+v", m)
	}
	if err := LayoutPark(ctx, layout, right, "parked-right"); err != nil {
		t.Fatal(err)
	}
	m = roles()
	if m["panel:decisions"].Window != "parked-right" || m["center"].Width != 160-30-1 {
		t.Fatalf("停放后右栏应在后台窗口、中间变宽：%+v", m)
	}
	if err := LayoutUnpark(ctx, layout, right, center, SplitSpec{Horizontal: true, Size: "40"}); err != nil {
		t.Fatal(err)
	}
	m = roles()
	if m["panel:decisions"].Window != "main" || m["panel:decisions"].Width != 40 {
		t.Fatalf("挪回后应回到主窗口：%+v", m)
	}
	if m["panel:decisions"].PID != roles()["panel:decisions"].PID {
		t.Fatal("挪动不应重启面板程序")
	}
	if err := LayoutSelect(ctx, layout, center); err != nil || !roles()["center"].Active {
		t.Fatalf("选中中间窗格：%v", err)
	}
	if _, err := LayoutPanes(ctx, filepath.Join(filepath.Dir(layout), "none")); err != ErrNoServer {
		t.Fatalf("没有 server 时应是 ErrNoServer：%v", err)
	}
}
