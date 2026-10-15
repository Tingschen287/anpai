package deckui

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// 设置界面的子视图（n057）：编辑一个全局 agent（模型增删、改 id、标档位、移出）、从本机工具里加入 agent、
// 编辑项目某一档的模型清单（多选、排顺序）。全部只改界面上的设置，按 s 才整份保存。

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
}

// modelInput 是输入模型 id 的输入框，带候选（该 CLI 自己列出的模型，按输入过滤）。
type modelInput struct {
	rename  string // 不为空：改这个模型的 id；为空：加模型
	text    string
	all     []string // 候选（toolModelsMsg 填）
	loading bool
	note    string
	cur     int // 候选里的光标；-1 = 用输入的文字
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

var modelIDOK = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@+-]{0,127}$`)

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
	m.sub, m.msg = &subView{kind: subAdd}, "正在查本机装了哪些 agent CLI……"
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
			m.msg = "读不到工具目录：" + errText(msg.err)
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
		in := m.sub.input
		in.loading = false
		if msg.err != nil {
			in.note = "列不出模型（" + oneLine(deckstore.Clean(errText(msg.err), 120)) + "）：手填 id"
			return nil
		}
		in.all = msg.models
		if len(msg.models) == 0 {
			in.note = "这个 CLI 没有列模型的命令：手填 id"
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
	switch k {
	case "esc", "q":
		m.sub = nil // 提示留着（例如清单变了、主 agent 自动换了）
		return nil
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
		m.msg = t.Name + " 没装（PATH 里找不到 " + t.Binary + "）：先装好再加"
		return nil
	}
	if !deckstore.ValidID(t.Tool) || m.agentPtr(t.Tool) != nil {
		m.msg = "已有 id 为 " + t.Tool + " 的 agent"
		return nil
	}
	a := deckstore.AgentDef{ID: t.Tool, Name: t.Name, Tool: t.Tool, Binary: t.Binary, Agent: t.Agent,
		Models: append([]deckstore.ModelDef(nil), t.Models...)}
	m.set.Agents = append(m.set.Agents, a)
	m.touch()
	m.sub = &subView{kind: subAgent, agent: a.ID}
	m.msg = "已加入 " + t.Name + "（带默认模型，可在这里改）；按 esc 回去在项目里勾选它，按 s 保存"
	return nil
}

func (m *Settings) addView() string {
	var b strings.Builder
	b.WriteString(bold + brand + " 加入本机的 agent" + reset + dimOn + "   ↑↓ 选  ⏎ 加入  esc 返回" + reset + "\n\n")
	if m.tools == nil {
		b.WriteString("  " + m.msg + "\n")
		return b.String()
	}
	opts := m.addable()
	if len(opts) == 0 {
		b.WriteString(dimOn + "  Agent Deck 认识的 agent 都已加入了" + reset + "\n")
	}
	m.sub.cur = max(0, min(m.sub.cur, len(opts)-1))
	for i, t := range opts {
		cur := " "
		if i == m.sub.cur {
			cur = brand + "›" + reset
		}
		state := green + "●" + reset + " " + dimOn + sanitize(t.Path) + "  " + sanitize(t.Version) + reset
		if !t.Installed {
			state = dimOn + "○ 没装" + reset
		}
		fmt.Fprintf(&b, " %s %-12s %-11s %s\n", cur, sanitize(t.Name), sanitize(t.Binary), state)
	}
	b.WriteString("\n" + dimOn + "  加入时带一份默认模型与档位，加入后可改。命令与启动额外参数不能在这里改。" + reset + "\n")
	if m.msg != "" {
		b.WriteString("\n " + oneLine(deckstore.Clean(m.msg, 240)))
	}
	return b.String()
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
			m.msg = fmt.Sprintf("%s 在项目「%s」的档位清单里：先在项目里拿掉它，再改档位", md.ID, strings.Join(users, "、"))
			return nil
		}
		md.Tier = next
		m.touch()
	case "+", "a":
		in := &modelInput{cur: -1}
		m.sub.input = in
		if t, ok := m.toolInfo(a.Tool); ok && t.CanList {
			in.loading, in.note = true, "正在问 "+t.Binary+" 有哪些模型……"
			tool := a.Tool
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				ms, err := m.API.ToolModels(ctx, tool)
				return toolModelsMsg{tool: tool, models: ms, err: err}
			}
		}
		in.note = "手填模型 id"
	case "e":
		if n == 0 {
			return nil
		}
		id := a.Models[m.sub.cur].ID
		m.sub.input = &modelInput{rename: id, text: id, cur: -1, note: "改 id（项目档位清单里的也一并改）"}
	case "x", "delete":
		if n == 0 {
			return nil
		}
		id := a.Models[m.sub.cur].ID
		if users := m.projectsWithModel(id); len(users) > 0 {
			m.msg = fmt.Sprintf("%s 在项目「%s」的档位清单里：先在项目里拿掉它，再删", id, strings.Join(users, "、"))
			return nil
		}
		a.Models = append(a.Models[:m.sub.cur], a.Models[m.sub.cur+1:]...)
		m.touch()
		m.msg = "已删掉 " + id
	case "r":
		if users := m.projectsUsing(a.ID); len(users) > 0 {
			m.msg = fmt.Sprintf("项目「%s」勾选了 %s：先在这些项目里取消勾选，再移出", strings.Join(users, "、"), a.Name)
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
		m.sub, m.msg = nil, "已把 "+name+" 移出全局可用清单（按 s 保存）"
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

func (m *Settings) agentView() string {
	a := m.agentPtr(m.sub.agent)
	if a == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, bold+brand+" %s"+reset+dimOn+"（%s）   ↑↓ 选  ⏎ 换档位  + 加模型  e 改 id  x 删模型  r 移出全局  esc 返回"+reset+"\n\n", sanitize(a.Name), sanitize(a.ID))
	cmd := "命令 " + sanitize(a.Binary)
	if t, ok := m.toolInfo(a.Tool); ok {
		if t.Installed {
			cmd += "  " + sanitize(t.Version)
		} else {
			cmd += "  " + orange + "没装" + reset + dimOn
		}
	}
	b.WriteString(dimOn + "  " + cmd + "  · agent-deck 工具 " + sanitize(a.Tool) + reset + "\n")
	if len(a.ExtraArgs) > 0 {
		var parts []string
		for _, g := range a.ExtraArgs {
			parts = append(parts, sanitize(strings.Join(g, " ")))
		}
		b.WriteString(dimOn + "  启动额外参数（安全白名单，只读；要改请手改 ~/.config/deck/agents.yaml）：" + strings.Join(parts, "；") + reset + "\n")
	}
	b.WriteString("\n")
	if len(a.Models) == 0 {
		b.WriteString(dimOn + "  还没有模型：按 + 加" + reset + "\n")
	}
	m.sub.cur = max(0, min(m.sub.cur, len(a.Models)-1))
	for i, md := range a.Models {
		cur := " "
		if i == m.sub.cur && m.sub.input == nil {
			cur = brand + "›" + reset
		}
		use := ""
		if users := m.projectsWithModel(md.ID); len(users) > 0 {
			use = dimOn + "  项目在用：" + sanitize(strings.Join(users, "、")) + reset
		}
		fmt.Fprintf(&b, " %s %-34s %s%s\n", cur, sanitize(md.ID), string(md.Tier), use)
	}
	if in := m.sub.input; in != nil {
		b.WriteString("\n" + m.inputView())
	}
	b.WriteString("\n" + dimOn + "  档位：frontier 最强、balanced 日常、economy 便宜；项目每档的清单只能放同一档的模型。" + reset)
	if m.msg != "" {
		b.WriteString("\n " + oneLine(deckstore.Clean(m.msg, 240)))
	}
	return b.String()
}

// ---------------- 输入模型 id ----------------

func (in *modelInput) matches() []string {
	var out []string
	q := strings.ToLower(in.text)
	for _, s := range in.all {
		if in.rename == "" && q != "" && !strings.Contains(strings.ToLower(s), q) {
			continue
		}
		out = append(out, s)
		if len(out) >= 12 {
			break
		}
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

// applyModelInput 加一个模型（档位按光标所在模型的档位，没有就 balanced），或改一个模型的 id（项目档位清单里的一并改）。
func (m *Settings) applyModelInput(a *deckstore.AgentDef, rename, id string) string {
	if a == nil {
		return "agent 不见了"
	}
	if !modelIDOK.MatchString(id) {
		return "模型 id 只能是字母数字和 . _ / : @ + -，不能有空格"
	}
	if owner := m.ownerOf(id); owner != "" && !(rename == id) {
		return id + " 已是 " + owner + " 的模型"
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
		m.msg = rename + " 改成了 " + id
		return ""
	}
	tier := deckstore.Balanced
	if len(a.Models) > 0 {
		tier = a.Models[max(0, min(m.sub.cur, len(a.Models)-1))].Tier
	}
	a.Models = append(a.Models, deckstore.ModelDef{ID: id, Tier: tier})
	m.sub.cur = len(a.Models) - 1
	m.touch()
	m.msg = "加了 " + id + "（" + string(tier) + "，⏎ 换档位）"
	return ""
}

func (m *Settings) inputView() string {
	in := m.sub.input
	var b strings.Builder
	title := "加模型"
	if in.rename != "" {
		title = "改 " + sanitize(in.rename) + " 的 id"
	}
	fmt.Fprintf(&b, "  %s：%s%s▏\n", title, bold, sanitize(in.text)+reset)
	if in.note != "" {
		b.WriteString(dimOn + "  " + oneLine(deckstore.Clean(in.note, 160)) + reset + "\n")
	}
	if in.rename == "" {
		for i, s := range in.matches() {
			cur := " "
			if i == in.cur {
				cur = brand + "›" + reset
			}
			taken := ""
			if o := m.ownerOf(s); o != "" {
				taken = dimOn + "（已有）" + reset
			}
			fmt.Fprintf(&b, "   %s %s%s\n", cur, sanitize(s), taken)
		}
		b.WriteString(dimOn + "  打字过滤，↑↓ 选候选，⏎ 加入（不选候选就用输入的文字），esc 取消" + reset + "\n")
	} else {
		b.WriteString(dimOn + "  ⏎ 确定，esc 取消" + reset + "\n")
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

func (m *Settings) tierView() string {
	p := m.project()
	if p == nil {
		return ""
	}
	t := m.sub.tier
	var b strings.Builder
	fmt.Fprintf(&b, bold+brand+" %s · %s 档"+reset+dimOn+"   ↑↓ 选  ⏎/空格 放进/拿掉  [ ] 或 K J 调顺序  esc 返回"+reset+"\n\n", sanitize(p.Name), string(t))
	opts := m.tierOptions(p, t)
	if len(opts) == 0 {
		b.WriteString(dimOn + "  勾选的 agent 里没有 " + string(t) + " 档的模型：先在项目里勾选 agent，或在全局 agent 里把模型标成这一档" + reset + "\n")
	}
	m.sub.cur = max(0, min(m.sub.cur, len(opts)-1))
	for i, id := range opts {
		cur := " "
		if i == m.sub.cur {
			cur = brand + "›" + reset
		}
		mark := "   "
		for j, x := range p.Models[t] {
			if x == id {
				mark = fmt.Sprintf("%2d.", j+1)
			}
		}
		fmt.Fprintf(&b, " %s %s %-34s %s\n", cur, mark, sanitize(id), dimOn+sanitize(m.ownerOf(id))+reset)
	}
	b.WriteString("\n" + dimOn + "  只说档位派活时，按序号取第一个额度没显示用尽的；额度未知照常用；全部用尽就不派并说明。\n" +
		"  点名某个模型时不看这里：勾选的 agent 的模型都能点名用。" + reset + "\n")
	if m.msg != "" {
		b.WriteString("\n " + oneLine(deckstore.Clean(m.msg, 240)))
	}
	return b.String()
}

func (m *Settings) subView() string {
	switch m.sub.kind {
	case subAdd:
		return m.addView()
	case subAgent:
		return m.agentView()
	case subTier:
		return m.tierView()
	}
	return ""
}
