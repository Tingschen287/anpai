package deckui

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// agent 设置界面（架构稿 §14.2，n049）：看全局可用清单（标出没装的）、为项目勾选 agent 与三档模型、选主 agent、
// 撤销已记住的 Codex 授权。数据与校验在 deckd（n050），这里只编辑并整份 PUT 回去。

// SettingsAPI 是设置界面对 deckd 的调用（测试可换）。
type SettingsAPI interface {
	Load(ctx context.Context) (deckstore.Settings, []deckstore.TrustGrant, error)
	Save(ctx context.Context, s deckstore.Settings) error
	Revoke(ctx context.Context, project, tool string) error
}

// DeckSettingsAPI 走 deckd。
type DeckSettingsAPI struct{ C *deckclient.Client }

func (a DeckSettingsAPI) Load(ctx context.Context) (deckstore.Settings, []deckstore.TrustGrant, error) {
	var s deckstore.Settings
	var g []deckstore.TrustGrant
	if err := a.C.Get(ctx, "/v1/settings", &s); err != nil {
		return s, nil, err
	}
	if err := a.C.Get(ctx, "/v1/trust", &g); err != nil {
		return s, nil, err
	}
	return s, g, nil
}

func (a DeckSettingsAPI) Save(ctx context.Context, s deckstore.Settings) error {
	var out any
	return a.C.Do(ctx, "PUT", "/v1/settings", map[string]any{"agents": s.Agents, "projects": s.Projects}, &out)
}

func (a DeckSettingsAPI) Revoke(ctx context.Context, project, tool string) error {
	var out any
	return a.C.Do(ctx, "DELETE", "/v1/trust?project="+url.QueryEscape(project)+"&tool="+url.QueryEscape(tool), nil, &out)
}

// rowKind 是设置界面里一行能做什么。
type rowKind int

const (
	rowAgentPick rowKind = iota // 项目里勾选一个 agent
	rowTier                     // 项目的某一档模型
	rowMain                     // 项目的主 agent
	rowTrust                    // 一条记住的授权
)

type row struct {
	kind  rowKind
	agent string
	tier  deckstore.Tier
	trust deckstore.TrustGrant
}

// Settings 是设置界面的 Bubble Tea 模型。
type Settings struct {
	API       SettingsAPI
	Installed func(binary string) bool // 判断一个 agent 是否已装（默认查 PATH）
	set       deckstore.Settings
	trust     []deckstore.TrustGrant
	proj      int // 正在编辑的项目
	cur       int // 光标所在的行（rows 下标）
	dirty     bool
	quitAsked bool
	msg       string
	loaded    bool
	width     int
}

type loadedMsg struct {
	set   deckstore.Settings
	trust []deckstore.TrustGrant
	err   error
}
type doneMsg struct {
	what string
	err  error
}

func (m *Settings) Init() tea.Cmd { return m.load }

func (m *Settings) load() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, g, err := m.API.Load(ctx)
	return loadedMsg{s, g, err}
}

func (m *Settings) installed(a deckstore.AgentDef) bool {
	bin := a.Binary
	if bin == "" {
		bin = a.Tool
	}
	if m.Installed != nil {
		return m.Installed(bin)
	}
	_, err := exec.LookPath(bin)
	return err == nil
}

func (m *Settings) project() *deckstore.ProjectDef {
	if m.proj < 0 || m.proj >= len(m.set.Projects) {
		return nil
	}
	return &m.set.Projects[m.proj]
}

func (m *Settings) rows() []row {
	var rs []row
	if p := m.project(); p != nil {
		for _, a := range m.set.Agents {
			rs = append(rs, row{kind: rowAgentPick, agent: a.ID})
		}
		for i := len(deckstore.Tiers) - 1; i >= 0; i-- {
			rs = append(rs, row{kind: rowTier, tier: deckstore.Tiers[i]})
		}
		rs = append(rs, row{kind: rowMain})
	}
	for _, g := range m.trust {
		rs = append(rs, row{kind: rowTrust, trust: g})
	}
	return rs
}

func (m *Settings) agent(id string) (deckstore.AgentDef, bool) {
	for _, a := range m.set.Agents {
		if a.ID == id {
			return a, true
		}
	}
	return deckstore.AgentDef{}, false
}

// modelsFor 是项目某一档可选的模型：本项目勾选的 agent 里，属于这一档的模型（按 agent、模型名排序）。
func (m *Settings) modelsFor(p *deckstore.ProjectDef, tier deckstore.Tier) []string {
	var out []string
	for _, id := range p.Agents {
		a, ok := m.agent(id)
		if !ok {
			continue
		}
		for _, md := range a.Models {
			if md.Tier == tier {
				out = append(out, md.ID)
			}
		}
	}
	sort.Strings(out)
	return out
}

func picked(p *deckstore.ProjectDef, id string) bool {
	for _, x := range p.Agents {
		if x == id {
			return true
		}
	}
	return false
}

// togglePick 勾选或取消一个 agent。取消时，引用它的档位模型与主 agent 一并清掉（否则存不进去：deckd 会拒绝）。
func (m *Settings) togglePick(id string) {
	p := m.project()
	if picked(p, id) {
		var keep []string
		for _, x := range p.Agents {
			if x != id {
				keep = append(keep, x)
			}
		}
		p.Agents = keep
		a, _ := m.agent(id)
		for tier, model := range p.Models {
			for _, md := range a.Models {
				if md.ID == model {
					delete(p.Models, tier)
				}
			}
		}
		if p.MainAgent.Agent == id {
			p.MainAgent.Agent = ""
			if len(p.Agents) > 0 {
				p.MainAgent.Agent = p.Agents[0]
			}
		}
	} else {
		p.Agents = append(p.Agents, id)
	}
	m.dirty = true
}

// cycleTier 让某一档换到下一个可选模型；最后一个之后是「不设」。
func (m *Settings) cycleTier(tier deckstore.Tier) {
	p := m.project()
	opts := append(m.modelsFor(p, tier), "")
	cur := p.Models[tier]
	next := opts[0]
	for i, o := range opts {
		if o == cur {
			next = opts[(i+1)%len(opts)]
		}
	}
	if p.Models == nil {
		p.Models = map[deckstore.Tier]string{}
	}
	if next == "" {
		delete(p.Models, tier)
	} else {
		p.Models[tier] = next
	}
	m.dirty = true
}

// cycleMain 让主 agent 在「勾选的 agent × 三档」之间轮换。
func (m *Settings) cycleMain() {
	p := m.project()
	type opt struct {
		agent string
		tier  deckstore.Tier
	}
	var opts []opt
	for _, id := range p.Agents {
		for i := len(deckstore.Tiers) - 1; i >= 0; i-- {
			opts = append(opts, opt{id, deckstore.Tiers[i]})
		}
	}
	if len(opts) == 0 {
		m.msg = "先勾选至少一个 agent"
		return
	}
	next := opts[0]
	for i, o := range opts {
		if o.agent == p.MainAgent.Agent && o.tier == p.MainAgent.Tier {
			next = opts[(i+1)%len(opts)]
		}
	}
	p.MainAgent = deckstore.MainAgent{Agent: next.agent, Tier: next.tier}
	m.dirty = true
}

func (m *Settings) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case loadedMsg:
		if msg.err != nil {
			m.msg = "读不到设置：" + errText(msg.err)
			return m, nil
		}
		m.set, m.trust, m.loaded, m.dirty = msg.set, msg.trust, true, false
		if m.proj >= len(m.set.Projects) {
			m.proj = 0
		}
	case doneMsg:
		if msg.err != nil {
			m.msg = msg.what + "失败：" + errText(msg.err)
			return m, nil
		}
		m.msg = msg.what + "完成"
		return m, m.load
	case tea.KeyMsg:
		return m.key(msg.String())
	}
	return m, nil
}

func (m *Settings) key(k string) (tea.Model, tea.Cmd) {
	if k != "q" && k != "esc" {
		m.quitAsked = false
	}
	rs := m.rows()
	switch k {
	case "q", "esc", "ctrl+c":
		if m.dirty && !m.quitAsked && k != "ctrl+c" {
			m.quitAsked = true
			m.msg = "有没保存的改动：按 s 保存，或再按一次 q 放弃并关闭"
			return m, nil
		}
		return m, tea.Quit
	case "up", "k":
		m.cur = max(0, m.cur-1)
	case "down", "j":
		m.cur = min(len(rs)-1, m.cur+1)
	case "left", "h":
		if len(m.set.Projects) > 0 {
			m.proj = (m.proj + len(m.set.Projects) - 1) % len(m.set.Projects)
		}
	case "right", "l":
		if len(m.set.Projects) > 0 {
			m.proj = (m.proj + 1) % len(m.set.Projects)
		}
	case " ", "enter":
		if m.cur >= len(rs) {
			break
		}
		switch r := rs[m.cur]; r.kind {
		case rowAgentPick:
			m.togglePick(r.agent)
		case rowTier:
			m.cycleTier(r.tier)
		case rowMain:
			m.cycleMain()
		}
	case "d":
		if m.cur < len(rs) && rs[m.cur].kind == rowTrust {
			g := rs[m.cur].trust
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				return doneMsg{"撤销 " + g.Project + " 的 " + g.Tool + " 授权", m.API.Revoke(ctx, g.Project, g.Tool)}
			}
		}
	case "s":
		set := m.set
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return doneMsg{"保存", m.API.Save(ctx, set)}
		}
	}
	return m, nil
}

func (m *Settings) View() string {
	var b strings.Builder
	b.WriteString("\x1b[1;38;5;39m agent 设置\x1b[0m\x1b[38;5;242m   ↑↓ 选  空格/⏎ 改  ←→ 换项目  d 撤销授权  s 保存  q 关闭\x1b[0m\n\n")
	if !m.loaded {
		b.WriteString("  " + m.msg + "\n")
		return b.String()
	}
	b.WriteString("\x1b[1m 全局可用的 agent\x1b[0m\n")
	for _, a := range m.set.Agents {
		mark, style := "\x1b[38;5;42m●\x1b[0m", ""
		note := ""
		if !m.installed(a) {
			mark, style, note = "\x1b[38;5;242m○\x1b[0m", "\x1b[38;5;242m", "（没装）"
		}
		var models []string
		for i := len(deckstore.Tiers) - 1; i >= 0; i-- {
			for _, md := range a.Models {
				if md.Tier == deckstore.Tiers[i] {
					models = append(models, string(md.Tier)[:1]+":"+sanitize(md.ID))
				}
			}
		}
		fmt.Fprintf(&b, "  %s %s%-12s %-8s %s%s\x1b[0m\n", mark, style, sanitize(a.Name), sanitize(a.Tool), strings.Join(models, " "), note)
	}
	b.WriteString("\n")
	rs := m.rows()
	line := 0
	cursor := func() string {
		defer func() { line++ }()
		if line == m.cur {
			return "\x1b[38;5;39m›\x1b[0m"
		}
		return " "
	}
	if p := m.project(); p != nil {
		fmt.Fprintf(&b, "\x1b[1m 项目：%s\x1b[0m\x1b[38;5;242m（%d/%d）\x1b[0m\n", sanitize(p.Name), m.proj+1, len(m.set.Projects))
		for _, a := range m.set.Agents {
			box := "[ ]"
			if picked(p, a.ID) {
				box = "\x1b[38;5;42m[x]\x1b[0m"
			}
			fmt.Fprintf(&b, " %s %s %s\n", cursor(), box, sanitize(a.Name))
		}
		for i := len(deckstore.Tiers) - 1; i >= 0; i-- {
			t := deckstore.Tiers[i]
			v := p.Models[t]
			if v == "" {
				v = "\x1b[38;5;242m（不设）\x1b[0m"
			}
			fmt.Fprintf(&b, " %s %-9s %s\n", cursor(), string(t), sanitize(v))
		}
		main := "\x1b[38;5;242m（未选）\x1b[0m"
		if p.MainAgent.Agent != "" {
			main = sanitize(p.MainAgent.Agent) + " · " + string(p.MainAgent.Tier)
		}
		fmt.Fprintf(&b, " %s 主 agent  %s\n", cursor(), main)
	} else {
		b.WriteString("\x1b[38;5;242m 还没有项目（项目由主 agent 流程建立，n051）\x1b[0m\n")
	}
	b.WriteString("\n\x1b[1m 已记住的 Codex 授权\x1b[0m\n")
	if len(m.trust) == 0 {
		b.WriteString("\x1b[38;5;242m   （没有）\x1b[0m\n")
	}
	for _, g := range m.trust {
		fmt.Fprintf(&b, " %s %s · %s · %s · %s\n", cursor(), sanitize(g.Project), sanitize(g.Tool), sanitize(g.Dir), g.ApprovedAt.Local().Format("01-02 15:04"))
	}
	_ = rs
	if m.dirty {
		b.WriteString("\n\x1b[38;5;214m 有没保存的改动\x1b[0m")
	}
	if m.msg != "" {
		b.WriteString("\n " + deckstore.Clean(m.msg, 240))
	}
	return b.String()
}

// RunSettings 跑设置界面（布局里按 F9 开的窗口，关掉就回到布局）。
func RunSettings(api SettingsAPI) error {
	_, err := tea.NewProgram(&Settings{API: api}, tea.WithAltScreen()).Run()
	return err
}
