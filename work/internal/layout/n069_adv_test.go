package layout

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// 对抗测试夹具：可指定窗口大小的真 tmux 布局（socket 在临时目录，只 -S 自己的）。
func advLayout(t *testing.T, w, h int) (TmuxMux, Spec, Built, error) {
	t.Helper()
	dir, err := os.MkdirTemp("", "adv")
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
	fake := filepath.Join(dir, "fake deck")
	os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 600\n"), 0o755)
	s := Spec{Deck: fake, LayoutSocket: sock, AgentSocket: filepath.Join(dir, "agent"), Width: w, Height: h, Panels: deckstore.DefaultPanels()}
	conf := filepath.Join(dir, "base.conf")
	os.WriteFile(conf, []byte(BaseConf(s)), 0o600)
	m := TmuxMux{Socket: sock}
	b, err := Build(m, s, conf)
	return m, s, b, err
}

func tmuxRaw(t *testing.T, sock string, args ...string) string {
	t.Helper()
	c := exec.Command("tmux", append([]string{"-S", sock}, args...)...)
	c.Env = append(os.Environ(), "TMUX=")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Logf("tmux %v: %v %s", args, err, out)
	}
	return string(out)
}

func dump(t *testing.T, m Mux) string {
	ps, _ := m.Panes()
	sort.Slice(ps, func(i, j int) bool { return paneNum(ps[i].ID) < paneNum(ps[j].ID) })
	var b strings.Builder
	for _, p := range ps {
		fmt.Fprintf(&b, " %s[%s|%s %dx%d]", p.ID, p.Role, p.Window, p.Width, p.Height)
	}
	return b.String()
}

// 矮窗口（8～14 行）：Build、挂满右栏、拔光、再挂回，每一步不能报错，也不能让中间/任何主窗口窗格高度 < 1 或消失。
func TestAdvSmallWindowHeights(t *testing.T) {
	for h := 8; h <= 14; h++ {
		h := h
		t.Run(fmt.Sprintf("h%d", h), func(t *testing.T) {
			m, s, _, err := advLayout(t, 120, h)
			if err != nil {
				t.Skipf("Build 本身在 %d 行就失败（既有行为，非本次新增）：%v", h, err)
			}
			s = s.withDefaults()
			step := func(what string, on map[string]bool) {
				_, err := ApplyPanels(m, s, defsWith(on))
				t.Logf("h=%d %s err=%v\n   %s", h, what, err, dump(t, m))
				if err != nil && !strings.Contains(err.Error(), "no space for new pane") {
					t.Errorf("h=%d %s：除「放不下」以外的报错 %v", h, what, err)
				}
				r := byRole(t, m)
				if _, ok := r[RoleCenter]; !ok {
					t.Fatalf("h=%d %s：中间窗格没了", h, what)
				}
				if r[RoleCenter].Height < 1 {
					t.Errorf("h=%d %s：中间只剩 %d 行", h, what, r[RoleCenter].Height)
				}
				for role, p := range r {
					if p.Window == MainWindow && p.Height < 1 {
						t.Errorf("%s 高度 %d", role, p.Height)
					}
				}
			}
			step("挂满右栏", map[string]bool{"progress": true, "activity": true})
			step("拔光右栏", map[string]bool{"decisions": false, "quota": false, "progress": false, "activity": false})
			step("拔下栏", map[string]bool{"decisions": false, "quota": false, "notifications": false})
			step("全挂回", map[string]bool{"progress": true, "activity": true})
		})
	}
}

func rightHeights(t *testing.T, m Mux) []int {
	ps, _ := m.Panes()
	sort.Slice(ps, func(i, j int) bool { return paneNum(ps[i].ID) < paneNum(ps[j].ID) })
	var hs []int
	for _, p := range ps {
		if regionOf(p.Role) == "right" && p.Window == MainWindow {
			hs = append(hs, p.Height)
		}
	}
	return hs
}

// 窗口 14 行、右栏三块：可用 5 行，应 2/2/1 或 1/2/2 之类（差 ≤1）；实际首块被压成 1、第二块 3。
func TestAdvTinyWindowThreePanelsLopsided(t *testing.T) {
	m, s, _, err := advLayout(t, 120, 14)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPanels(m, s, defsWith(map[string]bool{"progress": true})); err != nil {
		t.Fatal(err)
	}
	hs := rightHeights(t, m)
	t.Logf("右栏高度（自上而下）：%v", hs)
	lo, hi := 99, 0
	for _, h := range hs {
		lo, hi = min(lo, h), max(hi, h)
	}
	if hi-lo > 1 {
		t.Errorf("窗口放不下时也应尽量平分，实际 %v", hs)
	}
}

// 同名重复窗格：去重拆掉多余的那个，却不整理尺寸、不把焦点还给中间（只有 Attached/Removed 非空才 Normalize）。
func TestAdvDedupOnlyDoesNotNormalizeOrRefocus(t *testing.T) {
	m, s, b, err := advLayout(t, 160, 40)
	if err != nil {
		t.Fatal(err)
	}
	base := rightHeights(t, m)
	// 在 quota 旁边多切一个同名窗格，并把焦点放到最后一个重复窗格上
	dup, err := m.Split(b.Panels["quota"], splitV("5"), "sleep 600")
	if err != nil {
		t.Fatal(err)
	}
	m.SetRole(dup, "panel:right:quota")
	m.Select(dup)
	t.Logf("去重前：%s", dump(t, m))
	res, err := ApplyPanels(m, s, defsWith(nil))
	t.Logf("res=%+v err=%v\n去重后：%s", res, err, dump(t, m))
	if len(mainPanels(t, m)) == 0 {
		t.Fatal("夹具")
	}
	r := byRole(t, m)
	hs := rightHeights(t, m)
	lo, hi := 99, 0
	for _, h := range hs {
		lo, hi = min(lo, h), max(hi, h)
	}
	if !r[RoleCenter].Active {
		t.Errorf("去重后焦点不在中间：center active=%v", r[RoleCenter].Active)
	}
	_ = lo
	_ = hi
	if fmt.Sprint(hs) != fmt.Sprint(base) {
		t.Errorf("去重后右栏高度没整理回去：原 %v 现 %v；且 res.Removed 为空，界面不会提示拆了东西", base, hs)
	}
}

func splitV(size string) agentdeck.SplitSpec { return agentdeck.SplitSpec{Size: size} }

// killBefore 在 Remove 前先把目标窗格从外面杀掉（模拟：Panes() 之后、Remove 之前，用户/进程把窗格关了）。
type killBefore struct {
	TmuxMux
	t *testing.T
}

func (k killBefore) Remove(pane string) error {
	tmuxRaw(k.t, k.Socket, "kill-pane", "-t", pane)
	return k.TmuxMux.Remove(pane)
}

// 拔面板时窗格恰好已被外部杀死：目标状态（窗格不在）已达成，却返回错误 → deckd 回 applied=false，界面提示「布局没跟上」。
func TestAdvRemoveAlreadyDeadPaneIsNotAnError(t *testing.T) {
	m, s, _, err := advLayout(t, 160, 40)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ApplyPanels(killBefore{m, t}, s, defsWith(map[string]bool{"quota": false}))
	t.Logf("res=%+v err=%v", res, err)
	if _, still := mainPanels(t, m)["quota"]; still {
		t.Fatal("夹具：quota 应已不在")
	}
	if err != nil {
		t.Errorf("窗格已经不在，拔掉应算成功（幂等）：%v", err)
	}
}

// 外部杀掉中间窗格后再挂面板：不崩，报「没有中间窗格」
func TestAdvAttachWhenCenterOrTabsOrSidebarDead(t *testing.T) {
	for _, role := range []string{RoleCenter, RoleTabs, RoleSidebar} {
		role := role
		t.Run(role, func(t *testing.T) {
			m, s, _, err := advLayout(t, 160, 40)
			if err != nil {
				t.Fatal(err)
			}
			tmuxRaw(t, m.Socket, "kill-pane", "-t", byRole(t, m)[role].ID)
			// 把右栏全拔了再挂回，触发 attachPanel 的「第一块」分支（要用到 tabs/center）
			ApplyPanels(m, s, defsWith(map[string]bool{"decisions": false, "quota": false}))
			_, err = ApplyPanels(m, s, defsWith(nil))
			t.Logf("%s 死后：err=%v\n%s", role, err, dump(t, m))
			ps, perr := m.Panes()
			if perr != nil || len(ps) == 0 {
				t.Fatalf("布局 server 不该死：%v", perr)
			}
		})
	}
}

// F5 隐藏侧栏时拔掉、再挂回下栏，然后 F5 显示侧栏：侧栏应整高、下栏不在侧栏下面。
func TestAdvBottomReattachWhileSidebarHidden(t *testing.T) {
	m, s, _, err := advLayout(t, 160, 40)
	if err != nil {
		t.Fatal(err)
	}
	if err := Toggle(m, s, "left"); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPanels(m, s, defsWith(map[string]bool{"notifications": false})); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPanels(m, s, defsWith(nil)); err != nil {
		t.Fatal(err)
	}
	t.Logf("侧栏隐藏中：%s", dump(t, m))
	if err := Toggle(m, s, "left"); err != nil {
		t.Fatal(err)
	}
	t.Logf("侧栏显示后：%s", dump(t, m))
	checkFrame(t, m, s.withDefaults(), "侧栏隐藏时重挂下栏")
	b := mainPanels(t, m)["notifications"]
	if b.Width != s.Width-32-1 {
		t.Errorf("下栏应在侧栏右边：%+v", b)
	}
}

// 下栏被 F7 隐藏、同时右栏被 F6 隐藏；拔掉右栏全部（停放着的）再挂回：右栏会不会自己弹出来？
func TestAdvRemoveAllParkedRightThenAttachPopsOut(t *testing.T) {
	m, s, _, err := advLayout(t, 160, 40)
	if err != nil {
		t.Fatal(err)
	}
	if err := Toggle(m, s, "right"); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPanels(m, s, defsWith(map[string]bool{"decisions": false, "quota": false})); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPanels(m, s, defsWith(nil)); err != nil {
		t.Fatal(err)
	}
	for name, p := range mainPanels(t, m) {
		if regionOf(p.Role) == "right" && p.Window == MainWindow {
			// 设计取舍而非缺陷：「隐藏」只靠停放的窗格表示，拔光后状态丢了，再挂上就显示。记录行为，不判失败
			t.Logf("F6 隐藏右栏后拔光再挂回：%s 在主窗口显示（隐藏状态丢失，可接受）", name)
		}
	}
}

// 自定义面板：同一栏里有两个下栏面板、整栏隐藏后新挂第三个，再显示。
func TestAdvTwoBottomPanelsHiddenThenAttachThird(t *testing.T) {
	m, s, _, err := advLayout(t, 160, 40)
	if err != nil {
		t.Fatal(err)
	}
	defs := append(deckstore.DefaultPanels(),
		deckstore.PanelDef{Name: "b2", Command: "sleep 600", Region: "bottom", On: false},
		deckstore.PanelDef{Name: "b3", Command: "sleep 600", Region: "bottom", On: false})
	set := func(on ...string) []deckstore.PanelDef {
		out := append([]deckstore.PanelDef(nil), defs...)
		for i := range out {
			for _, n := range on {
				if out[i].Name == n {
					out[i].On = true
				}
			}
		}
		return out
	}
	if _, err := ApplyPanels(m, s, set("b2")); err != nil {
		t.Fatal(err)
	}
	if err := Toggle(m, s, "bottom"); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPanels(m, s, set("b2", "b3")); err != nil { // 隐藏着时挂上第三块：整栏显示出来（owner 主动挂上就是想看到它，Codex 审查 1 之后 owner 定的）
		t.Fatal(err)
	}
	t.Log(dump(t, m))
	checkFrame(t, m, s.withDefaults(), "下栏三块")
	n := 0
	for _, p := range mainPanels(t, m) {
		if regionOf(p.Role) == "bottom" && p.Window == MainWindow {
			n++
			if p.Height != 6 {
				t.Errorf("下栏面板高度 %d≠6：%+v", p.Height, p)
			}
		}
	}
	if n != 3 {
		t.Errorf("下栏应 3 块：%d", n)
	}
}

// LayoutRemovePane 攻击：构造的目标串/角色，看能否拆到非面板窗格或搞垮 server。
func TestAdvRemovePaneHostileTargets(t *testing.T) {
	m, _, b, err := advLayout(t, 160, 40)
	if err != nil {
		t.Fatal(err)
	}
	// 把焦点放到一个面板上：空目标、{active} 之类都会解析成「当前窗格」
	m.Select(b.Panels["decisions"])
	before := len(byRole(t, m))
	victims := []string{
		"", " ", "{active}", "{last}", "{top}", "{bottom}", "{left}", "{right}", "main", "deck", "deck:main", "deck:main.0", "=deck:",
		"%0;", "%0", b.Center + ";kill-server;", b.Panels["quota"] + ";", "-t", "-a", "--", "%999", "$0", "@0",
		b.Center + "\n", "'" + b.Center + "'", b.Sidebar + " ; kill-pane -t " + b.Sidebar + " ;",
	}
	for _, v := range victims {
		err := agentdeckRemove(m.Socket, v)
		t.Logf("Remove(%q) → %v", v, err)
		r := byRole(t, m)
		for _, role := range []string{RoleCenter, RoleSidebar, RoleTabs} {
			if _, ok := r[role]; !ok {
				t.Fatalf("目标 %q 拆掉了非面板窗格 %s", v, role)
			}
		}
	}
	after := len(byRole(t, m))
	t.Logf("窗格数 %d → %d", before, after)
	if after < before {
		t.Logf("注意：有面板窗格被空/别名目标拆掉（仍是面板，角色校验按同一目标解析，没绕过）")
	}
}

func agentdeckRemove(sock, pane string) error {
	return agentdeck.LayoutRemovePane(context.Background(), sock, pane)
}

// owner 手改 panels.yaml 把 quota 从 right 改到 bottom：ApplyPanels 只按名字判断「已有窗格」，不会挪栏。
func TestAdvRegionChangedByHandEditIsIgnored(t *testing.T) {
	m, s, _, err := advLayout(t, 160, 40)
	if err != nil {
		t.Fatal(err)
	}
	defs := deckstore.DefaultPanels()
	for i := range defs {
		if defs[i].Name == "quota" {
			defs[i].Region = "bottom"
		}
	}
	if _, err := ApplyPanels(m, s, defs); err != nil {
		t.Fatal(err)
	}
	if p := mainPanels(t, m)["quota"]; regionOf(p.Role) != "bottom" {
		t.Errorf("注册表说 quota 在 bottom，布局里仍在 %s（角色 %s）", regionOf(p.Role), p.Role)
	}
}
