package layout

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// ApplyResult 是一次 ApplyPanels 做了什么（面板名）。
type ApplyResult struct {
	Attached []string
	Removed  []string
}

// panelOf 从窗格角色里取出栏与面板名（panel:right:quota → right、quota）；不是面板返回 ok=false。
func panelOf(role string) (region, name string, ok bool) {
	rest, found := strings.CutPrefix(role, panelPrefix)
	if !found {
		return "", "", false
	}
	region, name, found = strings.Cut(rest, ":")
	if !found || name == "" || (region != "right" && region != "bottom") {
		return "", "", false
	}
	return region, name, true
}

// ApplyPanels 让正在运行的布局与面板注册表 defs 一致（n069：F9 设置里挂上、拔掉面板，立即作用，不用重建布局）：
//   - 注册表里 on 的面板，布局里没有窗格：在它的栏里新开一个，排在这一栏最下面（或最右面）；
//   - 注册表里 off 的面板，布局里有窗格（含被 F5/F6/F7 停放的）：拆掉，面板程序随之结束；
//   - 其余不动（程序不重启）。注册表里没有的窗格也不碰（owner 手改过 panels.yaml 的残留，不替他决定）。
//
// 这一栏被 F5～F7 整栏隐藏着时，挂上新面板会把这一栏显示出来（owner 主动挂上，就是想看到它）。
// 调用方要持布局锁（Lock）：与 deck layout toggle、normalize 串行。出错时已做的部分保留，返回已做的与错误。
//
// **步骤边界（Codex 复审 2 的 R2-2，owner 选根治方案）**：m 的 context（deckd 的生命周期与总 deadline）只在**步骤之间**检查：
// 一个步骤（拆一个窗格；新开一个窗格含打角色，含隐藏栏的显示与暂停窗格的恢复）一旦开始，就用不随 ctx 取消的 Mux 做完，
// 每条命令各有自己的 5 秒上限，所以步骤是有界的、也不会做到一半被取消而留下暂停着的标签条或没有角色的窗格。
// ctx 取消或到期后不再开始新的步骤，已经做完的步骤保留，最后把尺寸理顺（也用不随取消的 Mux）。
func ApplyPanels(m Mux, s Spec, defs []deckstore.PanelDef) (ApplyResult, error) {
	s = s.withDefaults()
	var res ApplyResult
	panes, err := m.Panes()
	if err != nil {
		return res, err
	}
	s = s.fitTo(panes)
	have := map[string][]agentdeck.LayoutPane{}
	for _, p := range panes {
		if _, name, ok := panelOf(p.Role); ok {
			have[name] = append(have[name], p)
		}
	}
	var errs []error
	stopped, touched := false, false
	// step 做一个步骤：先在步骤边界看 m 的 context（取消了就不开始，之后的步骤也都不再开始），再用不随取消的 Mux 把它做完。
	step := func(f func(d Mux) error) error {
		if stopped {
			return nil
		}
		if err := stepBoundary(m); err != nil {
			stopped = true
			errs = append(errs, err)
			return err
		}
		touched = true
		d, done := stepMux(m)
		defer done()
		return f(d)
	}
	remove := func(name string, p agentdeck.LayoutPane) bool {
		err := step(func(d Mux) error { return d.Remove(p.ID) })
		if err != nil {
			if !stopped {
				errs = append(errs, i18n.E("err.layout.removepanel", name, err))
			}
			return false
		}
		return !stopped
	}
	// 先拆：拆掉的空间让给后来新开的窗格
	for _, d := range defs {
		if d.On {
			continue
		}
		for _, p := range have[d.Name] {
			if remove(d.Name, p) {
				res.Removed = append(res.Removed, d.Name)
			}
		}
	}
	for _, d := range defs {
		if !d.On || stopped {
			continue
		}
		// 注册表里这块面板换了栏（手改 panels.yaml）：旧栏里的窗格拆掉，下面按新栏重新挂
		var inPlace []agentdeck.LayoutPane
		for _, p := range have[d.Name] {
			if region, _, _ := panelOf(p.Role); region != d.Region {
				if remove(d.Name, p) {
					res.Removed = append(res.Removed, d.Name)
				} else {
					inPlace = append(inPlace, p) // 没拆掉就当它还在，免得挂出第二个
				}
				continue
			}
			inPlace = append(inPlace, p)
		}
		have[d.Name] = inPlace
		if dup := have[d.Name]; len(dup) > 1 {
			// 同名面板有多个窗格（上一次中途出错留下的）：留编号最小的一个，其余拆掉
			sort.Slice(dup, func(i, j int) bool { return paneNum(dup[i].ID) < paneNum(dup[j].ID) })
			for _, p := range dup[1:] {
				remove(d.Name, p)
			}
		}
		if stopped || len(have[d.Name]) > 0 {
			continue
		}
		err := step(func(dm Mux) error { return attachPanel(dm, s, d) })
		switch {
		case stopped:
		case err != nil:
			errs = append(errs, i18n.E("err.layout.attachpanel", d.Name, err))
		default:
			res.Attached = append(res.Attached, d.Name)
		}
	}
	if touched {
		// 收尾理顺尺寸、交还焦点：不随取消（ctx 到期后这里仍要把已经做完的步骤理顺，不然右栏高度、宽度是乱的）。
		// 只要有步骤开始过就理顺（不只看成功的：新开的窗格后来被撤回，也动过尺寸）
		dm := detached(m)
		if err := Normalize(dm, s); err != nil {
			errs = append(errs, err)
		}
		if center := centerOf(dm); center != "" {
			// 键盘焦点交还中间（拆掉的窗格若正是焦点所在，tmux 会把焦点给邻居）
			if err := dm.Select(center); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return res, errors.Join(errs...)
}

// stepLimit 是一个结构步骤（含它里面暂停与恢复的窗格）的总时限：每条 tmux 命令另有 5 秒上限，正常的步骤是毫秒级，
// 这个时限只防 tmux 卡住时一步拖得没完。恢复暂停的窗格在步骤里再用不随取消的 Mux（detached），不受这个时限截断。
const stepLimit = 30 * time.Second

// stepMux 给一个步骤配 Mux：不随 m 的 ctx 取消，另有自己的时限（stepLimit）；别的 Mux 实现退回 detached。
func stepMux(m Mux) (Mux, func()) {
	if s, ok := m.(interface {
		Step(time.Duration) (Mux, func())
	}); ok {
		return s.Step(stepLimit)
	}
	return detached(m), func() {}
}

// stepBoundary 在步骤边界看 Mux 的 context 有没有取消或到期（TmuxMux 直接看；别的 Mux 用它的 Panes 是否出错代替）。
func stepBoundary(m Mux) error {
	if c, ok := m.(interface{ Err() error }); ok {
		return c.Err()
	}
	_, err := m.Panes()
	return err
}

func centerOf(m Mux) string {
	panes, err := m.Panes()
	if err != nil {
		return ""
	}
	for _, p := range panes {
		if p.Role == RoleCenter {
			return p.ID
		}
	}
	return ""
}

// attachPanel 在布局里新开一个面板窗格。与 Build 同样的切法；布局已经拼好，右栏的第一块要绕开标签条，下栏的第一块要绕开侧栏
// （与 Toggle 把整栏挪回来时的做法一样：先停放，开好再接回）。
func attachPanel(m Mux, s Spec, d deckstore.PanelDef) (err error) {
	region := d.Region
	panes, err := m.Panes()
	if err != nil {
		return err
	}
	center, tabs, sidebar := "", "", ""
	var shown, parked []agentdeck.LayoutPane
	for _, p := range panes {
		switch {
		case p.Role == RoleCenter:
			center = p.ID
		case p.Role == RoleTabs && p.Window == MainWindow:
			tabs = p.ID
		case p.Role == RoleSidebar && p.Window == MainWindow:
			sidebar = p.ID
		}
		if regionOf(p.Role) != region {
			continue
		}
		if p.Window == MainWindow {
			shown = append(shown, p)
		} else {
			parked = append(parked, p)
		}
	}
	if center == "" {
		return i18n.E("err.layout.nocenter")
	}
	// 这一栏被 F5～F7 整栏隐藏着：owner 主动挂上一块面板，就是想看到它——先把隐藏的整栏显示出来，再按平常的办法挂上
	// （owner 选的；以前是新面板也停放着，再按键才一起回来）
	if len(shown) == 0 && len(parked) > 0 {
		if err := Toggle(m, s, region); err != nil {
			return err
		}
		return attachPanel(m, s, d)
	}
	sort.Slice(shown, func(i, j int) bool { return paneNum(shown[i].ID) < paneNum(shown[j].ID) })
	cmd := s.panelCmd(d)
	var id string
	switch {
	case region == "right" && len(shown) > 0:
		id, err = m.Split(shown[len(shown)-1].ID, agentdeck.SplitSpec{Size: s.rightStack(len(shown) + 1)}, cmd)
	case region == "right":
		if tabs != "" {
			if err := m.Park(tabs, "parked-tabs"); err != nil {
				return err
			}
			defer func() {
				err = errors.Join(err, restoreCoded(detached(m), s, center, true, tabs, agentdeck.SplitSpec{Before: true, Size: "1"}))
			}()
		}
		id, err = m.Split(center, agentdeck.SplitSpec{Horizontal: true, Size: itoa(s.RightWidth)}, cmd)
	case region == "bottom" && len(shown) > 0:
		id, err = m.Split(shown[len(shown)-1].ID, agentdeck.SplitSpec{Horizontal: true, Size: "50%"}, cmd)
	case region == "bottom":
		if sidebar != "" {
			if err := m.Park(sidebar, "parked-left"); err != nil {
				return err
			}
			defer func() {
				err = errors.Join(err, restoreCoded(detached(m), s, center, false, sidebar,
					agentdeck.SplitSpec{Horizontal: true, Before: true, Full: true, Size: itoa(s.LeftWidth)}))
			}()
		}
		id, err = m.Split(center, agentdeck.SplitSpec{Full: true, Size: itoa(s.BottomHeight)}, cmd)
	default:
		return i18n.E("err.layout.region", region)
	}
	if err != nil {
		return err
	}
	role := panelPrefix + region + ":" + d.Name
	if err := m.SetRole(id, role); err != nil {
		if err2 := m.SetRole(id, role); err2 != nil { // 再试一次；还不行就撤回这个刚建的、还没有角色的窗格，不留谁也认不出的孤儿
			if dm, ok := m.(interface{ Discard(pane string) error }); ok {
				if derr := dm.Discard(id); derr != nil {
					return errors.Join(err2, derr)
				}
			}
			return err2
		}
	}
	return nil
}

func itoa(n int) string { return strconv.Itoa(n) }

// restoreCoded 同 restore，但出错是带码的（经 deckd 回给界面，不能是本地语言的整句）。
func restoreCoded(m Mux, s Spec, center string, isTabs bool, pane string, spec agentdeck.SplitSpec) error {
	err := m.Unpark(pane, center, spec)
	if err != nil {
		err = m.Unpark(pane, center, spec) // 再试一次：这一步失败，标签条或侧栏就一直停放着看不见
	}
	if err != nil {
		if isTabs {
			return i18n.E("err.layout.restore.tabs", err)
		}
		return i18n.E("err.layout.restore.side", err)
	}
	if err := Normalize(m, s); err != nil {
		return err
	}
	return m.Select(center)
}

// detached 返回不随 ctx 取消的 Mux（真 tmux 有，测试里的别的实现原样）：收尾接回暂停的窗格用。
func detached(m Mux) Mux {
	if d, ok := m.(interface{ Detached() Mux }); ok {
		return d.Detached()
	}
	return m
}
