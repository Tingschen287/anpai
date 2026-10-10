// Package layout 拼 Agent Deck v1 的四区布局（n049，架构稿 §1、§7）。
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
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
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
	b.WriteString("# Agent Deck v1 布局 server（deck up 生成，勿手改）\n")
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
		"set -g pane-active-border-style fg=colour39",
		"set -g pane-border-style fg=colour238",
		"set -g window-size latest",
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

// Keys 是布局 server 根表上的按键（外层 tmux 截获，不进中间的 CLI）。
var Keys = []struct{ Key, What string }{
	{"F2", "接管当前显示的子 agent（切走、隐藏或 Ctrl-Q 即自动交还）"},
	{"F5", "显示/隐藏左栏"},
	{"F6", "显示/隐藏右栏"},
	{"F7", "显示/隐藏下栏"},
	{"F9", "agent 设置"},
	{"F10", "回到中间窗格（键盘焦点）"},
}

// BindingsConf 是窗格建好之后的按键绑定。接管键交给标签条程序处理（它在布局窗格里，有 owner 身份；
// run-shell 起的进程不在任何窗格里，deckd 认不出它，所以不能直接调接管）。
func BindingsConf(s Spec, b Built) string {
	s = s.withDefaults()
	toggle := func(region string) string {
		return "run-shell -b " + Quote(s.deckCmd(append([]string{"layout", "toggle", region}, s.sockets()...)...))
	}
	var w strings.Builder
	w.WriteString("# Agent Deck v1 按键（deck up 生成，勿手改）\n")
	fmt.Fprintf(&w, "bind -n F2 send-keys -t %s F2\n", b.Tabs)
	fmt.Fprintf(&w, "bind -n F5 %s\n", toggle("left"))
	fmt.Fprintf(&w, "bind -n F6 %s\n", toggle("right"))
	fmt.Fprintf(&w, "bind -n F7 %s\n", toggle("bottom"))
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
func Toggle(m Mux, s Spec, region string) error {
	s = s.withDefaults()
	if region != "left" && region != "right" && region != "bottom" {
		return fmt.Errorf("没有 %q 这一栏（只有 left、right、bottom）", region)
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
		return fmt.Errorf("布局里没有中间窗格")
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
		return fmt.Errorf("%s 栏没有窗格", region)
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
			if err := m.Unpark(tabs, center, agentdeck.SplitSpec{Before: true, Size: "1"}); err == nil {
				Normalize(m, s)
				m.Select(center)
			}
		}()
	}
	// 下栏原本在侧栏右边（侧栏整高）。整宽并回会铺到侧栏下面、把侧栏压矮，所以先停放侧栏，下栏回去后再把侧栏整高接回左边。
	if region == "bottom" && sidebar != "" {
		if err := m.Park(sidebar, "parked-left"); err != nil {
			return err
		}
		defer func() {
			if err := m.Unpark(sidebar, center, agentdeck.SplitSpec{Horizontal: true, Before: true, Full: true, Size: strconv.Itoa(s.LeftWidth)}); err == nil {
				Normalize(m, s)
				m.Select(center)
			}
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
