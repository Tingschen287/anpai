package deckui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/logo"
	"github.com/Tingschen287/terminal-deck/work/internal/viewstate"
)

// SessionInfo 是 /v1/sessions 的一行：注册表会话加上 agent-deck 报告的运行状态。
type SessionInfo struct {
	deckstore.Session
	Activity string `json:"activity"`
}

// Data 是侧栏与标签条要的全部数据。
type Data struct {
	Projects []deckstore.ProjectDef
	Sessions []SessionInfo
	View     viewstate.State
	Err      string // 取数据或做动作出的错（已清洗）
	Info     string // 动作的结果说明（n055：起主 agent 等），绿色显示
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// Openable 说明一个会话能不能换进中间：要已经在 agent-deck 上建好（有会话 id 与 tmux 会话名）。
func Openable(x SessionInfo) bool { return x.AgentDeckID != "" && x.TmuxSession != "" }

// IsOpen、IsFocus、IsTaken、IsPending 按 viewstate 的状态判断（它统一用 agent-deck 会话 id）。
func (d Data) IsOpen(x SessionInfo) bool {
	return x.AgentDeckID != "" && contains(d.View.Open, x.AgentDeckID)
}
func (d Data) IsFocus(x SessionInfo) bool {
	return x.AgentDeckID != "" && d.View.Focus == x.AgentDeckID
}
func (d Data) IsPending(x SessionInfo) bool {
	return x.AgentDeckID != "" && contains(d.View.HandbackPending, x.AgentDeckID)
}
func (d Data) IsTaken(x SessionInfo) bool {
	return x.AgentDeckID != "" && contains(d.View.TakenOver, x.AgentDeckID) && !d.IsPending(x)
}

// Group 是一个项目和它的会话（主 agent 在前，其余按创建先后）。
type Group struct {
	ID       string // 项目 id
	Name     string
	Sessions []SessionInfo
}

// Groups 按项目分组：项目按设置里的顺序，设置里没有的项目排在最后。
func (d Data) Groups() []Group {
	byProject := map[string][]SessionInfo{}
	for _, x := range d.Sessions {
		byProject[x.Project] = append(byProject[x.Project], x)
	}
	var out []Group
	seen := map[string]bool{}
	add := func(id, name string) {
		list := byProject[id]
		sort.SliceStable(list, func(i, j int) bool {
			if (list[i].Role == "main") != (list[j].Role == "main") {
				return list[i].Role == "main"
			}
			return list[i].CreatedAt.Before(list[j].CreatedAt)
		})
		out = append(out, Group{ID: id, Name: name, Sessions: list})
		seen[id] = true
	}
	for _, p := range d.Projects {
		name := p.Name
		if name == "" {
			name = p.ID
		}
		add(p.ID, name)
	}
	var rest []string
	for id := range byProject {
		if !seen[id] {
			rest = append(rest, id)
		}
	}
	sort.Strings(rest)
	for _, id := range rest {
		add(id, id)
	}
	return out
}

// Ordered 是侧栏里会话的先后（键盘上下移动按它）。
func (d Data) Ordered() []SessionInfo {
	var out []SessionInfo
	for _, g := range d.Groups() {
		out = append(out, g.Sessions...)
	}
	return out
}

// Entry 是侧栏里可以选中的一行：项目标题（Session 为 -1）或会话（Ordered 里的下标）。n055 起项目标题也能选，
// 新建、还没有会话的项目才能用键盘选到、按 m 起主 agent。
type Entry struct {
	Project string
	Session int
}

// Entries 是键盘上下移动的先后：每个项目先是标题，再是它的会话。
func (d Data) Entries() []Entry {
	var out []Entry
	idx := 0
	for _, g := range d.Groups() {
		out = append(out, Entry{Project: g.ID, Session: -1})
		for range g.Sessions {
			out = append(out, Entry{Project: g.ID, Session: idx})
			idx++
		}
	}
	return out
}

// 状态圆点与说明。agent-deck 的状态只对在跑的会话有意义；注册表里不在跑的按注册表状态说。
func status(x SessionInfo) (dot, text string) {
	switch x.Status {
	case deckstore.StatusLaunching:
		return "\x1b[38;5;245m◌\x1b[0m", "启动中"
	case deckstore.StatusUncertain:
		return "\x1b[38;5;214m◌\x1b[0m", "启动结果不明"
	case deckstore.StatusDone:
		return "\x1b[38;5;242m○\x1b[0m", "已完成"
	case deckstore.StatusFailed:
		return "\x1b[38;5;196m○\x1b[0m", "启动失败"
	case deckstore.StatusStopped:
		return "\x1b[38;5;242m○\x1b[0m", "已停止"
	}
	switch x.Activity {
	case "running":
		return "\x1b[38;5;42m●\x1b[0m", "在跑"
	case "waiting":
		return "\x1b[38;5;220m●\x1b[0m", "等你输入"
	case "idle":
		return "\x1b[38;5;245m●\x1b[0m", "空闲"
	case "error":
		return "\x1b[38;5;196m●\x1b[0m", "出错"
	}
	return "\x1b[38;5;242m○\x1b[0m", "状态未知"
}

// Sidebar 是左侧会话栏。
type Sidebar struct {
	Data   Data
	Sel    int    // 键盘选中的会话（Ordered 里的下标）；Proj 不空时不用
	Proj   string // 键盘选中的是这个项目的标题行（n055）
	Mode   string // sixel、blocks、none
	CellW  int    // 字符格像素宽高（Sixel 用；取不到按 10×20）
	CellH  int
	Scroll int // 第一行显示的会话行（会话多于窗格高度时）

	hits  map[int]hit // 屏幕行 → 会话
	cache map[string]string
}

type hit struct {
	idx  int
	eye  int    // 眼睛所在的列（行尾）；-1 表示这一行没有眼睛
	proj string // 项目标题行：项目 id（idx 为 -1）
}

// Hit 返回点在 (x, y) 的是哪个会话、是否点在眼睛上。
func (s *Sidebar) Hit(x, y int) (idx int, eye bool, ok bool) {
	h, ok := s.hits[y]
	if !ok || h.idx < 0 {
		return 0, false, false
	}
	return h.idx, h.eye >= 0 && x >= h.eye-1, true
}

// HitProject 返回点在第 y 行的项目标题（n055）。
func (s *Sidebar) HitProject(y int) (string, bool) {
	h, ok := s.hits[y]
	if !ok || h.idx >= 0 || h.proj == "" {
		return "", false
	}
	return h.proj, true
}

// Move 按 Entries 上下移动选中行（项目标题与会话都算一行）。
func (s *Sidebar) Move(delta int) {
	es := s.Data.Entries()
	if len(es) == 0 {
		return
	}
	cur := 0
	for i, e := range es {
		if s.Proj != "" && e.Session < 0 && e.Project == s.Proj || s.Proj == "" && e.Session == s.Sel {
			cur = i
			break
		}
	}
	e := es[max(0, min(len(es)-1, cur+delta))]
	if e.Session < 0 {
		s.Proj = e.Project
	} else {
		s.Proj, s.Sel = "", e.Session
	}
}

// SelectedProject 是选中行所属的项目（项目标题或会话所在项目）。
func (s *Sidebar) SelectedProject() string {
	if s.Proj != "" {
		return s.Proj
	}
	if list := s.Data.Ordered(); s.Sel >= 0 && s.Sel < len(list) {
		return list[s.Sel].Project
	}
	return ""
}

const logoCols, logoRows = 4, 2

func (s *Sidebar) logoLines(tool string) []string {
	if s.Mode == "blocks" {
		if im := logo.For(tool); im != nil {
			return logo.Blocks(im, logoCols, logoRows)
		}
	}
	if s.Mode == "sixel" && logo.For(tool) != nil {
		return []string{"    ", "    "} // 留出位置，图另画
	}
	b, ok := logo.Brand[tool]
	if !ok {
		return []string{" \x1b[38;5;245m?\x1b[0m  ", "    "}
	}
	return []string{fmt.Sprintf(" \x1b[1;38;2;%d;%d;%dm%s\x1b[0m  ", b.Color.R, b.Color.G, b.Color.B, b.Letter), "    "}
}

func (s *Sidebar) sixel(tool string) string {
	cw, ch := s.CellW, s.CellH
	if cw <= 0 || ch <= 0 {
		cw, ch = 10, 20
	}
	key := fmt.Sprintf("%s/%dx%d", tool, cw, ch)
	if s.cache == nil {
		s.cache = map[string]string{}
	}
	if v, ok := s.cache[key]; ok {
		return v
	}
	// 高度按两行算、宽度取同样像素，图是正方形；不会超出四列（字符格通常是 1:2）
	side := min(logoRows*ch, logoCols*cw)
	v := logo.Sixel(logo.For(tool), side, side)
	s.cache[key] = v
	return v
}

// Render 画一整屏（宽 w、高 h）。
func (s *Sidebar) Render(w, h int) Frame {
	s.hits = map[int]hit{}
	var lines []string
	var images []Image
	lines = append(lines, Fit("\x1b[1;38;5;39m Agent Deck\x1b[0m", w), "")
	ordered := s.Data.Ordered()
	if s.Sel >= len(ordered) {
		s.Sel = max(0, len(ordered)-1)
	}
	type row struct {
		text  string
		idx   int // -1 表示不是会话行
		eye   bool
		image string
		proj  string // 项目标题行的项目 id
	}
	var body []row
	idx := 0
	for _, g := range s.Data.Groups() {
		hb := ""
		if s.Proj != "" && s.Proj == g.ID {
			hb = "\x1b[48;5;236m"
		}
		body = append(body, row{text: Fit(fmt.Sprintf("%s\x1b[1m ▾ %s\x1b[22m\x1b[38;5;242m · %d\x1b[0m", hb, sanitize(g.Name), len(g.Sessions)), w), idx: -1, proj: g.ID})
		if len(g.Sessions) == 0 {
			body = append(body, row{text: Fit("\x1b[38;5;242m   按 m 起主 agent\x1b[0m", w), idx: -1})
		}
		for _, x := range g.Sessions {
			dot, st := status(x)
			bar, bg := " ", ""
			if s.Data.IsFocus(x) {
				bar = "\x1b[38;5;39m▌\x1b[0m"
			}
			if s.Proj == "" && idx == s.Sel {
				bg = "\x1b[48;5;236m"
			}
			lg := s.logoLines(x.Tool)
			title := sanitize(x.Title)
			if x.Role == "main" {
				title = "\x1b[38;5;220m★\x1b[0m" + bg + " " + title
			}
			if !Openable(x) {
				title = "\x1b[38;5;242m" + title + "\x1b[0m"
			}
			eye := "\x1b[38;5;242m○\x1b[0m"
			if s.Data.IsOpen(x) {
				eye = "\x1b[38;5;39m◉\x1b[0m"
			}
			sub := sanitize(x.Tool) + " · " + st
			switch {
			case s.Data.IsTaken(x):
				sub += " · \x1b[38;5;214m接管中\x1b[0m\x1b[38;5;245m"
			case s.Data.IsPending(x):
				sub += " · \x1b[38;5;214m待交还\x1b[0m\x1b[38;5;245m"
			}
			content := w - 1 - logoCols - 1 - 2
			r1 := bar + lg[0] + bg + " " + Fit(dot+bg+" "+title, content) + bg + " " + eye + "\x1b[0m"
			r2 := bar + lg[1] + bg + " " + Fit("\x1b[38;5;245m"+sub+"\x1b[0m"+bg, content) + bg + "  \x1b[0m"
			im := ""
			if s.Mode == "sixel" && logo.For(x.Tool) != nil {
				im = s.sixel(x.Tool)
			}
			body = append(body, row{text: r1, idx: idx, eye: true, image: im}, row{text: r2, idx: idx})
			idx++
		}
		body = append(body, row{idx: -1})
	}
	footer := []string{Fit("\x1b[38;5;242m ↑↓ 选  ⏎ 打开  h 放回后台\x1b[0m", w), Fit("\x1b[38;5;242m m 起主 agent\x1b[0m", w)}
	// 结果与出错说明可能比侧栏宽（例如「停在目录信任确认上……」）：折行显示，最多 4 行（n055 端到端：一行截断看不全）
	if s.Data.Info != "" {
		footer = append(wrapLines(s.Data.Info, w, "\x1b[38;5;42m"), footer...)
	}
	if s.Data.Err != "" {
		footer = append(wrapLines(s.Data.Err, w, "\x1b[38;5;196m"), footer...)
	}
	room := h - len(lines) - len(footer)
	// 让选中的会话可见
	selRow := -1
	for i, r := range body {
		if s.Proj == "" && r.idx == s.Sel && r.eye || s.Proj != "" && r.proj == s.Proj {
			selRow = i
		}
	}
	if selRow >= 0 {
		if selRow < s.Scroll {
			s.Scroll = selRow
		}
		if selRow+1 >= s.Scroll+room {
			s.Scroll = selRow + 2 - room
		}
	}
	s.Scroll = max(0, min(s.Scroll, len(body)-room))
	for i := s.Scroll; i < len(body) && len(lines) < h-len(footer); i++ {
		r := body[i]
		y := len(lines)
		lines = append(lines, r.text)
		if r.proj != "" {
			s.hits[y] = hit{idx: -1, eye: -1, proj: r.proj}
		}
		if r.idx >= 0 {
			e := -1
			if r.eye {
				e = w - 2
			}
			s.hits[y] = hit{idx: r.idx, eye: e}
			if r.image != "" && i+1 < len(body) && len(lines) < h-len(footer) {
				images = append(images, Image{Row: y, Col: 1, Data: r.image}) // 两行都在屏上才画图
			}
		}
	}
	for len(lines) < h-len(footer) {
		lines = append(lines, "")
	}
	lines = append(lines, footer...)
	return Frame{Lines: lines, Images: images}
}

// wrapLines 把一段说明按宽度折成最多 4 行（每行前空一格），超出的截断。
func wrapLines(text string, w int, color string) []string {
	lines := strings.Split(ansi.Hardwrap(oneLine(text), max(1, w-1), false), "\n")
	if len(lines) > 4 {
		lines = lines[:4]
		lines[3] = ansi.Truncate(lines[3], max(1, w-2), "") + "…"
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = Fit(color+" "+l+"\x1b[0m", w)
	}
	return out
}

// sanitize 去掉会话标题里的控制字符与疑似密钥（标题来自模型，可能夹带转义序列），
// 并压成一行：Clean 为多行报告保留换行与制表符，单行字段里它们会挪动行列、错开点击区域。
func sanitize(s string) string { return oneLine(deckstore.Clean(strings.TrimSpace(s), 80)) }

func oneLine(s string) string { return strings.NewReplacer("\n", " ", "\t", " ").Replace(s) }
