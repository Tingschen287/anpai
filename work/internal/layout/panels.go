package layout

import (
	"errors"
	"sort"
	"strconv"
	"strings"

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
// 这一栏被 F6/F7 整栏隐藏着时，新面板建好立刻同样停放，不把隐藏的栏弹出来；再按键显示时一起回来。
// 调用方要持布局锁（Lock）：与 deck layout toggle、normalize 串行。出错时已做的部分保留，返回已做的与错误。
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
	// 先拆：拆掉的空间让给后来新开的窗格
	for _, d := range defs {
		if d.On {
			continue
		}
		for _, p := range have[d.Name] {
			if err := m.Remove(p.ID); err != nil {
				errs = append(errs, i18n.E("err.layout.removepanel", d.Name, err))
				continue
			}
			res.Removed = append(res.Removed, d.Name)
		}
	}
	for _, d := range defs {
		if !d.On {
			continue
		}
		if dup := have[d.Name]; len(dup) > 1 {
			// 同名面板有多个窗格（上一次中途出错留下的）：留编号最小的一个，其余拆掉
			sort.Slice(dup, func(i, j int) bool { return paneNum(dup[i].ID) < paneNum(dup[j].ID) })
			for _, p := range dup[1:] {
				if err := m.Remove(p.ID); err != nil {
					errs = append(errs, i18n.E("err.layout.removepanel", d.Name, err))
				}
			}
		}
		if len(have[d.Name]) > 0 {
			continue
		}
		if err := attachPanel(m, s, d); err != nil {
			errs = append(errs, i18n.E("err.layout.attachpanel", d.Name, err))
			continue
		}
		res.Attached = append(res.Attached, d.Name)
	}
	if len(res.Removed)+len(res.Attached) > 0 {
		if err := Normalize(m, s); err != nil {
			errs = append(errs, err)
		}
		if center := centerOf(m); center != "" {
			// 键盘焦点交还中间（拆掉的窗格若正是焦点所在，tmux 会把焦点给邻居）
			if err := m.Select(center); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return res, errors.Join(errs...)
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
	hidden := len(shown) == 0 && len(parked) > 0
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
				err = errors.Join(err, restoreCoded(m, s, center, true, tabs, agentdeck.SplitSpec{Before: true, Size: "1"}))
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
				err = errors.Join(err, restoreCoded(m, s, center, false, sidebar,
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
	if err := m.SetRole(id, panelPrefix+region+":"+d.Name); err != nil {
		// 没有角色的窗格以后谁也认不出，也拆不了（Remove 只认面板角色）：如实报错，窗格留着，owner 重建布局即清
		return err
	}
	if hidden {
		return m.Park(id, "parked-"+region)
	}
	return nil
}

func itoa(n int) string { return strconv.Itoa(n) }

// restoreCoded 同 restore，但出错是带码的（经 deckd 回给界面，不能是本地语言的整句）。
func restoreCoded(m Mux, s Spec, center string, isTabs bool, pane string, spec agentdeck.SplitSpec) error {
	if err := m.Unpark(pane, center, spec); err != nil {
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
