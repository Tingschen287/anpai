package deckui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// 设置界面的子视图（n057）：编辑一个全局 agent（模型增删、改 id、标档位、移出）、从本机工具里加入 agent、
// 编辑项目某一档的模型清单（多选、排顺序）。改动照样自动保存（n059）。

type subKind int

const (
	subAgent subKind = iota // 编辑一个全局 agent
	subAdd                  // 加入本机的 agent
	subTier                 // 编辑项目某一档的清单
)

type subView struct {
	kind  subKind
	agent string         // subAgent：正在编辑的 agent id
	tier  deckstore.Tier // subTier
	cur   int
	// 输入框（subAgent 里加模型、改 id）：input 不为 nil 时所有按键归输入框
	input *modelInput
	// confirm：按过一次 r、等确认移出（有手写启动参数的 agent）
	confirm bool
}

// modelInput 是输入模型 id 的输入框，带候选（该 CLI 自己列出的模型，按输入过滤）。
type modelInput struct {
	rename  string // 不为空：改这个模型的 id；为空：加模型
	text    string
	all     []string // 候选（toolModelsMsg 填）
	loading bool
	note    string
	cur     int // 候选里的光标；-1 = 用输入的文字
	limit   int // 最多列几个候选（随窗口高度缩减，n059；0 = 不列）
}

type toolsMsg struct {
	tools []ToolInfo
	err   error
}

type toolModelsMsg struct {
	tool   string
	models []string
	err    error
}

func (m *Settings) fetchTools() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		t, err := m.API.Tools(ctx)
		return toolsMsg{tools: t, err: err}
	}
}

func (m *Settings) openAgent(id string) tea.Cmd {
	m.sub, m.msg = &subView{kind: subAgent, agent: id}, ""
	return m.fetchTools()
}

func (m *Settings) openAdd() tea.Cmd {
	m.sub, m.msg = &subView{kind: subAdd}, T("add.loading")
	return m.fetchTools()
}

func (m *Settings) openTier(t deckstore.Tier) tea.Cmd {
	if m.project() == nil {
		return nil
	}
	m.sub, m.msg = &subView{kind: subTier, tier: t}, ""
	return nil
}

func (m *Settings) subMsg(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case toolsMsg:
		if msg.err != nil {
			m.msg = T("tools.fail", errLong(msg.err))
			return nil
		}
		m.tools = msg.tools
		if m.sub != nil && m.sub.kind == subAdd {
			m.msg = ""
		}
	case toolModelsMsg:
		if m.sub == nil || m.sub.input == nil {
			return nil
		}
		if a := m.agentPtr(m.sub.agent); a == nil || a.Tool != msg.tool {
			return nil // 别的 agent 的候选回来晚了（n057 自查：不能填进当前 agent 的输入框）
		}
		in := m.sub.input
		in.loading = false
		if msg.err != nil {
			in.note = T("input.listfail", oneLine(deckstore.Clean(errLong(msg.err), textCap)))
			return nil
		}
		in.all = msg.models
		if len(msg.models) == 0 {
			in.note = T("input.nolist")
		}
	}
	return nil
}

func (m *Settings) toolInfo(tool string) (ToolInfo, bool) {
	for _, t := range m.tools {
		if t.Tool == tool {
			return t, true
		}
	}
	return ToolInfo{}, false
}

func (m *Settings) agentPtr(id string) *deckstore.AgentDef {
	for i := range m.set.Agents {
		if m.set.Agents[i].ID == id {
			return &m.set.Agents[i]
		}
	}
	return nil
}

// addable 是工具目录里还没加入的项（装了的排前）。
func (m *Settings) addable() []ToolInfo {
	var in, out []ToolInfo
	for _, t := range m.tools {
		taken := false
		for _, a := range m.set.Agents {
			if a.Tool == t.Tool || a.ID == t.Tool {
				taken = true
			}
		}
		if taken {
			continue
		}
		if t.Installed {
			in = append(in, t)
		} else {
			out = append(out, t)
		}
	}
	return append(in, out...)
}

func (m *Settings) subKey(msg tea.KeyMsg) tea.Cmd {
	s := m.sub
	if s.input != nil {
		return m.inputKey(msg)
	}
	k := msg.String()
	if k != "r" {
		s.confirm = false
	}
	switch k {
	case "esc", "q":
		m.sub = nil // 提示留着（例如清单变了、主 agent 自动换了）
		return nil
	case "R":
		// 冲突提示让按 R：子视图里也认，回到页面照页面的规矩重读（有没存上的改动时要再按一次）（n059 自查）
		m.sub = nil
		_, cmd := m.key("R")
		return cmd
	case "s":
		// 存不上时底部让按 s 重试：子视图里也认（n059 自查 2）
		_, cmd := m.key("s")
		return cmd
	case "ctrl+c":
		return tea.Quit
	case "up", "k":
		s.cur = max(0, s.cur-1)
		return nil
	case "down", "j":
		s.cur++ // 各视图自己封顶
	}
	switch s.kind {
	case subAdd:
		return m.addKey(k)
	case subAgent:
		return m.agentKey(k)
	case subTier:
		return m.tierKey(k)
	}
	return nil
}

// ---------------- 加入 agent ----------------

func (m *Settings) addKey(k string) tea.Cmd {
	opts := m.addable()
	m.sub.cur = max(0, min(m.sub.cur, len(opts)-1))
	if (k != "enter" && k != " ") || len(opts) == 0 {
		return nil
	}
	t := opts[m.sub.cur]
	if !t.Installed {
		m.msg = T("add.missing", t.Name, t.Binary)
		return nil
	}
	if !deckstore.ValidID(t.Tool) || m.agentPtr(t.Tool) != nil {
		m.msg = T("add.taken", t.Tool)
		return nil
	}
	a := deckstore.AgentDef{ID: t.Tool, Name: t.Name, Tool: t.Tool, Binary: t.Binary, Agent: t.Agent,
		Models: append([]deckstore.ModelDef(nil), t.Models...)}
	m.set.Agents = append(m.set.Agents, a)
	m.touch()
	m.sub = &subView{kind: subAgent, agent: a.ID}
	m.msg = T("add.done", t.Name)
	return nil
}

func (m *Settings) addView() (string, []string) {
	var b strings.Builder
	b.WriteString(m.titleLine(" "+T("add.title"), T("add.keys")))
	if m.tools == nil {
		b.WriteString("  " + m.msg + "\n")
		return b.String(), nil
	}
	opts := m.addable()
	if len(opts) == 0 {
		b.WriteString(dimOn + "  " + T("add.none") + reset + "\n")
	}
	m.sub.cur = max(0, min(m.sub.cur, len(opts)-1))
	for i, t := range opts {
		state := green + "●" + reset + " " + dimOn + sanitizeFull(t.Path) + "  " + sanitizeFull(t.Version) + reset
		if !t.Installed {
			state = dimOn + "○ " + T("agent.missing") + reset
		}
		lines := m.wrap(fmt.Sprintf("%-12s %-11s %s", sanitizeFull(t.Name), sanitizeFull(t.Binary), state), 3) // 长路径折行、不截短
		for j, l := range lines {
			switch {
			case i == m.sub.cur && j == 0:
				b.WriteString(m.hl(" "+brand+"›"+reset+" "+l) + "\n")
			case i == m.sub.cur:
				b.WriteString(m.hl("   "+l) + "\n")
			default:
				b.WriteString("   " + l + "\n")
			}
		}
	}
	b.WriteString("\n" + m.note(T("add.about")))
	return b.String(), m.msgFoot()
}

// ---------------- 编辑一个全局 agent ----------------

func (m *Settings) agentKey(k string) tea.Cmd {
	a := m.agentPtr(m.sub.agent)
	if a == nil {
		m.sub = nil
		return nil
	}
	n := len(a.Models)
	m.sub.cur = max(0, min(m.sub.cur, n-1))
	switch k {
	case "enter", " ":
		if n == 0 {
			return nil
		}
		md := &a.Models[m.sub.cur]
		next := nextTier(md.Tier)
		if users := m.projectsWithModel(md.ID); len(users) > 0 {
			m.msg = T("model.inuse.tier", md.ID, strings.Join(users, T("sep.list")))
			return nil
		}
		md.Tier = next
		m.touch()
	case "+", "a":
		in := &modelInput{cur: -1, limit: 12} // 画面时再按窗口高度缩减
		m.sub.input = in
		if t, ok := m.toolInfo(a.Tool); ok && t.CanList {
			in.loading, in.note = true, T("input.asking", t.Binary)
			tool := a.Tool
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				ms, err := m.API.ToolModels(ctx, tool)
				return toolModelsMsg{tool: tool, models: ms, err: err}
			}
		}
		in.note = T("input.manual")
	case "e":
		if n == 0 {
			return nil
		}
		id := a.Models[m.sub.cur].ID
		m.sub.input = &modelInput{rename: id, text: id, cur: -1, note: T("input.renamenote")}
	case "x", "delete":
		if n == 0 {
			return nil
		}
		id := a.Models[m.sub.cur].ID
		if users := m.projectsWithModel(id); len(users) > 0 {
			m.msg = T("model.inuse.del", id, strings.Join(users, T("sep.list")))
			return nil
		}
		a.Models = append(a.Models[:m.sub.cur], a.Models[m.sub.cur+1:]...)
		m.touch()
		m.msg = T("model.deleted", id)
	case "r":
		if users := m.projectsUsing(a.ID); len(users) > 0 {
			m.msg = T("agent.inuse", strings.Join(users, T("sep.list")), a.Name)
			return nil
		}
		if len(a.ExtraArgs) > 0 && !m.sub.confirm {
			m.sub.confirm = true
			m.msg = T("agent.remove.confirm", a.Name)
			return nil
		}
		name := a.Name
		var keep []deckstore.AgentDef
		for _, x := range m.set.Agents {
			if x.ID != a.ID {
				keep = append(keep, x)
			}
		}
		m.set.Agents = keep
		m.touch()
		m.sub, m.msg = nil, T("agent.removed", name)
	}
	return nil
}

func nextTier(t deckstore.Tier) deckstore.Tier {
	switch t {
	case deckstore.Frontier:
		return deckstore.Balanced
	case deckstore.Balanced:
		return deckstore.Economy
	}
	return deckstore.Frontier
}

func (m *Settings) agentView() (string, []string) {
	a := m.agentPtr(m.sub.agent)
	if a == nil {
		return "", nil
	}
	var b strings.Builder
	title := m.titleLine(" "+T("agent.title", sanitizeFull(a.Name), sanitizeFull(a.ID)), T("agent.keys"))
	b.WriteString(title)
	cmd := T("agent.cmd", sanitizeFull(a.Binary))
	if t, ok := m.toolInfo(a.Tool); ok {
		if t.Installed {
			cmd += "  " + sanitizeFull(t.Version)
		} else {
			cmd += "  " + orange + T("agent.missing") + reset + dimOn
		}
	}
	b.WriteString(dimOn + "  " + cmd + "  · " + T("agent.tool", sanitizeFull(a.Tool)) + reset + "\n")
	if len(a.ExtraArgs) > 0 {
		var parts []string
		for _, g := range a.ExtraArgs {
			parts = append(parts, sanitizeFull(strings.Join(g, " ")))
		}
		b.WriteString(m.note(T("agent.extra", strings.Join(parts, T("sep.semi")))))
	}
	b.WriteString("\n")
	if len(a.Models) == 0 {
		b.WriteString(dimOn + "  " + T("agent.nomodels") + reset + "\n")
	}
	m.sub.cur = max(0, min(m.sub.cur, len(a.Models)-1))
	for i, md := range a.Models {
		use := ""
		if users := m.projectsWithModel(md.ID); len(users) > 0 {
			use = dimOn + "  " + T("model.used", sanitizeFull(strings.Join(users, T("sep.list")))) + reset // 项目多时不截（n059 自查 3）
		}
		id := sanitizeFull(md.ID)
		if pad := 34 - ansi.StringWidth(id); pad > 0 {
			id += strings.Repeat(" ", pad)
		}
		lines := m.wrap(id+" "+tierLabel(md.Tier)+use, 3) // 用的项目多时折行、不截掉（n059 自查 2）
		for j, l := range lines {
			switch {
			case i == m.sub.cur && m.sub.input == nil && j == 0:
				b.WriteString(m.hl(" "+brand+"›"+reset+" "+l) + "\n")
			case i == m.sub.cur && m.sub.input == nil:
				b.WriteString(m.hl("   "+l) + "\n")
			default:
				b.WriteString("   " + l + "\n")
			}
		}
	}
	b.WriteString("\n" + strings.TrimRight(m.note(T("agent.tiers")), "\n"))
	// 输入框、候选与提示固定在底部（n057 attempt 2 自查：模型多时滚动会把输入框滚出屏幕）；
	// 候选个数随窗口高度缩减，输入框与顶部「esc 返回」始终放得下（n059，n057 评审 2、3 的已知问题）
	var foot []string
	if in := m.sub.input; in != nil {
		in.limit = 12
		if m.height > 0 {
			// 按出口折行后的实际行数量（n059 自查 4：长候选折成两行时按一项一行算会超高）：标题、fitHeight 加的空行、
			// 输入框与候选与说明、提示，再留出「上面 / 下面还有」与至少一行列表，放不下就少列候选
			fits := func() bool {
				foot := m.fitLines(append(strings.Split(strings.TrimRight(m.inputView(), "\n"), "\n"), m.msgFoot()...))
				return strings.Count(m.fitWidth(title), "\n")+1+len(foot)+3 <= m.height
			}
			in.limit = 0
			if fits() {
				for in.limit = 12; in.limit > 0 && !fits(); in.limit-- {
				}
			} else {
				in.limit = 12 // 一个候选都不列也放不下（底部很长）：整块一起滚，候选照常列出、PgDn 翻得到（attempt 2 自查）
			}
		}
		foot = strings.Split(strings.TrimRight(m.inputView(), "\n"), "\n")
	}
	return b.String(), append(foot, m.msgFoot()...)
}

// msgFoot 是子视图底部的提示行（拒绝原因、「再按一次确认」、保存没成等）：固定显示，不随列表滚走（n057 attempt 2 自查）。
func (m *Settings) msgFoot() []string {
	var out []string
	switch { // 与页面底部（foot）同一顺序：正在重试时说「保存中」，不再说「没存上」（n059 自查 2）
	case m.conflict:
		out = append(out, orange+" "+m.saveErr+reset)
	case m.saving || (m.dirty && m.saveErr == ""):
		out = append(out, dimOn+" "+T("status.saving")+reset)
	case m.saveErr != "":
		out = append(out, orange+" "+T("status.failed", oneLine(deckstore.Clean(m.saveErr, textCap)))+reset)
	case m.saved:
		out = append(out, green+" "+T("status.savedshort")+reset) // 子视图里也看得见存上了（n059 自查）
	}
	if m.msg != "" {
		out = append(out, " "+oneLine(deckstore.Clean(m.msg, textCap)))
	}
	return m.wrapFoot(out)
}

// wrapFoot 把底部的几行按窗口宽度折行（n059 自查：冲突提示、确认提示在窄窗口被截掉）。
func (m *Settings) wrapFoot(lines []string) []string {
	if m.width <= 1 {
		return lines
	}
	var out []string
	for _, l := range lines {
		out = append(out, strings.Split(ansi.Wrap(l, m.width-1, ""), "\n")...)
	}
	return out
}

// titleLine 是子视图的标题：窄窗口时按键提示另起一行，再放不下按组折行（n057 attempt 2 自查：「esc 返回」被截掉）。
func (m *Settings) titleLine(title, keys string) string {
	if m.width <= 0 || ansi.StringWidth(title)+3+ansi.StringWidth(keys) <= m.width {
		return bold + brand + title + reset + dimOn + "   " + keys + reset + "\n\n"
	}
	return bold + brand + title + reset + "\n" + dimOn + " " + strings.Join(wrapKeys(keys, m.width), "\n ") + reset + "\n\n"
}

// ---------------- 输入模型 id ----------------

// matches 是按输入过滤后的候选（最多 limit 个）。
func (in *modelInput) matches() []string {
	var out []string
	q := strings.ToLower(in.text)
	for _, s := range in.all {
		if len(out) >= in.limit {
			break
		}
		if in.rename == "" && q != "" && !strings.Contains(strings.ToLower(s), q) {
			continue
		}
		out = append(out, s)
	}
	return out
}

func (m *Settings) inputKey(msg tea.KeyMsg) tea.Cmd {
	in := m.sub.input
	a := m.agentPtr(m.sub.agent)
	switch k := msg.String(); k {
	case "esc", "ctrl+c":
		m.sub.input = nil
	case "up":
		in.cur = max(-1, in.cur-1)
	case "down":
		in.cur = min(len(in.matches())-1, in.cur+1)
	case "backspace":
		if r := []rune(in.text); len(r) > 0 {
			in.text = string(r[:len(r)-1])
			in.cur = -1
		}
	case "enter":
		id := strings.TrimSpace(in.text)
		if ms := in.matches(); in.cur >= 0 && in.cur < len(ms) {
			id = ms[in.cur]
		}
		if err := m.applyModelInput(a, in.rename, id); err != "" {
			in.note = err
			return nil
		}
		m.sub.input = nil
	default:
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			in.text += string(msg.Runes)
			in.cur = -1
		}
	}
	return nil
}

// applyModelInput 加一个模型（档位按光标所在模型的档位，没有就日常），或改一个模型的 id（项目档位清单里的一并改）。
func (m *Settings) applyModelInput(a *deckstore.AgentDef, rename, id string) string {
	if a == nil {
		return T("input.gone")
	}
	if !deckstore.ValidModelID(id) {
		return T("input.badid")
	}
	if owner := m.ownerOf(id); owner != "" && !(rename == id) {
		return T("input.taken", id, owner)
	}
	if rename != "" {
		for i := range a.Models {
			if a.Models[i].ID == rename {
				a.Models[i].ID = id
			}
		}
		for pi := range m.set.Projects {
			for t, list := range m.set.Projects[pi].Models {
				for j := range list {
					if list[j] == rename {
						list[j] = id
					}
				}
				m.set.Projects[pi].Models[t] = list
			}
		}
		m.touch()
		m.msg = T("model.renamed", rename, id)
		return ""
	}
	tier := deckstore.Balanced
	if len(a.Models) > 0 {
		tier = a.Models[max(0, min(m.sub.cur, len(a.Models)-1))].Tier
	}
	a.Models = append(a.Models, deckstore.ModelDef{ID: id, Tier: tier})
	m.sub.cur = len(a.Models) - 1
	m.touch()
	m.msg = T("model.added", id, tierName(tier))
	return ""
}

func (m *Settings) inputView() string {
	in := m.sub.input
	var b strings.Builder
	title := T("input.add")
	if in.rename != "" {
		title = T("input.rename", sanitizeFull(in.rename))
	}
	b.WriteString("  " + T("input.prompt", title) + bold + sanitizeFull(in.text) + reset + "▏\n")
	if in.note != "" {
		b.WriteString(m.note(oneLine(deckstore.Clean(in.note, textCap))))
	}
	if in.rename == "" {
		for i, s := range in.matches() {
			cur := " "
			if i == in.cur {
				cur = brand + "›" + reset
			}
			taken := ""
			if o := m.ownerOf(s); o != "" {
				taken = dimOn + T("input.has") + reset
			}
			fmt.Fprintf(&b, "   %s %s%s\n", cur, sanitizeFull(s), taken)
		}
		b.WriteString(m.note(T("input.addkeys")))
	} else {
		b.WriteString(m.note(T("input.renamekeys")))
	}
	return b.String()
}

// ---------------- 项目某一档的清单 ----------------

func (m *Settings) tierKey(k string) tea.Cmd {
	p := m.project()
	if p == nil {
		m.sub = nil
		return nil
	}
	t := m.sub.tier
	opts := m.tierOptions(p, t)
	m.sub.cur = max(0, min(m.sub.cur, len(opts)-1))
	if len(opts) == 0 {
		return nil
	}
	id := opts[m.sub.cur]
	list := p.Models[t]
	pos := -1
	for i, x := range list {
		if x == id {
			pos = i
		}
	}
	switch k {
	case "enter", " ":
		if pos >= 0 {
			list = append(list[:pos:pos], list[pos+1:]...)
		} else {
			list = append(list, id)
		}
	case "K", "shift+up", "[":
		if pos <= 0 {
			return nil
		}
		list[pos-1], list[pos] = list[pos], list[pos-1]
	case "J", "shift+down", "]":
		if pos < 0 || pos >= len(list)-1 {
			return nil
		}
		list[pos+1], list[pos] = list[pos], list[pos+1]
	default:
		return nil
	}
	if p.Models == nil {
		p.Models = map[deckstore.Tier]deckstore.ModelList{}
	}
	if len(list) == 0 {
		delete(p.Models, t)
	} else {
		p.Models[t] = list
	}
	// 选项按「已选在前」重排了：光标跟着刚动过的那个模型
	for i, o := range m.tierOptions(p, t) {
		if o == id {
			m.sub.cur = i
		}
	}
	m.fixMain(p)
	m.touch()
	return nil
}

// tierOptions 是这一档的可选模型：先按清单顺序列已选的，再列没选的（勾选的 agent 里属于这一档的）。
func (m *Settings) tierOptions(p *deckstore.ProjectDef, t deckstore.Tier) []string {
	out := append([]string(nil), p.Models[t]...)
	for _, id := range m.modelsFor(p, t) {
		if !p.Models[t].Has(id) {
			out = append(out, id)
		}
	}
	return out
}

func (m *Settings) tierView() (string, []string) {
	p := m.project()
	if p == nil {
		return "", nil
	}
	t := m.sub.tier
	var b strings.Builder
	b.WriteString(m.titleLine(" "+T("tierview.title", sanitizeFull(p.Name), tierName(t)), T("tierview.keys")))
	opts := m.tierOptions(p, t)
	if len(opts) == 0 {
		b.WriteString(dimOn + "  " + T("tierview.empty", tierName(t)) + reset + "\n")
	}
	m.sub.cur = max(0, min(m.sub.cur, len(opts)-1))
	for i, id := range opts {
		mark := "   "
		for j, x := range p.Models[t] {
			if x == id {
				mark = fmt.Sprintf("%2d.", j+1)
			}
		}
		name := sanitizeFull(id)
		if pad := 34 - ansi.StringWidth(name); pad > 0 {
			name += strings.Repeat(" ", pad)
		}
		line := mark + " " + name + " " + dimOn + sanitizeFull(m.agentName(m.ownerOf(id))) + reset
		if i == m.sub.cur {
			b.WriteString(m.hl(" "+brand+"›"+reset+" "+line) + "\n")
		} else {
			b.WriteString("   " + line + "\n")
		}
	}
	b.WriteString("\n" + m.note(T("tierview.about")))
	return b.String(), m.msgFoot()
}

func (m *Settings) subView() (string, []string) {
	switch m.sub.kind {
	case subAdd:
		return m.addView()
	case subAgent:
		return m.agentView()
	case subTier:
		return m.tierView()
	}
	return "", nil
}
