package layout

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

func TestQuoteSurvivesShell(t *testing.T) {
	for _, s := range []string{"/opt/my deck/deck", "it's", "a'b'c", "$(rm -rf x)", ""} {
		out, err := exec.Command("sh", "-c", "printf %s "+Quote(s)).Output()
		if err != nil || string(out) != s {
			t.Fatalf("%q 经 shell 后变成 %q（%v）", s, out, err)
		}
	}
}

// 真 tmux：用一个只会睡觉的假 deck 拼出四区布局，核对几何与角色，再逐栏隐藏、显示。
func realLayout(t *testing.T) (TmuxMux, Spec, Built) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("没有 tmux")
	}
	dir, err := os.MkdirTemp("", "dly")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "deck")
	t.Cleanup(func() {
		c := exec.Command("tmux", "-S", sock, "kill-server")
		c.Env = append(os.Environ(), "TMUX=")
		c.Run()
		os.RemoveAll(dir)
	})
	fake := filepath.Join(dir, "fake deck") // 路径带空格：命令要正确加引号
	os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 600\n"), 0o755)
	s := Spec{Deck: fake, LayoutSocket: sock, AgentSocket: filepath.Join(dir, "agent"), Width: 160, Height: 40, Panels: deckstore.DefaultPanels()}
	conf := filepath.Join(dir, "base.conf")
	os.WriteFile(conf, []byte(BaseConf(s)), 0o600)
	m := TmuxMux{Socket: sock}
	b, err := Build(m, s, conf)
	if err != nil {
		t.Fatal(err)
	}
	return m, s, b
}

func byRole(t *testing.T, m Mux) map[string]agentdeck.LayoutPane {
	t.Helper()
	panes, err := m.Panes()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]agentdeck.LayoutPane{}
	for _, p := range panes {
		out[p.Role] = p
	}
	return out
}

func TestBuildFourRegions(t *testing.T) {
	m, s, b := realLayout(t)
	r := byRole(t, m)
	want := []string{RoleCenter, RoleTabs, RoleSidebar, "panel:right:decisions", "panel:right:quota", "panel:bottom:notifications"}
	if len(r) != len(want) {
		t.Fatalf("窗格角色应恰好是 %v：%v", want, r)
	}
	for _, role := range want {
		if _, ok := r[role]; !ok {
			t.Fatalf("缺 %s", role)
		}
	}
	if _, ok := r["panel:right:progress"]; ok {
		t.Fatal("默认关着的面板不应挂上")
	}
	if r[RoleSidebar].Height != s.Height || r[RoleSidebar].Width != 32 {
		t.Fatalf("侧栏应整高、宽 32：%+v", r[RoleSidebar])
	}
	if r[RoleTabs].Height != 1 || r[RoleTabs].Width != r[RoleCenter].Width {
		t.Fatalf("标签条应 1 行、与中间同宽：%+v / %+v", r[RoleTabs], r[RoleCenter])
	}
	bottom := r["panel:bottom:notifications"]
	if bottom.Height != 6 || bottom.Width != s.Width-32-1 {
		t.Fatalf("下栏应在侧栏右边、整宽 6 行：%+v", bottom)
	}
	if r["panel:right:decisions"].Width != 40 || r["panel:right:quota"].Width != 40 {
		t.Fatalf("右栏两块面板应宽 40 且上下叠放：%+v %+v", r["panel:right:decisions"], r["panel:right:quota"])
	}
	if !r[RoleCenter].Active {
		t.Fatal("建好后键盘焦点应在中间窗格")
	}
	if b.Center != r[RoleCenter].ID || b.Tabs != r[RoleTabs].ID {
		t.Fatal("Built 记的 id 应与实际一致")
	}
}

func TestToggleEachRegionKeepsPrograms(t *testing.T) {
	m, s, _ := realLayout(t)
	before := byRole(t, m)
	for _, region := range []string{"left", "right", "bottom"} {
		if err := Toggle(m, s, region); err != nil {
			t.Fatal(err)
		}
		hidden := byRole(t, m)
		for role, p := range hidden {
			if regionOf(role) == region && p.Window == MainWindow {
				t.Fatalf("隐藏 %s 后 %s 仍在主窗口", region, role)
			}
		}
		if !hidden[RoleCenter].Active {
			t.Fatalf("隐藏 %s 后焦点应回到中间", region)
		}
		if err := Toggle(m, s, region); err != nil {
			t.Fatal(err)
		}
		shown := byRole(t, m)
		for role, p := range shown {
			if p.Width != before[role].Width || p.Height != before[role].Height || p.Window != before[role].Window {
				t.Fatalf("显隐 %s 一轮后 %s 应与原来一致：%+v → %+v", region, role, before[role], p)
			}
			if regionOf(role) != region {
				continue
			}
			if p.Window != MainWindow {
				t.Fatalf("显示 %s 后 %s 应回到主窗口", region, role)
			}
			if p.PID != before[role].PID {
				t.Fatalf("显隐不应重启 %s 的程序", role)
			}
			if p.Width != before[role].Width || p.Height != before[role].Height {
				t.Fatalf("显示 %s 后 %s 尺寸应复原：%dx%d → %dx%d", region, role, before[role].Width, before[role].Height, p.Width, p.Height)
			}
		}
	}
	if err := Toggle(m, s, "top"); err == nil {
		t.Fatal("没有的栏应报错")
	}
}

func TestBindingsConf(t *testing.T) {
	s := Spec{Deck: "/opt/my deck/deck", LayoutSocket: "/tmp/x/deck", AgentSocket: "/tmp/x/default"}
	conf := BindingsConf(s, Built{Center: "%1", Tabs: "%7"})
	for _, want := range []string{
		"bind -n F2 send-keys -t %7 F2",
		"bind -n C-q send-keys -t %7 C-q",
		"bind -n F10 select-pane -t %1",
		`run-shell -b ''\''/opt/my deck/deck'\'' '\''layout'\'' '\''toggle'\'' '\''left'\''`,
	} {
		if !strings.Contains(conf, want) {
			t.Fatalf("按键配置缺 %q：\n%s", want, conf)
		}
	}
	if !strings.Contains(BaseConf(Spec{Graphics: "sixel"}), "terminal-features ',*:sixel'") || strings.Contains(BaseConf(Spec{Graphics: "blocks"}), "terminal-features") ||
		!strings.Contains(BaseConf(Spec{Graphics: "blocks"}), "DECK_GRAPHICS 'blocks'") {
		t.Fatal("只有能画 Sixel 时才告诉 tmux")
	}
}

// 交错显隐几栏后再全部显示，整体回到原样（同一批程序、同样尺寸）。
func TestInterleavedTogglesRestoreTheLayout(t *testing.T) {
	m, s, _ := realLayout(t)
	before := byRole(t, m)
	for _, seq := range [][]string{
		{"left", "bottom", "left", "bottom"},
		{"right", "bottom", "bottom", "right"},
		{"left", "right", "bottom", "right", "left", "bottom"},
	} {
		for _, region := range seq {
			if err := Toggle(m, s, region); err != nil {
				t.Fatalf("%v：%v", seq, err)
			}
		}
		after := byRole(t, m)
		for role, p := range after {
			b := before[role]
			if p.PID != b.PID || p.Width != b.Width || p.Height != b.Height || p.Window != b.Window {
				t.Fatalf("按 %v 显隐后 %s 应复原：%+v → %+v", seq, role, b, p)
			}
		}
	}
}
