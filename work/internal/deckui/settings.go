package deckui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// agent 设置界面（架构稿 §14.2，n049；n057 owner：「我没看到哪里可以配置我全局可用的agent和模型，还有每个主会话可用的agent和模型」）：
//   - 全局可用的 agent：从本机装了的 CLI 里加入、移出；每个 agent 的模型增删、改 id、标档位（⏎ 打开 agent）；
//   - 每个项目（每个主会话）：勾选可用的 agent，三档各一个有顺序的模型清单（可多选、可跨 agent），选主 agent；
//   - 撤销已记住的 Codex 授权。
// 数据与校验在 deckd（n050），这里只编辑并整份 PUT 回去（带上读到的代数，别处改过就拒绝）。
// 启动额外参数（extra_args）、命令（binary）、agent 标记不在界面里改，只显示并说明去哪改。

// SettingsAPI 是设置界面对 deckd 的调用（测试可换）。
type SettingsAPI interface {
	Load(ctx context.Context) (deckstore.Settings, []deckstore.TrustGrant, error)
	Save(ctx context.Context, s deckstore.Settings) error
	Revoke(ctx context.Context, project, tool string) error
	Tools(ctx context.Context) ([]ToolInfo, error)
	ToolModels(ctx context.Context, tool string) ([]string, error)
}

// ToolInfo 是工具目录的一项与它在本机的情况（deckd GET /v1/tools）。
type ToolInfo struct {
	Tool      string               `json:"tool"`
	Name      string               `json:"name"`
	Binary    string               `json:"binary"`
	Agent     bool                 `json:"agent"`
	Models    []deckstore.ModelDef `json:"models"`
	Path      string               `json:"path"`
	Version   string               `json:"version"`
	Installed bool                 `json:"installed"`
	Error     string               `json:"error"`
	Added     bool                 `json:"added"`
	CanList   bool                 `json:"can_list"`
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
	return a.C.Do(ctx, "PUT", "/v1/settings", map[string]any{"agents": s.Agents, "projects": s.Projects, "generation": s.Generation}, &out)
}

func (a DeckSettingsAPI) Revoke(ctx context.Context, project, tool string) error {
	var out any
	return a.C.Do(ctx, "DELETE", "/v1/trust?project="+url.QueryEscape(project)+"&tool="+url.QueryEscape(tool), nil, &out)
}

func (a DeckSettingsAPI) Tools(ctx context.Context) ([]ToolInfo, error) {
	var out []ToolInfo
	return out, a.C.Get(ctx, "/v1/tools", &out)
}

func (a DeckSettingsAPI) ToolModels(ctx context.Context, tool string) ([]string, error) {
	var out struct {
		Models []string `json:"models"`
	}
	return out.Models, a.C.Get(ctx, "/v1/tools/models?tool="+url.QueryEscape(tool), &out)
}

// rowKind 是设置界面里一行能做什么。
type rowKind int

const (
	rowAgentPick rowKind = iota // 项目里勾选一个 agent
	rowTier                     // 项目的某一档模型清单
	rowMain                     // 项目的主 agent
	rowTrust                    // 一条记住的授权
	rowGlobal                   // 全局可用的一个 agent（⏎ 打开编辑）
	rowAddAgent                 // 「加入本机的 agent」
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
	edits     int // 每次改动加一：保存回来时据此判断保存期间有没有新改动
	quitAsked bool
	msg       string
	loaded    bool
	width     int
	height    int
	// Start 是新建项目时目录浏览的起点（deck settings 进程的当前目录，即 deck up 所在的目录，n055）
	Start string
	form  *projectForm // 不为 nil：正在新建项目
	hint  string       // 下一次保存成功后附在「保存完成」后面的提示（新建项目后的下一步）
	sub   *subView     // 不为 nil：正在编辑一个 agent、加入 agent、编辑某一档清单（n057）
	tools []ToolInfo   // 工具目录（打开「加入」或 agent 时读）
}

type loadedMsg struct {
	set       deckstore.Settings
	trust     []deckstore.TrustGrant
	err       error
	trustOnly bool // 只换授权列表，不动正在编辑的设置
	edits     int  // 发起重读时的改动计数：回来时又有改动，就不拿重读的设置盖掉它们
	force     bool // owner 按 R 重新读取：丢掉没保存的改动
}
type doneMsg struct {
	what  string
	err   error
	save  bool // 是保存（不是撤销授权）
	edits int  // 保存时的改动计数
}

func (m *Settings) Init() tea.Cmd { return m.load }

func (m *Settings) load() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, g, err := m.API.Load(ctx)
	return loadedMsg{set: s, trust: g, err: err}
}

// reloadAfterSave 保存成功后重读。记下发起时的改动计数：重读回来之前又改了，回来时不覆盖（n049 评审 2 B7）。
func (m *Settings) reloadAfterSave(edits int) tea.Cmd {
	return func() tea.Msg {
		msg := m.load().(loadedMsg)
		msg.edits = edits
		return msg
	}
}

// loadTrust 重读授权列表，保留没保存的编辑。
func (m *Settings) loadTrust() tea.Msg {
	msg := m.load().(loadedMsg)
	msg.trustOnly = true
	return msg
}

// touch 记一次编辑。
func (m *Settings) touch() {
	m.dirty = true
	m.edits++
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

// rows 是能移光标的行，与画面同序：项目（每个主会话）在上，全局可用的 agent 在下，最后是记住的授权。
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
	for _, a := range m.set.Agents {
		rs = append(rs, row{kind: rowGlobal, agent: a.ID})
	}
	rs = append(rs, row{kind: rowAddAgent})
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

// modelsFor 是项目某一档可选的模型：本项目勾选的 agent 里，属于这一档的模型（按勾选顺序、模型登记顺序）。
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
	return out
}

// ownerOf 是模型所属的 agent（全局清单里）。
func (m *Settings) ownerOf(model string) string {
	for _, a := range m.set.Agents {
		for _, md := range a.Models {
			if md.ID == model {
				return a.ID
			}
		}
	}
	return ""
}

func picked(p *deckstore.ProjectDef, id string) bool {
	for _, x := range p.Agents {
		if x == id {
			return true
		}
	}
	return false
}

// togglePick 勾选或取消一个 agent。取消时，各档清单里它的模型与主 agent 一并去掉（否则存不进去：deckd 会拒绝）。
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
		for tier, list := range p.Models {
			var rest deckstore.ModelList
			for _, model := range list {
				if m.ownerOf(model) != id {
					rest = append(rest, model)
				}
			}
			if len(rest) == 0 {
				delete(p.Models, tier)
			} else {
				p.Models[tier] = rest
			}
		}
		if p.MainAgent.Agent == id {
			p.MainAgent = deckstore.MainAgent{}
			if opts := m.mainOptions(p); len(opts) > 0 {
				p.MainAgent = opts[0]
			}
		}
	} else {
		p.Agents = append(p.Agents, id)
	}
	m.touch()
}

// mainOptions 是主 agent 能选的「agent · 档位」：这一档清单里有这个 agent 的模型（主 agent 按清单里它自己的模型起，n057）。
func (m *Settings) mainOptions(p *deckstore.ProjectDef) []deckstore.MainAgent {
	var opts []deckstore.MainAgent
	for _, id := range p.Agents {
		for i := len(deckstore.Tiers) - 1; i >= 0; i-- {
			t := deckstore.Tiers[i]
			for _, model := range p.Models[t] {
				if m.ownerOf(model) == id {
					opts = append(opts, deckstore.MainAgent{Agent: id, Tier: t})
					break
				}
			}
		}
	}
	return opts
}

// mainModel 是主 agent 起来时会用的模型（这一档清单里它自己的第一个；额度用尽时 deckd 会取它的下一个）。
func (m *Settings) mainModel(p *deckstore.ProjectDef) string {
	for _, model := range p.Models[p.MainAgent.Tier] {
		if m.ownerOf(model) == p.MainAgent.Agent {
			return model
		}
	}
	return ""
}

// cycleMain 让主 agent 在能选的「agent · 档位」之间轮换。
func (m *Settings) cycleMain() {
	p := m.project()
	opts := m.mainOptions(p)
	if len(opts) == 0 {
		m.msg = "先在某一档清单里放进一个勾选的 agent 的模型，主 agent 才有得选"
		return
	}
	next := opts[0]
	for i, o := range opts {
		if o == p.MainAgent {
			next = opts[(i+1)%len(opts)]
		}
	}
	p.MainAgent = next
	m.touch()
}

func (m *Settings) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case loadedMsg:
		if msg.err != nil {
			m.msg = "读不到设置：" + errText(msg.err)
			return m, nil
		}
		if msg.trustOnly {
			m.trust = msg.trust
			return m, nil
		}
		if !msg.force && msg.edits != m.edits {
			m.trust = msg.trust // 重读途中又改了：设置以界面上的为准，仍标为没保存
			m.msg = "保存完成（之后又有改动，还没保存）"
			return m, nil
		}
		m.set, m.trust, m.loaded, m.dirty = msg.set, msg.trust, true, false
		if msg.force {
			m.edits++
			m.sub, m.msg = nil, "已重新读取设置"
		}
		if m.proj >= len(m.set.Projects) {
			m.proj = 0
		}
	case toolsMsg, toolModelsMsg:
		return m, m.subMsg(msg)
	case doneMsg:
		if msg.err != nil {
			m.msg = msg.what + "失败：" + errText(msg.err)
			if msg.save {
				m.hint = ""
				if strings.Contains(msg.err.Error(), "被别处改过") {
					m.msg += "；按 R 重新读取（会丢掉这里没保存的改动）"
				}
			}
			return m, nil
		}
		m.msg = msg.what + "完成"
		if msg.save {
			m.msg += "：对之后起的会话生效，已在跑的会话不变"
		}
		if msg.save && m.hint != "" {
			m.msg += "；" + m.hint
			m.hint = ""
		}
		if msg.save && msg.edits == m.edits {
			return m, m.reloadAfterSave(m.edits)
		}
		if msg.save {
			m.msg += "（保存期间又有改动，还没保存）"
		}
		return m, m.loadTrust // 撤销授权不能丢掉没保存的编辑
	case tea.KeyMsg:
		if m.form != nil {
			return m, m.formKey(msg)
		}
		if m.sub != nil {
			return m, m.subKey(msg)
		}
		return m.key(msg.String())
	}
	return m, nil
}

// save 整份保存（在后台跑，继续编辑不能改到正在发出的那份）。
func (m *Settings) save() tea.Cmd {
	set, err := cloneSettings(m.set)
	if err != nil {
		m.msg = "保存失败：" + errText(err)
		return nil
	}
	edits := m.edits
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return doneMsg{what: "保存", err: m.API.Save(ctx, set), save: true, edits: edits}
	}
}

func (m *Settings) key(k string) (tea.Model, tea.Cmd) {
	if k != "q" && k != "esc" && k != "R" {
		m.quitAsked = false
	}
	rs := m.rows()
	m.cur = max(0, min(m.cur, len(rs)-1)) // 列表可能变短或为空
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
		m.cur = max(0, min(len(rs)-1, m.cur+1))
	case "left", "h":
		if len(m.set.Projects) > 0 {
			m.proj = (m.proj + len(m.set.Projects) - 1) % len(m.set.Projects)
		}
	case "right", "l":
		if len(m.set.Projects) > 0 {
			m.proj = (m.proj + 1) % len(m.set.Projects)
		}
	case " ", "enter":
		if !m.loaded || m.cur < 0 || m.cur >= len(rs) {
			break
		}
		switch r := rs[m.cur]; r.kind {
		case rowAgentPick:
			m.togglePick(r.agent)
		case rowTier:
			if k == "enter" {
				return m, m.openTier(r.tier)
			}
			m.cycleTier(r.tier)
		case rowMain:
			m.cycleMain()
		case rowGlobal:
			return m, m.openAgent(r.agent)
		case rowAddAgent:
			return m, m.openAdd()
		}
	case "a":
		if m.loaded {
			return m, m.openAdd()
		}
	case "d":
		if m.cur < len(rs) && rs[m.cur].kind == rowTrust {
			g := rs[m.cur].trust
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				return doneMsg{what: "撤销 " + g.Project + " 的 " + g.Tool + " 授权", err: m.API.Revoke(ctx, g.Project, g.Tool)}
			}
		}
	case "s":
		if m.loaded {
			return m, m.save()
		}
	case "R":
		if m.dirty && !m.quitAsked {
			m.quitAsked = true
			m.msg = "重新读取会丢掉没保存的改动：再按一次 R 确认"
			return m, nil
		}
		m.quitAsked = false
		return m, func() tea.Msg {
			msg := m.load().(loadedMsg)
			msg.force = true
			return msg
		}
	case "n":
		if !m.loaded {
			break
		}
		start := m.Start
		if start == "" {
			start, _ = os.Getwd()
		}
		m.form, m.msg = newProjectForm(start), ""
	}
	return m, nil
}

// cycleTier 是在项目区按空格的快捷操作：这一档清单只留一个模型，在可选模型之间轮换；最后一个之后是「不设」。
// 要多选、排顺序按 ⏎ 打开这一档。
func (m *Settings) cycleTier(tier deckstore.Tier) {
	p := m.project()
	opts := append(m.modelsFor(p, tier), "")
	cur := p.Models[tier].First()
	next := opts[0]
	for i, o := range opts {
		if o == cur {
			next = opts[(i+1)%len(opts)]
		}
	}
	if p.Models == nil {
		p.Models = map[deckstore.Tier]deckstore.ModelList{}
	}
	if next == "" {
		delete(p.Models, tier)
	} else {
		p.Models[tier] = deckstore.ModelList{next}
	}
	m.fixMain(p)
	m.touch()
}

// fixMain：清单变了以后主 agent 那一档没有它的模型了，就换到第一个能选的（没有就清空并提示）。
func (m *Settings) fixMain(p *deckstore.ProjectDef) {
	if p.MainAgent.Agent == "" || m.mainModel(p) != "" {
		return
	}
	old := p.MainAgent
	p.MainAgent = deckstore.MainAgent{}
	if opts := m.mainOptions(p); len(opts) > 0 {
		p.MainAgent = opts[0]
	}
	m.msg = fmt.Sprintf("%s · %s 这一档清单里没有 %s 的模型了：主 agent 改为 %s", old.Agent, old.Tier, old.Agent, mainText(p.MainAgent))
}

func mainText(ma deckstore.MainAgent) string {
	if ma.Agent == "" {
		return "（未选）"
	}
	return ma.Agent + " · " + string(ma.Tier)
}

const (
	dimOn  = "\x1b[38;5;242m"
	brand  = "\x1b[38;2;180;245;70m"
	bold   = "\x1b[1m"
	reset  = "\x1b[0m"
	green  = "\x1b[38;5;42m"
	orange = "\x1b[38;5;214m"
)

func (m *Settings) View() string {
	if m.form != nil {
		return m.formView()
	}
	if m.sub != nil {
		return m.subView()
	}
	var b strings.Builder
	b.WriteString(bold + brand + " agent 设置" + reset + dimOn + "   ↑↓ 选  ⏎ 打开/改  空格 勾选/轮换  ←→ 换项目  a 加 agent  n 新建项目  d 撤销授权  s 保存  q 关闭" + reset + "\n\n")
	if !m.loaded {
		b.WriteString("  " + m.msg + "\n")
		return b.String()
	}
	line := 0
	cursor := func() string {
		defer func() { line++ }()
		if line == m.cur {
			return brand + "›" + reset
		}
		return " "
	}
	if p := m.project(); p != nil {
		fmt.Fprintf(&b, bold+" 项目：%s"+reset+dimOn+"（%d/%d，←→ 换）"+reset+"\n", sanitize(p.Name), m.proj+1, len(m.set.Projects))
		for _, a := range m.set.Agents {
			box := "[ ]"
			if picked(p, a.ID) {
				box = green + "[x]" + reset
			}
			fmt.Fprintf(&b, " %s %s %s\n", cursor(), box, sanitize(a.Name))
		}
		for i := len(deckstore.Tiers) - 1; i >= 0; i-- {
			t := deckstore.Tiers[i]
			v := dimOn + "（不设）" + reset
			if list := p.Models[t]; len(list) > 0 {
				var parts []string
				for _, model := range list {
					parts = append(parts, sanitize(model))
				}
				v = strings.Join(parts, dimOn+" → "+reset)
			}
			fmt.Fprintf(&b, " %s %-9s %s\n", cursor(), string(t), v)
		}
		main := dimOn + "（未选）" + reset
		if p.MainAgent.Agent != "" {
			main = sanitize(p.MainAgent.Agent) + " · " + string(p.MainAgent.Tier)
			if model := m.mainModel(p); model != "" {
				main += dimOn + "（起来时用 " + sanitize(model) + "）" + reset
			} else {
				main += orange + "（这一档清单里没有它的模型：保存会被拒）" + reset
			}
		}
		fmt.Fprintf(&b, " %s 主 agent  %s\n", cursor(), main)
		b.WriteString(dimOn + "   勾选的 agent 的模型都能点名用；每档清单是只说档位时的挑选顺序，额度显示用尽的跳过、额度未知照常用。\n" +
			"   档位行按 ⏎ 多选、排顺序，按空格只留一个并轮换。" + reset + "\n")
	} else {
		b.WriteString(dimOn + " 还没有项目：按 n 新建（选一个目录）" + reset + "\n")
	}
	b.WriteString("\n")
	b.WriteString(bold + " 全局可用的 agent" + reset + dimOn + "（⏎ 打开：改模型与档位、移出）" + reset + "\n")
	if len(m.set.Agents) == 0 {
		b.WriteString(dimOn + "   还没有：按 a 从本机装了的 CLI 里加入" + reset + "\n")
	}
	for _, a := range m.set.Agents {
		mark, style := green+"●"+reset, ""
		note := ""
		if !m.installed(a) {
			mark, style, note = dimOn+"○"+reset, dimOn, "（没装）"
		}
		fmt.Fprintf(&b, " %s %s %s%-12s %-11s %s%s%s\n", cursor(), mark, style, sanitize(a.Name), sanitize(a.Tool), modelsSummary(a), note, reset)
	}
	fmt.Fprintf(&b, " %s %s\n", cursor(), dimOn+"+ 加入本机的 agent（a）"+reset)
	b.WriteString("\n" + bold + " 已记住的 Codex 授权" + reset + "\n")
	if len(m.trust) == 0 {
		b.WriteString(dimOn + "   （没有）" + reset + "\n")
	}
	for _, g := range m.trust {
		fmt.Fprintf(&b, " %s %s · %s · %s · %s\n", cursor(), sanitize(g.Project), sanitize(g.Tool), sanitize(g.Dir), g.ApprovedAt.Local().Format("01-02 15:04"))
	}
	if m.dirty {
		b.WriteString("\n" + orange + " 有没保存的改动（s 保存）" + reset)
	}
	if m.msg != "" {
		b.WriteString("\n " + oneLine(deckstore.Clean(m.msg, 240)))
	}
	return b.String()
}

// modelsSummary 是全局区一行里的模型摘要：档位首字母 + 模型 id，按档位从高到低。
func modelsSummary(a deckstore.AgentDef) string {
	var models []string
	for i := len(deckstore.Tiers) - 1; i >= 0; i-- {
		for _, md := range a.Models {
			if md.Tier == deckstore.Tiers[i] {
				models = append(models, string(md.Tier)[:1]+":"+sanitize(md.ID))
			}
		}
	}
	if len(models) == 0 {
		return dimOn + "（还没有模型：⏎ 打开后按 + 加）" + reset
	}
	return strings.Join(models, " ")
}

// RunSettings 跑设置界面（布局里按 F9 开的窗口，关掉就回到布局）。
func RunSettings(api SettingsAPI) error {
	start, _ := os.Getwd()
	_, err := tea.NewProgram(&Settings{API: api, Start: start}, tea.WithAltScreen()).Run()
	return err
}

// cloneSettings 深拷贝一份设置（slice 与 map 不再共享）。
func cloneSettings(s deckstore.Settings) (deckstore.Settings, error) {
	var out deckstore.Settings
	b, err := json.Marshal(s)
	if err == nil {
		err = json.Unmarshal(b, &out)
	}
	return out, err
}

// projectsUsing 列出勾选了某个 agent 的项目名。
func (m *Settings) projectsUsing(agent string) []string {
	var out []string
	for i := range m.set.Projects {
		if picked(&m.set.Projects[i], agent) {
			out = append(out, m.set.Projects[i].Name)
		}
	}
	sort.Strings(out)
	return out
}

// projectsWithModel 列出档位清单里有某个模型的项目名。
func (m *Settings) projectsWithModel(model string) []string {
	var out []string
	for _, p := range m.set.Projects {
		for _, list := range p.Models {
			if list.Has(model) {
				out = append(out, p.Name)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}
