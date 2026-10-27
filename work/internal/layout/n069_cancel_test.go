package layout

import (
	"context"
	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"testing"
)

// Mux models pane state; cancellation semantics match TmuxMux.c and Detached.
// Cancellation immediately after a successful Park/Split is a legal production interleaving.
type n069CancelMux struct {
	Mux
	ctx              context.Context
	cancel           context.CancelFunc
	panes            *[]agentdeck.LayoutPane
	cancelAfterPark  string
	cancelAfterSplit bool
	cancelAfterPanes int // 第几次 Panes 之后取消（0 不取消）
	panesCalls       int
}

func (m *n069CancelMux) Panes() ([]agentdeck.LayoutPane, error) {
	if e := m.ctx.Err(); e != nil {
		return nil, e
	}
	m.panesCalls++
	if m.cancelAfterPanes > 0 && m.panesCalls == m.cancelAfterPanes {
		defer m.cancel()
	}
	return append([]agentdeck.LayoutPane(nil), (*m.panes)...), nil
}
func (m *n069CancelMux) Park(id, w string) error {
	if e := m.ctx.Err(); e != nil {
		return e
	}
	for i := range *m.panes {
		if (*m.panes)[i].ID == id {
			(*m.panes)[i].Window = w
		}
	}
	if id == m.cancelAfterPark {
		m.cancel()
	}
	return nil
}
func (m *n069CancelMux) Unpark(id, target string, s agentdeck.SplitSpec) error {
	if e := m.ctx.Err(); e != nil {
		return e
	}
	for i := range *m.panes {
		if (*m.panes)[i].ID == id {
			(*m.panes)[i].Window = MainWindow
		}
	}
	return nil
}
func (m *n069CancelMux) Remove(id string) error {
	if e := m.ctx.Err(); e != nil {
		return e
	}
	for i := range *m.panes {
		if (*m.panes)[i].ID == id {
			*m.panes = append((*m.panes)[:i], (*m.panes)[i+1:]...)
			break
		}
	}
	return nil
}
func (m *n069CancelMux) Detached() Mux                     { v := *m; v.ctx = context.WithoutCancel(m.ctx); return &v }
func (m *n069CancelMux) Select(string) error               { return m.ctx.Err() }
func (m *n069CancelMux) ResizeWidth(string, string) error  { return m.ctx.Err() }
func (m *n069CancelMux) ResizeHeight(string, string) error { return m.ctx.Err() }
func (m *n069CancelMux) Split(string, agentdeck.SplitSpec, string) (string, error) {
	if e := m.ctx.Err(); e != nil {
		return "", e
	}
	*m.panes = append(*m.panes, agentdeck.LayoutPane{ID: "%5", Window: MainWindow})
	if m.cancelAfterSplit {
		m.cancel()
	}
	return "%5", nil
}
func (m *n069CancelMux) SetRole(id, role string) error {
	if e := m.ctx.Err(); e != nil {
		return e
	}
	for i := range *m.panes {
		if (*m.panes)[i].ID == id {
			(*m.panes)[i].Role = role
		}
	}
	return nil
}

func TestN069HiddenRegionCancelRestoresStructuralPane(t *testing.T) {
	for _, region := range []string{"right", "bottom"} {
		t.Run(region, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			parkedName, structID, structRole := "quota", "%2", RoleTabs
			if region == "bottom" {
				parkedName, structID, structRole = "notifications", "%3", RoleSidebar
			}
			ps := []agentdeck.LayoutPane{{ID: "%1", Role: RoleCenter, Window: MainWindow, Width: 100, Height: 30}, {ID: "%2", Role: RoleTabs, Window: MainWindow, Width: 100, Height: 1}, {ID: "%3", Role: RoleSidebar, Window: MainWindow, Width: 32, Height: 40}, {ID: "%4", Role: "panel:" + region + ":" + parkedName, Window: "parked-" + region, Width: 40, Height: 10}}
			m := &n069CancelMux{ctx: ctx, cancel: cancel, panes: &ps, cancelAfterPark: structID}
			// 改后的设计（步骤边界）：取消发生在这一步里，这一步做完（含恢复暂停的栏），所以整次调用成功；原反例要求报错只因旧实现被取消打断
			_, err := ApplyPanels(m, Spec{}, []deckstore.PanelDef{{Name: parkedName, Region: region, Command: "sleep 1", On: true}, {Name: "new", Region: region, Command: "sleep 1", On: true}})
			if err != nil {
				t.Fatalf("这一步应做完：%v", err)
			}
			for _, p := range ps {
				if p.ID == structID {
					t.Logf("region=%s err=%v structural=%s window=%s", region, err, structRole, p.Window)
					if p.Window != MainWindow {
						t.Fatalf("cancelled hidden-region attach left %s parked", structRole)
					}
				}
			}
		})
	}
}

func TestN069CancelAfterSplitDoesNotLeaveUntaggedPane(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ps := []agentdeck.LayoutPane{{ID: "%1", Role: RoleCenter, Window: MainWindow, Width: 100, Height: 30}, {ID: "%4", Role: "panel:right:quota", Window: MainWindow, Width: 40, Height: 30}}
	m := &n069CancelMux{ctx: ctx, cancel: cancel, panes: &ps, cancelAfterSplit: true}
	// 同上：Split 之后取消，这一步里的 SetRole 用不随取消的 Mux 做完，窗格有角色
	_, err := ApplyPanels(m, Spec{}, []deckstore.PanelDef{{Name: "progress", Region: "right", Command: "sleep 1", On: true}})
	if err != nil {
		t.Fatalf("这一步应做完：%v", err)
	}
	for _, p := range ps {
		if p.ID == "%5" && p.Role == "" {
			t.Logf("err=%v new pane=%+v", err, p)
			t.Fatal("cancelled attach leaves an untagged pane which later ApplyPanels cannot identify")
		}
		if p.ID == "%5" && p.Role != "panel:right:progress" {
			t.Fatalf("新窗格应打上角色：%+v", p)
		}
	}
}

// 步骤边界：取消发生在第一块面板那一步之内：这一步做完（窗格建好、打上角色、停放的标签条恢复），第二块面板的步骤不再开始。
func TestN069CancelStopsAtTheNextStepBoundary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ps := []agentdeck.LayoutPane{{ID: "%1", Role: RoleCenter, Window: MainWindow, Width: 100, Height: 30}, {ID: "%4", Role: "panel:right:quota", Window: MainWindow, Width: 40, Height: 30}}
	m := &n069CancelMux{ctx: ctx, cancel: cancel, panes: &ps, cancelAfterSplit: true}
	res, err := ApplyPanels(m, Spec{}, []deckstore.PanelDef{
		{Name: "quota", Region: "right", Command: "sleep 1", On: true},
		{Name: "progress", Region: "right", Command: "sleep 1", On: true},
		{Name: "activity", Region: "right", Command: "sleep 1", On: true}})
	if err == nil {
		t.Fatal("取消后应报错")
	}
	tagged := 0
	for _, p := range ps {
		if p.Role == "panel:right:progress" {
			tagged++
		}
		if p.Role == "" && p.ID != "%1" {
			t.Fatalf("不留没有角色的窗格：%+v", p)
		}
		if p.ID == "%6" || p.Role == "panel:right:activity" {
			t.Fatalf("取消之后不该开始新步骤（第三块不该有）：%+v", p)
		}
	}
	if tagged != 1 || len(res.Attached) != 1 || res.Attached[0] != "progress" {
		t.Fatalf("第一块这一步应做完并记下：tagged=%d result=%+v panes=%+v", tagged, res, ps)
	}
}

// 取消发生在拆窗格之前：不开始。
func TestN069CancelBeforeRemoveDoesNotStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ps := []agentdeck.LayoutPane{{ID: "%1", Role: RoleCenter, Window: MainWindow}, {ID: "%4", Role: "panel:right:quota", Window: MainWindow}}
	m := &n069CancelMux{ctx: ctx, cancel: cancel, panes: &ps}
	// 起点就没取消，但 ApplyPanels 第一次 Panes 之后、第一步之前取消：用 cancel 钩在 Panes 上
	m.cancelAfterPanes = 1
	res, err := ApplyPanels(m, Spec{}, []deckstore.PanelDef{{Name: "quota", Region: "right", Command: "sleep 1", On: false}})
	if err == nil || len(res.Removed) != 0 {
		t.Fatalf("取消后不该再拆：%+v %v", res, err)
	}
	for _, p := range ps {
		if p.ID == "%4" {
			return
		}
	}
	t.Fatal("窗格不该被拆")
}

// 打角色连续失败（tmux 出错，不是取消）：撤回刚建的、没有角色的窗格，不留孤儿；Remove 没有被放宽。
type discardMux struct {
	*n069CancelMux
	roleFails   int
	unparkFails int
	unparkSkip  int // 前几次 Unpark 不注入失败
	resizes     int
	discarded   []string
}

func (d *discardMux) SetRole(id, role string) error {
	if d.roleFails > 0 {
		d.roleFails--
		return context.DeadlineExceeded
	}
	return d.n069CancelMux.SetRole(id, role)
}
func (d *discardMux) Detached() Mux { return d }
func (d *discardMux) ResizeHeight(id, h string) error {
	d.resizes++
	return d.n069CancelMux.ResizeHeight(id, h)
}
func (d *discardMux) Unpark(id, target string, s agentdeck.SplitSpec) error {
	if d.unparkSkip > 0 {
		d.unparkSkip--
	} else if d.unparkFails > 0 {
		d.unparkFails--
		return context.DeadlineExceeded
	}
	return d.n069CancelMux.Unpark(id, target, s)
}
func (d *discardMux) Discard(id string) error {
	d.discarded = append(d.discarded, id)
	return d.n069CancelMux.Remove(id)
}

func TestN069SetRoleFailureDiscardsTheFreshUntaggedPane(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ps := []agentdeck.LayoutPane{{ID: "%1", Role: RoleCenter, Window: MainWindow, Width: 100, Height: 30}, {ID: "%4", Role: "panel:right:quota", Window: MainWindow}}
	d := &discardMux{n069CancelMux: &n069CancelMux{ctx: ctx, cancel: cancel, panes: &ps}, roleFails: 2}
	_, err := ApplyPanels(d, Spec{}, []deckstore.PanelDef{{Name: "quota", Region: "right", Command: "x", On: true}, {Name: "progress", Region: "right", Command: "x", On: true}})
	if err == nil || len(d.discarded) != 1 || d.discarded[0] != "%5" {
		t.Fatalf("打角色失败应撤回新窗格：discarded=%v err=%v", d.discarded, err)
	}
	for _, p := range ps {
		if p.Role == "" && p.ID != "%1" {
			t.Fatalf("不该留没有角色的窗格：%+v", p)
		}
	}
	// 只失败一次：重试一次就成，不撤回
	ps2 := []agentdeck.LayoutPane{{ID: "%1", Role: RoleCenter, Window: MainWindow}}
	d2 := &discardMux{n069CancelMux: &n069CancelMux{ctx: ctx, cancel: cancel, panes: &ps2}, roleFails: 1}
	if _, err := ApplyPanels(d2, Spec{}, []deckstore.PanelDef{{Name: "progress", Region: "right", Command: "x", On: true}}); err != nil || len(d2.discarded) != 0 {
		t.Fatalf("重试一次应成功：%v %v", err, d2.discarded)
	}
}

// 步骤开始过但没成功（新窗格被撤回）：窗格数动过，收尾仍要理顺尺寸（以前只在有成功的步骤时才理顺）。
func TestN069NormalizeStillRunsAfterAWithdrawnAttach(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ps := []agentdeck.LayoutPane{{ID: "%1", Role: RoleCenter, Window: MainWindow, Width: 100, Height: 30},
		{ID: "%4", Role: "panel:right:quota", Window: MainWindow, Width: 40, Height: 10},
		{ID: "%6", Role: "panel:right:activity", Window: MainWindow, Width: 40, Height: 20}}
	d := &discardMux{n069CancelMux: &n069CancelMux{ctx: ctx, cancel: cancel, panes: &ps}, roleFails: 2}
	_, err := ApplyPanels(d, Spec{}, []deckstore.PanelDef{{Name: "quota", Region: "right", Command: "x", On: true},
		{Name: "activity", Region: "right", Command: "x", On: true}, {Name: "progress", Region: "right", Command: "x", On: true}})
	if err == nil || len(d.discarded) != 1 {
		t.Fatalf("应撤回一个新窗格：%v %v", d.discarded, err)
	}
	if d.resizes == 0 {
		t.Fatal("新窗格被撤回后也要把右栏尺寸理顺")
	}
}

// 停放的标签条接回失败一次：再试一次就成，不让它一直藏着。
func TestN069RestoreOfAParkedStructurePaneIsRetried(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ps := []agentdeck.LayoutPane{{ID: "%1", Role: RoleCenter, Window: MainWindow, Width: 100, Height: 30}, {ID: "%2", Role: RoleTabs, Window: MainWindow, Width: 100, Height: 1},
		{ID: "%5", Role: RoleSidebar, Window: MainWindow, Width: 32, Height: 40}}
	d := &discardMux{n069CancelMux: &n069CancelMux{ctx: ctx, cancel: cancel, panes: &ps}, unparkFails: 1}
	// 右栏里一个窗格都没有：新开右栏要先停放标签条、开好再接回；接回的第一次失败
	_, err := ApplyPanels(d, Spec{}, []deckstore.PanelDef{{Name: "new", Region: "right", Command: "x", On: true}})
	for _, p := range ps {
		if p.ID == "%2" && p.Window != MainWindow {
			t.Fatalf("标签条应已接回：err=%v %+v", err, p)
		}
	}
	if err != nil || d.unparkFails != 0 {
		t.Fatalf("接回失败一次后应重试成功：err=%v 剩余失败次数 %d", err, d.unparkFails)
	}
}

// 对抗自查（步骤边界）：隐藏的右栏挂新面板走 Toggle，Toggle 接回标签条那一步失败一次也要重试，不能让标签条留在停放窗口。
func TestN069ToggleRestoreOfTabsIsRetried(t *testing.T) {
	for _, region := range []string{"right", "bottom"} {
		t.Run(region, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			parkedName, structID, structRole := "quota", "%2", RoleTabs
			if region == "bottom" {
				parkedName, structID, structRole = "notifications", "%3", RoleSidebar
			}
			ps := []agentdeck.LayoutPane{{ID: "%1", Role: RoleCenter, Window: MainWindow, Width: 100, Height: 30}, {ID: "%2", Role: RoleTabs, Window: MainWindow, Width: 100, Height: 1}, {ID: "%3", Role: RoleSidebar, Window: MainWindow, Width: 32, Height: 40}, {ID: "%4", Role: "panel:" + region + ":" + parkedName, Window: "parked-" + region, Width: 40, Height: 10}}
			// 第 1 次 Unpark 是挂回停放的面板，第 2 次是接回标签条/侧栏（注入一次失败）；重试是第 3 次
			d := &discardMux{n069CancelMux: &n069CancelMux{ctx: ctx, cancel: cancel, panes: &ps}, unparkSkip: 1, unparkFails: 1}
			_, err := ApplyPanels(d, Spec{}, []deckstore.PanelDef{{Name: parkedName, Region: region, Command: "x", On: true}, {Name: "new", Region: region, Command: "x", On: true}})
			for _, p := range ps {
				if p.ID == structID && p.Window != MainWindow {
					t.Fatalf("%s 接回失败一次后没有重试，留在停放窗口：err=%v %+v", structRole, err, p)
				}
			}
			if d.unparkFails != 0 {
				t.Fatalf("注入的失败没有用上：%d", d.unparkFails)
			}
		})
	}
}

// Toggle 本身（F5～F7 与 attachPanel 都走它）：ctx 在停放标签条之后取消，接回用不随取消的 Mux，标签条不留在停放窗口。
func TestN069ToggleRestoreIgnoresCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ps := []agentdeck.LayoutPane{{ID: "%1", Role: RoleCenter, Window: MainWindow, Width: 100, Height: 30}, {ID: "%2", Role: RoleTabs, Window: MainWindow, Width: 100, Height: 1}, {ID: "%4", Role: "panel:right:quota", Window: "parked-right", Width: 40, Height: 10}}
	m := &n069CancelMux{ctx: ctx, cancel: cancel, panes: &ps, cancelAfterPark: "%2"}
	err := Toggle(m, Spec{}, "right")
	if err == nil {
		t.Fatal("取消后挂回面板那一步应报错")
	}
	for _, p := range ps {
		if p.ID == "%2" && p.Window != MainWindow {
			t.Fatalf("取消后标签条留在停放窗口：%+v", p)
		}
	}
}
