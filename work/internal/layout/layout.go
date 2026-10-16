// Package layout 拼「安排 Anpai」v1 的四区布局（n049，架构稿 §1、§7）。
//
//	┌────────┬──────────────────────────┬──────────┐
//	│        │ 标签条（1 行，deck tabs） │          │
//	│ 侧栏   ├──────────────────────────┤ 右栏面板 │
//	│ deck   │ 中间（deck center：       │ decisions│
//	│ sidebar│  嵌套 client 连 agent-deck）│ quota …  │
//	│        ├──────────────────────────┴──────────┤
//	│        │ 下栏面板（notifications）            │
//	└────────┴─────────────────────────────────────┘
//
// 布局在 deck 自己的 tmux server 上（无前缀键、无状态栏）。每个窗格用 @deck_role 标记角色：
// center、tabs、sidebar、panel:right:<名字>、panel:bottom:<名字>。显隐整栏是把窗格停放到后台窗口（程序不退出），
// 再挪回来。本包只做编排，tmux 操作经 Mux（真实实现在 agentdeck 适配层）。
package layout

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 窗格角色。
const (
	RoleCenter  = "center"
	RoleTabs    = "tabs"
	RoleSidebar = "sidebar"
	panelPrefix = "panel:"
	MainWindow  = "main"
)

// Mux 是本包要用的 tmux 操作（布局 server 上）。
type Mux interface {
	NewSession(conf, session, window, width, height, command string) (string, error)
	Split(target string, spec agentdeck.SplitSpec, command string) (string, error)
	SetRole(pane, role string) error
	Source(path string) error
	Panes() ([]agentdeck.LayoutPane, error)
	Select(pane string) error
	Park(pane, window string) error
	Unpark(pane, target string, spec agentdeck.SplitSpec) error
	ResizeWidth(pane, width string) error
	ResizeHeight(pane, height string) error
}

// Spec 是一次布局的参数。
type Spec struct {
	Deck         string // deck 可执行文件的绝对路径
	LayoutSocket string
	AgentSocket  string
	Session      string // 布局会话名，默认 deck
	Width        int
	Height       int
	LeftWidth    int // 侧栏宽，默认 32
	RightWidth   int // 右栏宽，默认 40
	BottomHeight int // 下栏高，默认 6
	Panels       []deckstore.PanelDef
	Graphics     string // logo 画法：sixel、blocks、none（经布局 server 的环境变量 DECK_GRAPHICS 传给侧栏）
}

func (s Spec) withDefaults() Spec {
	if s.Session == "" {
		s.Session = "deck"
	}
	if s.Width <= 0 {
		s.Width = 200
	}
	if s.Height <= 0 {
		s.Height = 50
	}
	if s.LeftWidth <= 0 {
		s.LeftWidth = 32
	}
	if s.RightWidth <= 0 {
		s.RightWidth = 40
	}
	if s.BottomHeight <= 0 {
		s.BottomHeight = 6
	}
	return s
}

// Quote 把一个参数包成 shell 单引号字面量（tmux 用 sh -c 执行窗格命令）。
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// deckCmd 拼一条 deck 子命令：deck 路径与两个 socket 都按字面量传，不经 shell 展开。
func (s Spec) deckCmd(sub ...string) string {
	parts := []string{Quote(s.Deck)}
	for _, a := range sub {
		parts = append(parts, Quote(a))
	}
	return strings.Join(parts, " ")
}

func (s Spec) sockets() []string {
	return []string{"-layout-socket", s.LayoutSocket, "-agent-socket", s.AgentSocket}
}

// panelCmd 把面板注册表里的命令换成可执行的：以「deck 」开头的换成 deck 的绝对路径，其余（自定义面板）原样。
func (s Spec) panelCmd(p deckstore.PanelDef) string {
	if rest, ok := strings.CutPrefix(p.Command, "deck "); ok {
		return Quote(s.Deck) + " " + rest
	}
	return p.Command
}

// BaseConf 是起 server 时的配置：无前缀键、无状态栏、开鼠标；外层终端能画 Sixel 时告诉 tmux。
func BaseConf(s Spec) string {
	s = s.withDefaults()
	var b strings.Builder
	b.WriteString(i18n.T("layout.conf.base") + "\n")
	for _, line := range []string{
		"set -g prefix None",
		"set -g prefix2 None",
		"set -g status off",
		"set -g mouse on",
		"set -g escape-time 0",
		"set -g focus-events on",
		"set -g remain-on-exit off",
		"set -g default-terminal tmux-256color",
		"set -as terminal-overrides ',*:RGB'",
		"set -g pane-border-lines heavy",
		"set -g pane-active-border-style fg=#b4f546", // 品牌色青柠（adr_031）
		"set -g pane-border-style fg=colour238",
		"set -g window-size latest",
		// 中间嵌套 client 复制时，内层 tmux 用 OSC 52 交给它的终端（也就是这台布局 server）；默认 external 不收窗格里
		// 程序发来的 OSC 52，复制就到不了 Windows 剪贴板（n049 真实 CLI 复验发现）。on：转给外层终端
		"set -g set-clipboard on",
	} {
		b.WriteString(line + "\n")
	}
	if s.Graphics == "sixel" {
		b.WriteString("set -as terminal-features ',*:sixel'\n")
	}
	if s.Graphics != "" {
		b.WriteString("set-environment -g DECK_GRAPHICS " + Quote(s.Graphics) + "\n")
	}
	return b.String()
}

// Built 是建好的布局里各窗格的 id。
type Built struct {
	Center, Tabs, Sidebar string
	Panels                map[string]string // 面板名 → pane id
}

// Build 在一个空 server 上拼出布局（conf 是 BaseConf 写出的文件）。
func Build(m Mux, s Spec, conf string) (Built, error) {
	s = s.withDefaults()
	b := Built{Panels: map[string]string{}}
	var err error
	b.Center, err = m.NewSession(conf, s.Session, MainWindow, strconv.Itoa(s.Width), strconv.Itoa(s.Height),
		s.deckCmd(append([]string{"center"}, s.sockets()...)...))
	if err != nil {
		return b, err
	}
	if err := m.SetRole(b.Center, RoleCenter); err != nil {
		return b, err
	}
	right, bottom := split(s.Panels)
	// 先切下栏（整宽），再切侧栏（整高）：侧栏占满左边，下栏落在中间与右栏下面
	prev := ""
	for i, p := range bottom {
		spec := agentdeck.SplitSpec{Full: true, Size: strconv.Itoa(s.BottomHeight)}
		target := b.Center
		if i > 0 {
			spec = agentdeck.SplitSpec{Horizontal: true, Size: "50%"}
			target = prev
		}
		id, err := m.Split(target, spec, s.panelCmd(p))
		if err != nil {
			return b, err
		}
		if err := m.SetRole(id, panelPrefix+"bottom:"+p.Name); err != nil {
			return b, err
		}
		b.Panels[p.Name], prev = id, id
	}
	b.Sidebar, err = m.Split(b.Center, agentdeck.SplitSpec{Horizontal: true, Before: true, Full: true, Size: strconv.Itoa(s.LeftWidth)},
		s.deckCmd(append([]string{"sidebar"}, s.sockets()...)...))
	if err != nil {
		return b, err
	}
	if err := m.SetRole(b.Sidebar, RoleSidebar); err != nil {
		return b, err
	}
	for i, p := range right {
		spec := agentdeck.SplitSpec{Horizontal: true, Size: strconv.Itoa(s.RightWidth)}
		target := b.Center
		if i > 0 {
			spec = agentdeck.SplitSpec{Size: s.rightStack(len(right))}
			target = prev
		}
		id, err := m.Split(target, spec, s.panelCmd(p))
		if err != nil {
			return b, err
		}
		if err := m.SetRole(id, panelPrefix+"right:"+p.Name); err != nil {
			return b, err
		}
		b.Panels[p.Name], prev = id, id
	}
	b.Tabs, err = m.Split(b.Center, agentdeck.SplitSpec{Before: true, Size: "1"},
		s.deckCmd(append([]string{"tabs"}, s.sockets()...)...))
	if err != nil {
		return b, err
	}
	if err := m.SetRole(b.Tabs, RoleTabs); err != nil {
		return b, err
	}
	if err := Normalize(m, s); err != nil {
		return b, err
	}
	return b, m.Select(b.Center)
}

// Normalize 把各栏调回约定尺寸：侧栏 LeftWidth 列、右栏 RightWidth 列且面板平均分高、下栏 BottomHeight 行、标签条 1 行，
// 其余都给中间。tmux 在停放、挪回、外层改尺寸时按比例重分空间，不复原的话中间会越来越窄（真 tmux 测试抓到）。
func Normalize(m Mux, s Spec) error {
	s = s.withDefaults()
	panes, err := m.Panes()
	if err != nil {
		return err
	}
	s = s.fitTo(panes)
	var right, bottom []agentdeck.LayoutPane
	for _, p := range panes {
		if p.Window != MainWindow {
			continue
		}
		switch {
		case p.Role == RoleSidebar:
			if err := m.ResizeWidth(p.ID, strconv.Itoa(s.LeftWidth)); err != nil {
				return err
			}
		case p.Role == RoleTabs:
			if err := m.ResizeHeight(p.ID, "1"); err != nil {
				return err
			}
		case regionOf(p.Role) == "right":
			right = append(right, p)
		case regionOf(p.Role) == "bottom":
			bottom = append(bottom, p)
		}
	}
	sort.Slice(right, func(i, j int) bool { return paneNum(right[i].ID) < paneNum(right[j].ID) })
	sort.Slice(bottom, func(i, j int) bool { return paneNum(bottom[i].ID) < paneNum(bottom[j].ID) })
	if len(bottom) > 0 {
		if err := m.ResizeHeight(bottom[0].ID, strconv.Itoa(s.BottomHeight)); err != nil {
			return err
		}
	}
	for i, p := range right {
		if i == 0 {
			if err := m.ResizeWidth(p.ID, strconv.Itoa(s.RightWidth)); err != nil {
				return err
			}
			continue
		}
		if err := m.ResizeHeight(p.ID, s.rightStack(len(right))); err != nil {
			return err
		}
	}
	return nil
}

// fitTo 按布局主窗口的实际高度修正 Spec（显隐与复原从命令行调用时，不知道外层终端多大）。
func (s Spec) fitTo(panes []agentdeck.LayoutPane) Spec {
	for _, p := range panes {
		if p.Window == MainWindow && p.WindowHeight > 0 {
			s.Height = p.WindowHeight
			break
		}
	}
	return s
}

// rightStack 是右栏第 2 块起每块面板的高度：右栏可用高度平均分（建布局与挪回用同一个算法，显隐后尺寸不变）。
func (s Spec) rightStack(n int) string {
	return strconv.Itoa(max(3, (s.Height-s.BottomHeight)/max(1, n)))
}

func split(ps []deckstore.PanelDef) (right, bottom []deckstore.PanelDef) {
	for _, p := range ps {
		if !p.On {
			continue
		}
		if p.Region == "bottom" {
			bottom = append(bottom, p)
		} else {
			right = append(right, p)
		}
	}
	return right, bottom
}

// layoutKeyRows 是根表按键和文字键。Keys 用 layoutKeyRows[i].text 取文字，调用处不是字符串字面量。
var layoutKeyRows = []struct{ key, text string }{
	{"F2", "layout.key.f2"},
	{"Ctrl-Q", "layout.key.cq"},
	{"F5", "layout.key.f5"},
	{"F6", "layout.key.f6"},
	{"F7", "layout.key.f7"},
	{"F8", "layout.key.f8"},
	{"F9", "layout.key.f9"},
	{"F10", "layout.key.f10"},
}

// Keys 是布局 server 根表上的按键说明（外层 tmux 截获，不进中间的 CLI）。按当前语言取文字。
func Keys() []struct{ Key, What string } {
	out := make([]struct{ Key, What string }, len(layoutKeyRows))
	for i, r := range layoutKeyRows {
		// 用包级表的下标，不用 r.text。检查器不认局部变量上的字段。
		out[i] = struct{ Key, What string }{r.key, i18n.T(layoutKeyRows[i].text)}
	}
	return out
}

// TextKeys 返回按键说明用到的文字键。
func TextKeys() []string {
	out := make([]string, len(layoutKeyRows))
	for i, r := range layoutKeyRows {
		out[i] = r.text
	}
	return out
}

// BindingsConf 是窗格建好之后的按键绑定。接管键交给标签条程序处理（它在布局窗格里，有 owner 身份；
// run-shell 起的进程不在任何窗格里，deckd 认不出它，所以不能直接调接管）。
func BindingsConf(s Spec, b Built) string {
	s = s.withDefaults()
	toggle := func(region string) string {
		return "run-shell -b " + Quote(s.deckCmd(append([]string{"layout", "toggle", region}, s.sockets()...)...))
	}
	var w strings.Builder
	w.WriteString(i18n.T("layout.conf.keys") + "\n")
	fmt.Fprintf(&w, "bind -n F2 send-keys -t %s F2\n", b.Tabs)
	// Ctrl-Q：放回后台。在布局 server 上截获、交给标签条按隐藏规则处理（架构稿 §7），只读与可写时都有效，
	// 不靠 agent-deck 在它那台 server 上的 C-q 断开绑定、也不必断开重连中间 client（n049 真机演示后改）
	fmt.Fprintf(&w, "bind -n C-q send-keys -t %s C-q\n", b.Tabs)
	fmt.Fprintf(&w, "bind -n F5 %s\n", toggle("left"))
	fmt.Fprintf(&w, "bind -n F6 %s\n", toggle("right"))
	fmt.Fprintf(&w, "bind -n F7 %s\n", toggle("bottom"))
	// F8：立刻转告排队的汇报（§14.4）。在布局 server 的后台窗口里跑，deckd 才认得出是 owner
	fmt.Fprintf(&w, "bind -n F8 new-window -d -n deliver %s\n", Quote(s.deckCmd("reports", "deliver-now")+"; sleep 1"))
	fmt.Fprintf(&w, "bind -n F9 new-window -n settings %s\n", Quote(s.deckCmd(append([]string{"settings"}, s.sockets()...)...)))
	fmt.Fprintf(&w, "bind -n F10 select-pane -t %s\n", b.Center)
	// 外层终端改尺寸后，tmux 按比例重分各栏；复原约定尺寸
	fmt.Fprintf(&w, "set-hook -g client-resized %s\n", Quote("run-shell -b "+Quote(s.deckCmd(append([]string{"layout", "normalize"}, s.sockets()...)...))))
	return w.String()
}

// regionOf 返回一个窗格角色属于哪一栏（left、right、bottom），不属于可显隐的栏返回空串。
func regionOf(role string) string {
	switch {
	case role == RoleSidebar:
		return "left"
	case strings.HasPrefix(role, panelPrefix+"right:"):
		return "right"
	case strings.HasPrefix(role, panelPrefix+"bottom:"):
		return "bottom"
	}
	return ""
}

// Toggle 显示或隐藏一整栏：这一栏在主窗口里就全部停放到后台窗口，否则全部挪回原位。
// 为并回暂时停放的标签条或侧栏，接回失败也如实返回（不报成功留下缺栏的布局，n049 评审 1 S2）。
func Toggle(m Mux, s Spec, region string) (err error) {
	s = s.withDefaults()
	if region != "left" && region != "right" && region != "bottom" {
		return i18n.Errorf("layout.err.region", region)
	}
	panes, err := m.Panes()
	if err != nil {
		return err
	}
	s = s.fitTo(panes)
	center, tabs, sidebar := "", "", ""
	var shown, parked []agentdeck.LayoutPane
	for _, p := range panes {
		if p.Role == RoleCenter {
			center = p.ID
		}
		if p.Role == RoleTabs && p.Window == MainWindow {
			tabs = p.ID
		}
		if p.Role == RoleSidebar && p.Window == MainWindow {
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
		return i18n.Errorf("layout.err.nocenter")
	}
	if len(shown) > 0 {
		for _, p := range shown {
			if err := m.Park(p.ID, "parked-"+region); err != nil {
				return err
			}
		}
		if err := Normalize(m, s); err != nil {
			return err
		}
		return m.Select(center)
	}
	if len(parked) == 0 {
		return i18n.Errorf("layout.err.nopanes", region)
	}
	// 按建窗格的先后（pane id 递增）挪回，保持原来的上下、左右顺序
	sort.Slice(parked, func(i, j int) bool { return paneNum(parked[i].ID) < paneNum(parked[j].ID) })
	// 右栏原本与「标签条 + 中间」这一整列并排。直接并到中间旁边，右栏会比原来矮一截（标签条那一行）。
	// 所以先把标签条停放，右栏并回后再把标签条接回中间上方，还原原来的结构（真 tmux 测试核对尺寸）。
	if region == "right" && tabs != "" {
		if err := m.Park(tabs, "parked-tabs"); err != nil {
			return err
		}
		defer func() {
			err = errors.Join(err, restore(m, s, center, i18n.T("layout.pane.tabs"), tabs, agentdeck.SplitSpec{Before: true, Size: "1"}))
		}()
	}
	// 下栏原本在侧栏右边（侧栏整高）。整宽并回会铺到侧栏下面、把侧栏压矮，所以先停放侧栏，下栏回去后再把侧栏整高接回左边。
	if region == "bottom" && sidebar != "" {
		if err := m.Park(sidebar, "parked-left"); err != nil {
			return err
		}
		defer func() {
			err = errors.Join(err, restore(m, s, center, i18n.T("layout.pane.side"), sidebar,
				agentdeck.SplitSpec{Horizontal: true, Before: true, Full: true, Size: strconv.Itoa(s.LeftWidth)}))
		}()
	}
	prev := ""
	for i, p := range parked {
		var spec agentdeck.SplitSpec
		target := center
		switch {
		case region == "left":
			spec = agentdeck.SplitSpec{Horizontal: true, Before: true, Full: true, Size: strconv.Itoa(s.LeftWidth)}
		case i > 0:
			target = prev
			if region == "right" {
				spec = agentdeck.SplitSpec{Size: s.rightStack(len(parked))}
			} else {
				spec = agentdeck.SplitSpec{Horizontal: true, Size: "50%"}
			}
		case region == "right":
			spec = agentdeck.SplitSpec{Horizontal: true, Size: strconv.Itoa(s.RightWidth)}
		default:
			spec = agentdeck.SplitSpec{Full: true, Size: strconv.Itoa(s.BottomHeight)}
		}
		if err := m.Unpark(p.ID, target, spec); err != nil {
			return err
		}
		prev = p.ID
	}
	if err := Normalize(m, s); err != nil {
		return err
	}
	return m.Select(center)
}

// restore 把为并回而暂时停放的窗格接回原位，再整理尺寸、选回中间。
func restore(m Mux, s Spec, center, what, pane string, spec agentdeck.SplitSpec) error {
	if err := m.Unpark(pane, center, spec); err != nil {
		return i18n.Errorf("layout.err.restore", what, err)
	}
	if err := Normalize(m, s); err != nil {
		return err
	}
	return m.Select(center)
}
