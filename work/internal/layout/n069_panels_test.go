package layout

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// n069：正在运行的布局上挂上、拔掉面板（真 tmux，假 deck 只会睡觉）。

func defsWith(on map[string]bool) []deckstore.PanelDef {
	ds := deckstore.DefaultPanels()
	for i := range ds {
		if v, ok := on[ds[i].Name]; ok {
			ds[i].On = v
		}
	}
	return ds
}

func mainPanels(t *testing.T, m Mux) map[string]agentdeck.LayoutPane {
	t.Helper()
	out := map[string]agentdeck.LayoutPane{}
	ps, _ := m.Panes()
	for _, p := range ps {
		if _, name, ok := panelOf(p.Role); ok {
			if _, dup := out[name]; dup {
				t.Fatalf("面板 %s 有两个窗格", name)
			}
			out[name] = p
		}
	}
	return out
}

func checkFrame(t *testing.T, m Mux, s Spec, what string) {
	t.Helper()
	r := byRole(t, m)
	if r[RoleSidebar].Height != s.Height || r[RoleSidebar].Width != 32 {
		t.Fatalf("%s：侧栏应整高、宽 32：%+v", what, r[RoleSidebar])
	}
	if r[RoleTabs].Height != 1 || r[RoleTabs].Width != r[RoleCenter].Width {
		t.Fatalf("%s：标签条应 1 行、与中间同宽：%+v / %+v", what, r[RoleTabs], r[RoleCenter])
	}
	if !r[RoleCenter].Active {
		t.Fatalf("%s：焦点应在中间", what)
	}
}

func TestApplyPanelsAttachesAndRemovesLive(t *testing.T) {
	m, s, _ := realLayout(t)
	before := mainPanels(t, m)
	centerPID := byRole(t, m)[RoleCenter].PID

	// 挂上进度（右栏第三块）：已有的面板程序不重启
	res, err := ApplyPanels(m, s, defsWith(map[string]bool{"progress": true}))
	if err != nil || len(res.Attached) != 1 || res.Attached[0] != "progress" || len(res.Removed) != 0 {
		t.Fatalf("挂上进度：%+v %v", res, err)
	}
	now := mainPanels(t, m)
	p, ok := now["progress"]
	if !ok || p.Window != MainWindow || p.Width != 40 {
		t.Fatalf("进度应在主窗口右栏、宽 40：%+v", p)
	}
	for _, n := range []string{"decisions", "quota", "notifications"} {
		if now[n].PID != before[n].PID {
			t.Fatalf("挂上别的面板不该重启 %s", n)
		}
	}
	if byRole(t, m)[RoleCenter].PID != centerPID {
		t.Fatal("不该动中间")
	}
	checkFrame(t, m, s, "挂上进度")

	// 幂等：再按同一份注册表应用，什么都不做
	res, err = ApplyPanels(m, s, defsWith(map[string]bool{"progress": true}))
	if err != nil || len(res.Attached)+len(res.Removed) != 0 {
		t.Fatalf("幂等：%+v %v", res, err)
	}

	// 拔掉额度（含窗格消失、程序结束）
	quotaPID := now["quota"].PID
	res, err = ApplyPanels(m, s, defsWith(map[string]bool{"progress": true, "quota": false}))
	if err != nil || len(res.Removed) != 1 || res.Removed[0] != "quota" {
		t.Fatalf("拔掉额度：%+v %v", res, err)
	}
	if _, still := mainPanels(t, m)["quota"]; still {
		t.Fatal("额度窗格应已拆掉")
	}
	if exec.Command("kill", "-0", itoa(quotaPID)).Run() == nil {
		// 进程可能还在收尾，稍等再查
		waitGone(t, quotaPID)
	}
	checkFrame(t, m, s, "拔掉额度")
}

func TestApplyPanelsRemoveEveryRightPanelThenAttachAgain(t *testing.T) {
	m, s, _ := realLayout(t)
	s = s.withDefaults()
	was := mainPanels(t, m)
	column := was["decisions"].Height + was["quota"].Height + 1 // 两块叠放：一条边框占一行
	if _, err := ApplyPanels(m, s, defsWith(map[string]bool{"decisions": false, "quota": false})); err != nil {
		t.Fatal(err)
	}
	r := byRole(t, m)
	for role := range r {
		if regionOf(role) == "right" {
			t.Fatalf("右栏应空：%s", role)
		}
	}
	if r[RoleCenter].Width <= 100 {
		t.Fatalf("右栏拔空后中间应变宽：%+v", r[RoleCenter])
	}
	checkFrame(t, m, s, "右栏拔空")
	// 再挂回一块：第一块要绕开标签条，标签条仍是 1 行、与中间同宽，右栏与「标签条+中间」同高
	if _, err := ApplyPanels(m, s, defsWith(map[string]bool{"decisions": false, "quota": true})); err != nil {
		t.Fatal(err)
	}
	q := mainPanels(t, m)["quota"]
	if q.Width != 40 || q.Height != column {
		t.Fatalf("右栏第一块应宽 40、与原来右栏同高 %d（绕开标签条）：%+v", column, q)
	}
	checkFrame(t, m, s, "右栏重新挂上")
}

func TestApplyPanelsBottomRegion(t *testing.T) {
	m, s, _ := realLayout(t)
	s = s.withDefaults()
	if _, err := ApplyPanels(m, s, defsWith(map[string]bool{"notifications": false})); err != nil {
		t.Fatal(err)
	}
	if _, ok := mainPanels(t, m)["notifications"]; ok {
		t.Fatal("下栏应已拔掉")
	}
	checkFrame(t, m, s, "下栏拔掉")
	if _, err := ApplyPanels(m, s, defsWith(nil)); err != nil {
		t.Fatal(err)
	}
	b := mainPanels(t, m)["notifications"]
	if b.Height != s.BottomHeight || b.Width != s.Width-32-1 {
		t.Fatalf("下栏应在侧栏右边、整宽 %d 行：%+v", s.BottomHeight, b)
	}
	checkFrame(t, m, s, "下栏重新挂上")
}

// 整栏被 F6 隐藏着时挂上新面板：owner 主动挂上就是想看到它，所以隐藏的整栏显示出来（与新面板一起，共 3 块）。
func TestApplyPanelsWhileRegionHidden(t *testing.T) {
	m, s, _ := realLayout(t)
	if err := Toggle(m, s, "right"); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPanels(m, s, defsWith(map[string]bool{"progress": true})); err != nil {
		t.Fatal(err)
	}
	for name, p := range mainPanels(t, m) {
		if regionOf(p.Role) == "right" && p.Window != MainWindow {
			t.Fatalf("挂上之后右栏应显示出来，%s 还停放在 %s", name, p.Window)
		}
	}
	if _, ok := mainPanels(t, m)["progress"]; !ok {
		t.Fatal("新面板应已建好")
	}
	n := 0
	for _, p := range mainPanels(t, m) {
		if regionOf(p.Role) == "right" {
			n++
			if p.Window != MainWindow || p.Width != 40 {
				t.Fatalf("显示后应回到主窗口、宽 40：%+v", p)
			}
		}
	}
	if n != 3 {
		t.Fatalf("右栏应有 3 块面板：%d", n)
	}
	checkFrame(t, m, s, "挂上并显示右栏后")
	// 隐藏着时拔掉一块也行（拔掉不会把隐藏的栏弹出来）
	if err := Toggle(m, s, "right"); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPanels(m, s, defsWith(map[string]bool{"progress": true, "decisions": false})); err != nil {
		t.Fatal(err)
	}
	if _, ok := mainPanels(t, m)["decisions"]; ok {
		t.Fatal("停放着的面板也要拔得掉")
	}
}

// 窗格已经死了（程序退出、窗格消失）而注册表说该挂着：应用时补回来。
func TestApplyPanelsRepairsDeadPane(t *testing.T) {
	m, s, _ := realLayout(t)
	q := mainPanels(t, m)["quota"]
	c := exec.Command("tmux", "-S", m.Socket, "kill-pane", "-t", q.ID)
	c.Env = append(c.Environ(), "TMUX=")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	if _, ok := mainPanels(t, m)["quota"]; ok {
		t.Fatal("前提：窗格应已死")
	}
	res, err := ApplyPanels(m, s, defsWith(nil))
	if err != nil || len(res.Attached) != 1 || res.Attached[0] != "quota" {
		t.Fatalf("应补回额度：%+v %v", res, err)
	}
	checkFrame(t, m, s, "补回死窗格")
}

// 注册表里没有的面板窗格（owner 手改 panels.yaml 留下的）不碰；同名两个窗格留一个。
func TestApplyPanelsLeavesUnknownAndDedupes(t *testing.T) {
	m, s, b := realLayout(t)
	extra, err := m.Split(b.Panels["quota"], agentdeck.SplitSpec{Size: "5"}, s.panelCmd(deckstore.PanelDef{Command: "sleep 600"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetRole(extra, "panel:right:mine"); err != nil {
		t.Fatal(err)
	}
	dup, err := m.Split(b.Panels["decisions"], agentdeck.SplitSpec{Size: "5"}, "sleep 600")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetRole(dup, "panel:right:decisions"); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPanels(m, s, defsWith(nil)); err != nil {
		t.Fatal(err)
	}
	ps, _ := m.Panes()
	n, mine := 0, false
	for _, p := range ps {
		n += btoi(p.Role == "panel:right:decisions")
		mine = mine || p.Role == "panel:right:mine"
	}
	if n != 1 || !mine {
		t.Fatalf("decisions 应只剩 1 个（%d），自己的面板应还在（%v）", n, mine)
	}
}

// Remove 只拆面板：中间、侧栏、标签条不是面板，拆了布局就残了。
func TestRemoveRefusesNonPanelPanes(t *testing.T) {
	m, _, _ := realLayout(t)
	r := byRole(t, m)
	for _, role := range []string{RoleCenter, RoleSidebar, RoleTabs} {
		if err := m.Remove(r[role].ID); err == nil || !strings.Contains(err.Error(), role) {
			t.Fatalf("不该拆 %s：%v", role, err)
		}
	}
	if got := byRole(t, m); len(got) != len(r) {
		t.Fatalf("窗格数变了：%d → %d", len(r), len(got))
	}
}

func TestPanelOf(t *testing.T) {
	for role, want := range map[string][2]string{
		"panel:right:quota":          {"right", "quota"},
		"panel:bottom:notifications": {"bottom", "notifications"},
	} {
		r, n, ok := panelOf(role)
		if !ok || r != want[0] || n != want[1] {
			t.Errorf("%s → %s %s %v", role, r, n, ok)
		}
	}
	for _, role := range []string{"", "center", "sidebar", "panel:", "panel:right:", "panel:left:x", "panel:right"} {
		if _, _, ok := panelOf(role); ok {
			t.Errorf("%q 不是面板", role)
		}
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func waitGone(t *testing.T, pid int) {
	t.Helper()
	for i := 0; i < 50; i++ {
		if exec.Command("kill", "-0", itoa(pid)).Run() != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("进程 %d 在窗格拆掉后仍在运行", pid)
}

// 右栏三块、四块面板：每块都有像样的高度，不被压成一两行（n069 e2e 抓到：以前只调第 2 块起的高度，第一块不动、最后一块吃掉差额，
// 三块以上成了 26/2/1）；显隐一轮后尺寸不变。
func TestRightColumnWithThreeAndFourPanelsStaysEven(t *testing.T) {
	m, s, _ := realLayout(t)
	s = s.withDefaults()
	rightHeights := func() []int {
		var hs []int
		for name, p := range mainPanels(t, m) {
			if regionOf(p.Role) == "right" && p.Window == MainWindow {
				_ = name
				hs = append(hs, p.Height)
			}
		}
		return hs
	}
	check := func(what string, n int) {
		t.Helper()
		hs := rightHeights()
		if len(hs) != n {
			t.Fatalf("%s：右栏应有 %d 块：%v", what, n, hs)
		}
		lo, hi, sum := 1<<30, 0, n-1
		for _, h := range hs {
			lo, hi, sum = min(lo, h), max(hi, h), sum+h
		}
		if lo < 5 || hi-lo > 4 {
			t.Fatalf("%s：高度不均匀或太矮 %v", what, hs)
		}
		if want := s.Height - s.BottomHeight - 1; sum != want+0 && sum != want-1 && sum != want+1 {
			t.Fatalf("%s：右栏总高 %d 不对（应约 %d）：%v", what, sum, want, hs)
		}
	}
	if _, err := ApplyPanels(m, s, defsWith(map[string]bool{"progress": true})); err != nil {
		t.Fatal(err)
	}
	check("三块", 3)
	if _, err := ApplyPanels(m, s, defsWith(map[string]bool{"progress": true, "activity": true})); err != nil {
		t.Fatal(err)
	}
	check("四块", 4)
	for i := 0; i < 2; i++ {
		if err := Toggle(m, s, "right"); err != nil {
			t.Fatal(err)
		}
	}
	check("四块，显隐一轮后", 4)
	if _, err := ApplyPanels(m, s, defsWith(map[string]bool{"progress": true, "activity": true, "quota": false})); err != nil {
		t.Fatal(err)
	}
	check("拔掉一块后", 3)
	checkFrame(t, m, s, "多块")
}

// 窗口很矮：四块面板塞不下也不报错、不崩，每块至少 3 行就停。
func TestNormalizeShortWindowManyPanels(t *testing.T) {
	m, s, _ := realLayout(t)
	s.Height = 14
	if _, err := ApplyPanels(m, s, defsWith(map[string]bool{"progress": true, "activity": true})); err != nil {
		t.Logf("塞不下时允许报错，但不能崩：%v", err)
	}
	if err := Normalize(m, s); err != nil {
		t.Logf("Normalize：%v", err)
	}
}

// 对抗自查 F1：下栏高度被改过（窗口缩放后 tmux 按比例分了空间）再 Normalize，右栏两块仍是约定的高度——
// 右栏高度要用调完下栏之后重新读到的，不是开头读到的。
func TestNormalizeTwoRightPanelsAfterBottomResized(t *testing.T) {
	for _, bottom := range []string{"12", "2"} {
		m, s, _ := realLayout(t)
		s = s.withDefaults()
		r := byRole(t, m)
		want := [2]int{r["panel:right:decisions"].Height, r["panel:right:quota"].Height}
		if err := m.ResizeHeight(r["panel:bottom:notifications"].ID, bottom); err != nil {
			t.Fatal(err)
		}
		if err := Normalize(m, s); err != nil {
			t.Fatal(err)
		}
		r = byRole(t, m)
		got := [2]int{r["panel:right:decisions"].Height, r["panel:right:quota"].Height}
		if got != want {
			t.Fatalf("下栏拉到 %s 行后 Normalize：右栏 %v，应为 %v", bottom, got, want)
		}
		if r["panel:bottom:notifications"].Height != s.BottomHeight {
			t.Fatalf("下栏应复原为 %d：%d", s.BottomHeight, r["panel:bottom:notifications"].Height)
		}
	}
}
