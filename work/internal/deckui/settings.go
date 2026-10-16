package deckui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// agent 设置界面（架构稿 §14.2，n049；n057 owner：「我没看到哪里可以配置我全局可用的agent和模型，还有每个主会话可用的agent和模型」）：
//   - 「项目」页：每个项目（每个主会话）勾选可用的 agent，选主 agent，三档各一个有顺序的模型清单（可多选、可跨 agent）；
//   - 「全局 agent」页：从本机装了的 CLI 里加入、移出；每个 agent 的模型增删、改 id、标档位（⏎ 打开 agent）；
//   - 「授权」页：撤销已记住的 Codex 授权。
// n059（owner：「UIUE能不能再优化得更舒服一点。更简单点」）：分页（Tab 切换）、改完自动保存、档位用中文、
// 底部一行随光标变化的说明、光标行整行高亮；界面文字都从文字表取（settings_text.go）。
// 数据与校验在 deckd（n050），这里只编辑并整份 PUT 回去（带上读到的代数，别处改过就拒绝）。
// 启动额外参数（extra_args）、命令（binary）、agent 标记不在界面里改，只显示并说明去哪改。

// SettingsAPI 是设置界面对 deckd 的调用（测试可换）。
type SettingsAPI interface {
	Load(ctx context.Context) (deckstore.Settings, []deckstore.TrustGrant, error)
	Save(ctx context.Context, s deckstore.Settings) (int, error) // 返回存下的代数
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

func (a DeckSettingsAPI) Save(ctx context.Context, s deckstore.Settings) (int, error) {
	var out deckstore.Settings
	err := a.C.Do(ctx, "PUT", "/v1/settings", map[string]any{"agents": s.Agents, "projects": s.Projects, "generation": s.Generation}, &out)
	return out.Generation, err
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

// page 是设置的一页（n059，owner 选「分页，Tab 切换」）：一页只做一件事。
type page int

const (
	pageProject page = iota // 这个项目用哪些 agent、主 agent、三档模型
	pageAgents              // 全局可用的 agent 与它们的模型
	pageTrust               // 已记住的 Codex 授权
	pageCount
)

var pageText = [pageCount]string{"page.project", "page.agents", "page.trust"}

// Settings 是设置界面的 Bubble Tea 模型。
type Settings struct {
	API       SettingsAPI
	Installed func(binary string) bool // 判断一个 agent 是否已装（默认查 PATH）
	set       deckstore.Settings
	trust     []deckstore.TrustGrant
	proj      int            // 正在编辑的项目
	modelReq  int            // 问 CLI 候选的次数：回来的结果只填进发出它的那个输入框（attempt 3 自查 2）
	page      page           // 当前页
	cur       int            // 当前页里光标所在的行（rows 下标）
	curs      [pageCount]int // 各页离开时的光标，回来时还在原处
	// 自动保存（n059，owner 选「改完自动保存」）：每改一处就整份保存；同一时间只有一次在路上，路上又改了就回来再存。
	dirty    bool        // 有改动还没存上
	edits    int         // 每次改动加一：保存回来时据此判断保存期间有没有新改动
	saving   bool        // 有一次保存在路上
	pending  bool        // 保存在路上时又改了
	conflict bool        // 别处改过（409）：不再自动保存，等 owner 按 R 重新读取
	saveErr  string      // 上一次保存没成的原因
	saved    bool        // 存成过（底部显示「已保存」）
	scr      scrollState // 画面滚动（n059 attempt 2）
	reloads  int         // 按 R 重读的次数：重读之前发出的保存回来时认得出（n059 自查）
	// 再按一次确认：q（有没存上的改动时关闭）、R（丢掉没存上的改动重读）、d（撤销授权）各自确认（n057 自查：不能共用）
	quitAsked   bool
	reloadAsked bool
	// revokeAsked：按过一次 d 要撤销的那一条（n059 自查：只记「按过」的话，列表刷新后第二次 d 会撤销另一条）
	revokeAsked *deckstore.TrustGrant
	// hintEdits：新建项目那一刻的改动计数；存上了含这次改动的保存才显示「去侧栏按 m」（n059 自查）
	hintEdits int
	msg       string
	loaded    bool
	width     int
	height    int
	// Start 是新建项目时目录浏览的起点（deck settings 进程的当前目录，即 deck up 所在的目录，n055）
	Start string
	form  *projectForm // 不为 nil：正在新建项目
	hint  string       // 下一次保存成功后显示的提示（新建项目后的下一步）
	sub   *subView     // 不为 nil：正在编辑一个 agent、加入 agent、编辑某一档清单（n057）
	tools []ToolInfo   // 工具目录（打开「加入」或 agent 时读）
}

type loadedMsg struct {
	set       deckstore.Settings
	trust     []deckstore.TrustGrant
	err       error
	trustOnly bool // 只换授权列表，不动正在编辑的设置
	force     bool // owner 按 R 重新读取：丢掉没存上的改动
}
type doneMsg struct {
	what    string
	err     error
	save    bool // 是保存（不是撤销授权）
	edits   int  // 保存时的改动计数
	gen     int  // 保存时带的代数
	saved   int  // 存下的代数（deckd 回的）
	reloads int  // 发出时的重读次数
}

func (m *Settings) Init() tea.Cmd { return m.load }

func (m *Settings) load() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, g, err := m.API.Load(ctx)
	return loadedMsg{set: s, trust: g, err: err}
}

// loadTrust 重读授权列表，保留界面上的设置。
func (m *Settings) loadTrust() tea.Msg {
	msg := m.load().(loadedMsg)
	msg.trustOnly = true
	return msg
}

// touch 记一次编辑（Update 看到改动计数变了就自动保存）。
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

// rows 是当前页能移光标的行，与画面同序。
func (m *Settings) rows() []row {
	var rs []row
	switch m.page {
	case pageProject:
		if p := m.project(); p != nil {
			for _, a := range m.set.Agents {
				rs = append(rs, row{kind: rowAgentPick, agent: a.ID})
			}
			rs = append(rs, row{kind: rowMain})
			for i := len(deckstore.Tiers) - 1; i >= 0; i-- {
				rs = append(rs, row{kind: rowTier, tier: deckstore.Tiers[i]})
			}
		}
	case pageAgents:
		for _, a := range m.set.Agents {
			rs = append(rs, row{kind: rowGlobal, agent: a.ID})
		}
		rs = append(rs, row{kind: rowAddAgent})
	case pageTrust:
		for _, g := range m.trust {
			rs = append(rs, row{kind: rowTrust, trust: g})
		}
	}
	return rs
}

// switchPage 换页（Tab、Shift+Tab、数字键）：各页的光标各自记着。
func (m *Settings) switchPage(to page) {
	m.curs[m.page] = m.cur
	m.page = (to%pageCount + pageCount) % pageCount
	m.cur = m.curs[m.page]
	m.msg = ""
}

func (m *Settings) agent(id string) (deckstore.AgentDef, bool) {
	for _, a := range m.set.Agents {
		if a.ID == id {
			return a, true
		}
	}
	return deckstore.AgentDef{}, false
}

// agentName 是 agent 的显示名（找不到就用 id）。
func (m *Settings) agentName(id string) string {
	if a, ok := m.agent(id); ok && a.Name != "" {
		return a.Name
	}
	return id
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
		m.msg = T("main.none")
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
			m.msg = T("load.fail", errLong(msg.err))
			return m, nil
		}
		if msg.trustOnly {
			m.trust = msg.trust
			m.revokeAsked = nil // 列表变了：要撤销哪一条重新确认
			return m, nil
		}
		m.set, m.trust, m.loaded, m.dirty = msg.set, msg.trust, true, false
		if msg.force {
			m.edits++   // 路上那次保存回来时不再当成「存上了现在的设置」
			m.reloads++ // 路上那次保存回来时认得出是重读之前发的
			m.sub, m.msg, m.hint = nil, T("reload.done"), ""
			m.conflict, m.saveErr, m.pending, m.saved = false, "", false, false
			m.revokeAsked = nil
		}
		if m.proj >= len(m.set.Projects) {
			m.proj = 0
		}
	case toolsMsg, toolModelsMsg:
		return m, m.subMsg(msg)
	case doneMsg:
		if msg.save {
			return m, m.saveDone(msg)
		}
		if msg.err != nil {
			m.msg = T("action.fail", msg.what, errLong(msg.err))
			return m, nil
		}
		m.msg = T("action.done", msg.what)
		return m, m.loadTrust
	case tea.KeyMsg:
		switch k := msg.String(); k {
		case "pgdown", "pgup":
			// 翻页：窗口脱开光标上下翻一屏，放不下的内容（很长的报错、很高的块）都翻得到（n059 attempt 2，评审 1 B2、B3）
			step := max(1, m.scr.show)
			if k == "pgup" {
				step = -step
			}
			if old := m.scr.off; (old < 0) != (old+step < 0) && old != 0 {
				m.scr.off = 0 // 翻过原来的位置时停在原位（attempt 3 自查：PgUp 到顶后再 PgDn 回不到光标与输入框所在的画面）
			} else {
				m.scr.off += step
			}
			m.cancelConfirms()
			return m, nil
		case "down", "j", "up", "k":
			m.scr.off = 0
			// 光标所在的块比一屏还高（例如一个 agent 的模型很多）：↑↓ 先在块里滚，滚到头再移光标（评审 1 B2）
			if m.form == nil && (m.sub == nil || m.sub.input == nil) {
				step := max(1, m.scr.show-1) // 一次翻大半屏（留一行接上），块里的内容几下就看完
				if (k == "down" || k == "j") && m.scr.blockOff < m.scr.blockMax {
					m.scr.blockOff = min(m.scr.blockOff+step, m.scr.blockMax)
					m.cancelConfirms()
					return m, nil
				}
				if (k == "up" || k == "k") && m.scr.blockOff > 0 {
					m.scr.blockOff = max(m.scr.blockOff-step, 0)
					m.cancelConfirms()
					return m, nil
				}
			}
			m.scr.blockOff = 0
		default:
			m.scr.off, m.scr.blockOff = 0, 0 // 换页、换项目、打开子视图等：从新光标所在块的顶上看起
		}
		before := m.edits
		var cmd tea.Cmd
		switch {
		case m.form != nil:
			cmd = m.formKey(msg)
		case m.sub != nil:
			cmd = m.subKey(msg)
		default:
			_, cmd = m.key(msg.String())
		}
		if m.edits != before {
			cmd = batchCmd(cmd, m.autosave())
		}
		return m, cmd
	}
	return m, nil
}

// cancelConfirms 取消「再按一次确认」：翻页与块内翻页像别的键一样，按了就不算确认（attempt 2 自查）。
func (m *Settings) cancelConfirms() {
	m.quitAsked, m.reloadAsked, m.revokeAsked = false, false, nil
	if m.sub != nil {
		m.sub.confirm = false
	}
}

func batchCmd(a, b tea.Cmd) tea.Cmd {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	}
	return tea.Batch(a, b)
}

// autosave 发起一次保存；已有一次在路上就记下，回来后再存（保存是整份的，存最新的那份即可）。
func (m *Settings) autosave() tea.Cmd {
	if !m.loaded || m.conflict {
		return nil
	}
	if m.saving {
		m.pending = true
		return nil
	}
	m.saving = true
	return m.save()
}

// save 整份保存（在后台跑，继续编辑不能改到正在发出的那份）。
func (m *Settings) save() tea.Cmd {
	set, err := cloneSettings(m.set)
	if err != nil {
		m.saving = false
		m.saveErr = errLong(err)
		return nil
	}
	edits, reloads := m.edits, m.reloads
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		saved, err := m.API.Save(ctx, set)
		return doneMsg{what: T("save"), err: err, save: true, edits: edits, gen: set.Generation, saved: saved, reloads: reloads}
	}
}

// saveDone 处理一次保存的结果：存上了就记下 deckd 回的代数；路上又改了就接着存；别处改过就停下等 R。
func (m *Settings) saveDone(msg doneMsg) tea.Cmd {
	m.saving = false
	if msg.reloads != m.reloads {
		// 重读之前发出的：失败（包括 409）不算数，不把刚读到的设置标成冲突。
		// 成功且重读读在它存上之前（读到的代数就是它带的）：画面是它之前的内容、deckd 已是它——
		// 跟上代数后把画面上的再存一次，两边一致（R 本来就是丢掉没存上的改动）（n059 自查 2）
		if msg.err == nil && m.set.Generation == msg.gen {
			m.set.Generation = msg.saved
			m.dirty, m.pending = true, false
			return m.autosave()
		}
		if m.pending {
			m.pending = false
			return m.autosave()
		}
		return nil
	}
	if msg.err != nil {
		var de *deckclient.Error
		if errors.As(msg.err, &de) && de.Status == http.StatusConflict {
			m.hint = "" // 要重读，新项目会丢：不再提示去侧栏；一时存不上的留着，重试存上后照样提示（n059 自查 2）
			m.conflict, m.pending = true, false
			m.saveErr = T("save.conflict")
			return nil
		}
		m.saveErr = errLong(msg.err)
		if m.pending && m.edits != msg.edits { // 之后的改动可能已经把它改对了
			m.pending = false
			return m.autosave()
		}
		m.pending = false
		return nil
	}
	m.saveErr, m.saved = "", true
	if m.set.Generation == msg.gen {
		m.set.Generation = msg.saved // 下一次保存带存下的代数（rev 127 起代数不是 +1，用 deckd 回的）
	}
	if m.hint != "" && msg.edits >= m.hintEdits {
		m.msg, m.hint = m.hint, ""
	}
	if msg.edits == m.edits {
		m.dirty, m.pending = false, false
		return nil
	}
	m.pending = false
	return m.autosave()
}

func (m *Settings) key(k string) (tea.Model, tea.Cmd) {
	if k != "q" && k != "esc" {
		m.quitAsked = false
	}
	if k != "R" {
		m.reloadAsked = false
	}
	if k != "d" {
		m.revokeAsked = nil
	}
	rs := m.rows()
	m.cur = max(0, min(m.cur, len(rs)-1)) // 列表可能变短或为空
	switch k {
	case "q", "esc", "ctrl+c":
		if (m.dirty || m.saving) && !m.quitAsked && k != "ctrl+c" {
			m.quitAsked = true
			why := m.saveErr
			if why == "" {
				why = T("quit.saving")
			}
			m.msg = T("quit.unsaved", why)
			return m, nil
		}
		return m, tea.Quit
	case "tab":
		m.switchPage(m.page + 1)
	case "shift+tab":
		m.switchPage(m.page - 1)
	case "1", "2", "3":
		m.switchPage(page(k[0] - '1'))
	case "up", "k":
		m.cur = max(0, m.cur-1)
	case "down", "j":
		m.cur = max(0, min(len(rs)-1, m.cur+1))
	case "left", "h":
		if m.page == pageProject && len(m.set.Projects) > 0 {
			m.proj = (m.proj + len(m.set.Projects) - 1) % len(m.set.Projects)
		}
	case "right", "l":
		if m.page == pageProject && len(m.set.Projects) > 0 {
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
			if k == "enter" || len(m.project().Models[r.tier]) > 1 {
				// 已经排好多个模型时，空格不把清单压成一个（n057 自查：容易误触），打开多选
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
		if m.page != pageTrust || m.cur >= len(rs) {
			break
		}
		g := rs[m.cur].trust
		if a := m.revokeAsked; a == nil || a.Project != g.Project || a.Tool != g.Tool {
			m.revokeAsked = &g
			m.msg = T("trust.confirm", g.Project, g.Tool)
			return m, nil
		}
		m.revokeAsked = nil
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return doneMsg{what: T("trust.revoke", g.Project, g.Tool), err: m.API.Revoke(ctx, g.Project, g.Tool)}
		}
	case "s":
		// 存不上（不是冲突，例如 deckd 一时连不上）时手动重试；平时改动自动保存，用不着它（n059 自查）
		if m.dirty && !m.saving && !m.conflict && m.saveErr != "" {
			return m, m.autosave()
		}
	case "R":
		if m.dirty && !m.reloadAsked {
			m.reloadAsked = true
			m.msg = T("reload.confirm")
			return m, nil
		}
		m.reloadAsked = false
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

// cycleTier 是在项目页按空格的快捷操作：这一档清单只留一个模型，在可选模型之间轮换；最后一个之后是「不设」。
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
	m.msg = T("main.moved", m.agentName(old.Agent), tierName(old.Tier), m.agentName(old.Agent), m.mainText(p.MainAgent))
}

// mainText 是「agent · 档位」的显示（未选时写明）。
func (m *Settings) mainText(ma deckstore.MainAgent) string {
	if ma.Agent == "" {
		return T("main.unset")
	}
	// 名字自己清理，档位接在后面：整串一起按 1024 字截时，长名字会把档位截掉（attempt 3 自查 2）
	return sanitizeFull(m.agentName(ma.Agent)) + " · " + tierName(ma.Tier)
}

const (
	dimOn  = "\x1b[38;5;242m"
	brand  = "\x1b[38;2;180;245;70m"
	bold   = "\x1b[1m"
	reset  = "\x1b[0m"
	green  = "\x1b[38;5;42m"
	orange = "\x1b[38;5;214m"
	// selBg 是光标所在行整行的底色（n059：以前只有一个小「›」，看不出选的是哪一行）
	selBg = "\x1b[48;5;237m"
	// tabOn 是当前页签：品牌青柠底、黑字
	tabOn = "\x1b[1;38;5;16;48;2;180;245;70m"
)

// fitWidth 是画面出口的兜底：哪一行比窗口宽就折下去，不让终端截掉（验收：80×24 及以上无截断；n059 自查）。
func (m *Settings) fitWidth(s string) string {
	if m.width <= 0 {
		return s
	}
	lim := max(1, m.width-1) // 留 1 列：行首空格加超长词时 ansi.Wrap 第一行会多 1 列（n059 自查 4）
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if ansi.StringWidth(l) <= m.width {
			out = append(out, l)
			continue
		}
		// ansi.Wrap 遇到某些长度、带连字符的长词会折出空行而把长词整个留在一行（评审 1 B1）：
		// 折完逐行再量，还超宽的按宽度硬折；折出来的空行不要（里面的颜色码接到下一行前面）
		var pieces []string
		for _, p := range strings.Split(ansi.Wrap(l, lim, ""), "\n") {
			if ansi.StringWidth(p) > lim {
				pieces = append(pieces, strings.Split(ansi.Hardwrap(p, lim, true), "\n")...)
			} else {
				pieces = append(pieces, p)
			}
		}
		// 只有空格的一段（ansi.Wrap 与 Hardwrap 都会折出来）不要；里面的颜色码接到下一段前面（attempt 2 自查：Hardwrap 的空行以前漏了）
		carry := ""
		for _, p := range pieces {
			if strings.TrimSpace(ansi.Strip(p)) == "" {
				carry += strings.Join(sgrRe.FindAllString(p, -1), "")
				continue
			}
			out = append(out, carry+p)
			carry = ""
		}
		if carry != "" && len(out) > 0 {
			out[len(out)-1] += carry
		}
	}
	return strings.Join(out, "\n")
}

// fitLines 把底部的几行各自按窗口宽折行。
func (m *Settings) fitLines(lines []string) []string {
	if m.width <= 0 {
		return lines
	}
	var out []string
	for _, l := range lines {
		out = append(out, strings.Split(m.fitWidth(l), "\n")...)
	}
	return out
}

// textCap 是报错、提示原样显示的长度上限：只防异常长的文字，平常的都显示全、折行（验收：80×24 及以上无截断）。
const textCap = 1000

// msgCap 是拼好的提示（文字表里的句子嵌着名字、报错）显示时的上限：里面的名字（≤1024 字）、报错（≤1000 字）拼之前已各自清理，
// 显示时只去控制字符，不再按 textCap 截（attempt 3 自查 2：嵌着长名字的拒绝原因、「列不出模型…：手填 id」后半句被截掉）。
const msgCap = 16384

// caretMark 是输入框的打字处：画面里写成 reset 后跟「▏」，fitHeightState 靠它让输入框始终在画面里。
const caretMark = reset + "▏"

// joinFull 把几个名字各自清理后再拼成一串（attempt 3 自查：整串按 1024 字截时，一个很长的名字会把后面的名字挤掉）。
func joinFull(xs []string, sep string) string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = sanitizeFull(x)
	}
	return strings.Join(out, sep)
}

// sgrRe 认出颜色等 SGR 转义码（折行时空白段里的颜色码要接到下一段）。
var sgrRe = regexp.MustCompile("\x1b\\[[0-9;:]*m")

// sanitizeFull 是设置界面显示 owner 的数据（名字、模型 id、目录、路径、清单）用的清理：与侧栏的 sanitize 一样去控制字符、打码，
// 但不按 80 个字截短，长的交给出口折行显示全（n059 自查 4：模型 id 可到 128 字、目录与清单更长，按 80 截就看不全）。
func sanitizeFull(s string) string { return oneLine(deckstore.Clean(strings.TrimSpace(s), 1024)) }

// errLong 把错误清洗成一行：与 errText 一样，但上限是 textCap 而不是 120（n059 自查 4：「先运行 deck daemon」这样的后半句被截掉）。
func errLong(err error) string { return oneLine(deckstore.Clean(err.Error(), textCap)) }

// wrap 把一行按窗口宽度折成几行（留出行首 lead 格）：在空格处折，单个词比一行还宽就硬折；不截短。
func (m *Settings) wrap(s string, lead int) []string {
	if m.width <= lead+1 {
		return []string{s}
	}
	lim := m.width - lead - 1
	var out []string
	for _, p := range strings.Split(ansi.Wrap(s, lim, ""), "\n") {
		// 与出口的 fitWidth 一样：Wrap 遇到某些带连字符的长词会折出超宽的一段，逐段再量、超宽的硬折
		// （attempt 3 自查 2：选中候选的那段超宽，到出口再折出的续行没有底色，选中块认不全）
		if ansi.StringWidth(p) > lim {
			out = append(out, strings.Split(ansi.Hardwrap(p, lim, true), "\n")...)
		} else {
			out = append(out, p)
		}
	}
	return out
}

// note 是一段灰色说明：按窗口宽度折行（子视图底部的长说明，以前超宽就被截掉），每行缩进两格。
func (m *Settings) note(s string) string {
	if m.width > 4 {
		s = ansi.Wrap(s, m.width-3, "")
	}
	return dimOn + "  " + strings.ReplaceAll(s, "\n", "\n  ") + reset + "\n"
}

// tierName 是档位的中文名（n059，owner：「先中文」）：文件、命令行 --tier 与 deckd 接口仍用英文。
func tierName(t deckstore.Tier) string { return T("tier." + string(t)) }

// tierColor 是档位的颜色：三档一眼分得开。
func tierColor(t deckstore.Tier) string {
	switch t {
	case deckstore.Frontier:
		return "\x1b[38;5;213m"
	case deckstore.Balanced:
		return "\x1b[38;5;81m"
	}
	return "\x1b[38;5;150m"
}

// tierLabel 是带颜色的档位名。
func tierLabel(t deckstore.Tier) string { return tierColor(t) + tierName(t) + reset }

// hl 给光标所在行整行加底色（行内的 reset 之后补回底色，补满到窗口宽）。
func (m *Settings) hl(s string) string {
	s = selBg + strings.ReplaceAll(s, reset, reset+selBg)
	if m.width > 0 {
		if w := ansi.StringWidth(s); w < m.width {
			s += strings.Repeat(" ", m.width-w)
		}
	}
	return s + reset
}

// tabBar 是顶部：标题与三个页签，下面一条分隔线和一个空行（fitHeight 把空行以上当作固定的标题）。
func (m *Settings) tabBar() string {
	var tabs []string
	for i, key := range pageText {
		name := " " + T(key) + " "
		if page(i) == pageTrust && len(m.trust) > 0 {
			name = " " + T(key) + fmt.Sprintf(" %d ", len(m.trust))
		}
		if page(i) == m.page {
			tabs = append(tabs, tabOn+name+reset)
		} else {
			tabs = append(tabs, dimOn+name+reset)
		}
	}
	w := 64
	if m.width > 0 {
		w = min(m.width, 96)
	}
	return bold + brand + " " + T("title") + reset + "   " + strings.Join(tabs, " ") + "\n" +
		dimOn + strings.Repeat("─", w) + reset + "\n\n"
}

func (m *Settings) View() string {
	// 三个出口（新建项目表单、子视图、页面）都是：正文与底部各自按窗口宽折行，再按窗口高滚动（n059 自查 3：
	// 一处一处折总有漏的——底部的输入框、表单的报错、折成多行的长目录——所以在出口统一兜底）
	if m.form != nil {
		return m.fitScreen(m.fitWidth(m.formView()), m.fitLines(m.formFoot()), 1)
	}
	if m.sub != nil {
		body, foot := m.subView() // 模型、候选多时也看得见光标行、顶部按键提示与底部的输入框和提示（n057 评审 1、attempt 2 自查）
		return m.fitScreen(m.fitWidth(body), m.fitLines(foot), 1)
	}
	var b strings.Builder
	b.WriteString(m.tabBar())
	if !m.loaded {
		b.WriteString("  " + m.msg + "\n")
		return m.fitScreen(m.fitWidth(b.String()), nil, 1) // 读不到设置的报错也折行（n059 自查 4）
	}
	line, lastSel := 0, false
	// row 画一行能移光标的行：光标所在行前面是「›」并整行加底色
	row := func(s string) string {
		defer func() { line++ }()
		lastSel = line == m.cur
		if lastSel {
			return m.hl(" " + brand + "›" + reset + " " + s)
		}
		return "   " + s
	}
	// cont 画上一行折下来的续行：光标在那一行时续行也加底色（n059 自查）
	cont := func(s string) string {
		if lastSel {
			return m.hl(s)
		}
		return s
	}
	switch m.page {
	case pageProject:
		m.projectPage(&b, row, cont)
	case pageAgents:
		m.agentsPage(&b, row, cont)
	case pageTrust:
		m.trustPage(&b, row, cont)
	}
	keep := 1
	if m.page == pageProject && m.project() != nil {
		keep = 2 // 项目页：「项目 ‹ 名字 ›」一行也固定在顶部，滚动时看得见在改哪个项目（n059 自查 4）
	}
	return m.fitScreen(m.fitWidth(b.String()), m.fitLines(m.foot()), keep)
}

func (m *Settings) projectPage(b *strings.Builder, row, cont func(string) string) {
	p := m.project()
	if p == nil {
		b.WriteString(dimOn + "   " + T("project.empty") + reset + "\n")
		return
	}
	fmt.Fprintf(b, " %s  %s‹ %s ›%s  %s\n\n", bold+T("project.label")+reset, bold+brand, sanitizeFull(p.Name), reset,
		dimOn+fmt.Sprintf("%d/%d", m.proj+1, len(m.set.Projects))+reset)
	b.WriteString(" " + bold + T("project.agents") + reset + "\n")
	if len(m.set.Agents) == 0 {
		b.WriteString(dimOn + "   " + T("project.noagents") + reset + "\n")
	}
	for _, a := range m.set.Agents {
		box := dimOn + "·" + reset
		if picked(p, a.ID) {
			box = green + "✓" + reset
		}
		note := ""
		if !m.installed(a) {
			note = dimOn + "  " + T("agent.missing") + reset
		}
		b.WriteString(row(box+" "+sanitizeFull(a.Name)+note) + "\n")
	}
	b.WriteString("\n")
	main := dimOn + T("main.unset") + reset
	if p.MainAgent.Agent != "" {
		main = m.mainText(p.MainAgent)
		if model := m.mainModel(p); model != "" {
			main += dimOn + "  " + T("main.uses", sanitizeFull(model)) + reset
		} else {
			main += orange + "  " + T("main.nomodel") + reset
		}
	}
	b.WriteString(row(bold+T("main.label")+reset+"  "+main) + "\n\n")
	b.WriteString(" " + bold + T("project.tiers") + reset + "\n")
	for i := len(deckstore.Tiers) - 1; i >= 0; i-- {
		t := deckstore.Tiers[i]
		toks := []string{dimOn + T("tier.unset") + reset}
		if list := p.Models[t]; len(list) > 0 {
			toks = nil
			for i, model := range list {
				if i > 0 {
					toks = append(toks, dimOn+" → "+reset+sanitizeFull(model))
				} else {
					toks = append(toks, sanitizeFull(model))
				}
			}
		}
		lead := tierLabel(t) + "  "
		width := 0
		if m.width > 0 {
			width = m.width - 3 - ansi.StringWidth(lead)
		}
		lines := wrapTokens(toks, width) // 多选的模型多时折行，不截掉（n059 自查）
		b.WriteString(row(lead+lines[0]) + "\n")
		for _, l := range lines[1:] {
			b.WriteString(cont(strings.Repeat(" ", 3+ansi.StringWidth(lead))+l) + "\n")
		}
	}
}

// agentsPage：每个全局 agent 一行，全部模型按档位分组、分色列出（owner 选「保持全部列出」），放不下就折行、不截断。
func (m *Settings) agentsPage(b *strings.Builder, row, cont func(string) string) {
	if len(m.set.Agents) == 0 {
		b.WriteString(dimOn + "   " + T("agents.empty") + reset + "\n")
	}
	const nameW = 14
	for _, a := range m.set.Agents {
		mark, style, note := green+"●"+reset, "", ""
		if !m.installed(a) {
			mark, style, note = dimOn+"○"+reset, dimOn, "  "+T("agent.missing")
		}
		name := sanitizeFull(a.Name)
		if pad := nameW - ansi.StringWidth(name); pad > 0 {
			name += strings.Repeat(" ", pad)
		}
		lead := mark + " " + style + name + reset + " "
		width := 0
		if m.width > 0 {
			width = m.width - 3 - ansi.StringWidth(lead) // 行首「 › 」或三个空格
		}
		toks := modelTokens(a)
		if note != "" {
			toks = append(toks, dimOn+note+reset) // 「（没装）」也算进宽度（n059 自查：以前接在折完的行后面，窄窗口被截掉）
		}
		lines := wrapTokens(toks, width)
		b.WriteString(row(lead+lines[0]) + "\n")
		indent := strings.Repeat(" ", 3+ansi.StringWidth(lead))
		for _, l := range lines[1:] {
			b.WriteString(cont(indent+l) + "\n")
		}
	}
	b.WriteString(row(dimOn+"+ "+T("agents.add")+reset) + "\n")
}

// modelTokens 把一个 agent 的模型按档位从高到低分组：每组第一个带档位名，组内用「、」接，组间空三格。折行只在这些地方断。
func modelTokens(a deckstore.AgentDef) []string {
	var toks []string
	for i := len(deckstore.Tiers) - 1; i >= 0; i-- {
		t := deckstore.Tiers[i]
		first := true
		for _, md := range a.Models {
			if md.Tier != t {
				continue
			}
			if first {
				sep := ""
				if len(toks) > 0 {
					sep = "   "
				}
				toks = append(toks, sep+tierLabel(t)+" "+sanitizeFull(md.ID))
				first = false
			} else {
				toks = append(toks, T("sep.list")+sanitizeFull(md.ID))
			}
		}
	}
	if len(toks) == 0 {
		toks = append(toks, dimOn+T("agents.nomodels")+reset)
	}
	return toks
}

// wrapTokens 把 token 接成行，一行放不下就换行（width<=0 不折）；换行时去掉 token 开头的分隔；
// 单个 token 比一整行还宽就按宽度硬折（n059 自查：很长的模型 id 在窄窗口被截掉）。
func wrapTokens(toks []string, width int) []string {
	lines := []string{""}
	for _, t := range toks {
		cur := lines[len(lines)-1]
		if width > 0 && cur != "" && ansi.StringWidth(cur)+ansi.StringWidth(t) > width {
			t = strings.TrimLeft(strings.TrimPrefix(t, T("sep.list")), " ")
			lines = append(lines, "")
			cur = ""
		}
		if width > 0 && cur == "" && ansi.StringWidth(t) > width {
			parts := strings.Split(ansi.Hardwrap(t, width, true), "\n")
			lines[len(lines)-1] = parts[0]
			lines = append(lines, parts[1:]...)
			continue
		}
		lines[len(lines)-1] = cur + t
	}
	return lines
}

func (m *Settings) trustPage(b *strings.Builder, row, cont func(string) string) {
	b.WriteString(" " + bold + T("trust.title") + reset + "\n" + m.note(T("trust.about")) + "\n")
	if len(m.trust) == 0 {
		b.WriteString(dimOn + "   " + T("trust.empty") + reset + "\n")
	}
	for _, g := range m.trust {
		// 太长时折行、不截短（n059 自查：80 列下很长的目录被截掉，验收要求 80×24 及以上无截断）
		lines := m.wrap(fmt.Sprintf("%s · %s · %s · %s", g.ApprovedAt.Local().Format("01-02 15:04"), sanitizeFull(g.Project),
			sanitizeFull(g.Tool), sanitizeFull(g.Dir)), 3)
		b.WriteString(row(lines[0]) + "\n")
		for _, l := range lines[1:] {
			b.WriteString(cont("   "+l) + "\n")
		}
	}
}

// foot 是底部：保存状态、上一个动作的结果、随光标变化的一行说明、这一页能按的键。窗口矮时先省按键行、再省说明行。
func (m *Settings) foot() []string {
	var out []string
	switch {
	case m.conflict:
		out = append(out, orange+" "+m.saveErr+reset)
	case m.saving || (m.dirty && m.saveErr == ""):
		out = append(out, dimOn+" "+T("status.saving")+reset)
	case m.saveErr != "":
		out = append(out, orange+" "+T("status.failed", oneLine(deckstore.Clean(m.saveErr, msgCap)))+reset)
	case m.saved:
		out = append(out, green+" "+T("status.saved")+reset)
	}
	if m.msg != "" {
		out = append(out, " "+oneLine(deckstore.Clean(m.msg, msgCap)))
	}
	if m.height <= 0 || m.height >= 13 {
		if h := m.rowHelp(); h != "" {
			out = append(out, dimOn+" "+h+reset)
		}
	}
	if m.height <= 0 || m.height >= 16 {
		for _, l := range wrapKeys(m.pageKeys(), m.width) {
			out = append(out, dimOn+" "+l+reset)
		}
	}
	if m.width <= 0 {
		return out
	}
	var wrapped []string // 窄窗口：提示折行，不截掉（按键行已按组折过）
	for _, l := range out {
		wrapped = append(wrapped, strings.Split(ansi.Wrap(l, m.width-1, ""), "\n")...)
	}
	return wrapped
}

// rowHelp 是光标所在行的一行说明（n059：代替以前固定的大段灰字）。
func (m *Settings) rowHelp() string {
	rs := m.rows()
	if len(rs) == 0 {
		switch m.page {
		case pageProject:
			return T("help.noproject")
		case pageAgents:
			return T("help.add")
		}
		return ""
	}
	switch r := rs[max(0, min(m.cur, len(rs)-1))]; r.kind {
	case rowAgentPick:
		return T("help.pick")
	case rowMain:
		return T("help.main")
	case rowTier:
		return T("help.tier")
	case rowGlobal:
		return T("help.agent")
	case rowAddAgent:
		return T("help.add")
	case rowTrust:
		return T("help.trust")
	}
	return ""
}

// pageKeys 是这一页能按的键（两个空格分组，窄窗口按组折行）。
func (m *Settings) pageKeys() string {
	switch m.page {
	case pageProject:
		return T("keys.project")
	case pageAgents:
		return T("keys.agents")
	}
	return T("keys.trust")
}

// wrapKeys 把按键提示按「一组按键」（两个空格分隔）折成放得下的几行（n057 attempt 2 自查：窄窗口「esc 返回」被截掉）。
func wrapKeys(keys string, width int) []string {
	if width <= 0 || ansi.StringWidth(keys)+1 <= width {
		return []string{keys}
	}
	var lines []string
	cur := ""
	for _, k := range strings.Split(keys, "  ") {
		if cur != "" && ansi.StringWidth(cur)+2+ansi.StringWidth(k) > width-1 {
			lines, cur = append(lines, cur), ""
		}
		if cur != "" {
			cur += "  "
		}
		cur += k
	}
	return append(lines, cur)
}

// fitHeight 让画面放得进窗口（n057 自查：矮窗口时顶部按键提示与光标行被裁掉）：顶部标题（到第一个空行为止）与底部提示固定，
// 中间按光标所在行（「›」）滚动，上下还有内容时写明。
func fitHeight(body string, foot []string, height int) string {
	var st scrollState
	return fitHeightState(body, foot, height, 1, &st)
}

// scrollState 是画面滚动的状态（n059 attempt 2，评审 1 B2、B3）。blockOff：光标所在的块比一屏还高时块内往下滚了几行
// （↑↓ 先在块内滚，滚到头再移光标）；off：PgUp/PgDn 叠加的行数（按别的键清零）；show、blockMax：上一次画面量出来的，按键时用。
type scrollState struct{ blockOff, blockMax, off, show int }

// pinCap 是固定在顶部的标题最多占几行（评审 2 B1：没有上限时，名字长到几百字就把画面撑出窗口）。
func pinCap(height int) int { return max(1, height/3) }

// fitScreen 按窗口高画出来，滚动状态记在 m.scr。
func (m *Settings) fitScreen(body string, foot []string, keep int) string {
	return fitHeightState(body, foot, m.height, keep, &m.scr)
}

// fitHeightState 同 fitHeight：固定的标题到第 keep 个空行为止；底部固定不下时（例如很长的报错）底部也一起滚，
// 默认从底部第一行（输入框或保存状态）看起，PgUp/PgDn 翻看其余（评审 1 B3）。
func fitHeightState(body string, foot []string, height, keep int, st *scrollState) string {
	st.blockMax, st.show = 0, 0
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(foot) > 0 {
		foot = append([]string{""}, foot...)
	}
	if height <= 0 || len(lines)+len(foot) <= height {
		st.off, st.blockOff = 0, 0
		return strings.Join(append(lines, foot...), "\n")
	}
	head := 0
	for blanks := 0; head < len(lines); head++ {
		if lines[head] == "" {
			if blanks++; blanks == keep {
				break
			}
		}
	}
	// 固定的标题最多占一屏的三分之一，多出来的（几百字的项目名、agent 名折成的行）接进正文一起滚（评审 2 B1）
	head = min(head+1, len(lines), pinCap(height))
	rest, pinned := lines[head:], foot
	bodyLen := len(rest)
	if head+len(foot)+3 > height {
		// 底部固定不下：底部接在正文后面一起滚（评审 1 B3：以前强留 3 行正文，总高超出窗口）；PgUp/PgDn 翻看
		rest = append(append([]string{}, rest...), foot...)
		pinned = nil
	}
	// 光标所在的整块：「›」行与它折下来、同样带底色的续行（n059 自查 5）。底部接进来时也跟着光标走，
	// 不把底部当成光标块（attempt 2 自查：以前 ↑↓ 选行时看不见光标行）；没有光标（输入框打开时）就从底部第一行看起
	at, blockEnd, cursor, cand, caret := 0, 0, false, -1, -1
	for i := 0; i < bodyLen; i++ {
		// 认「品牌色 + ›」：数据（模型 id、项目名）经 Clean 去掉了转义码，里面的「›」不会被当成光标（attempt 2 自查）
		if strings.Contains(rest[i], brand+"›") {
			at, cursor = i, true
		}
	}
	if !cursor {
		// 打字处「reset+▏」（输入框、新建项目的项目 id）：数据里的「▏」前面没有转义码，不会认错
		for i := range rest {
			if strings.Contains(rest[i], caretMark) {
				caret = i
				break
			}
		}
	}
	if cursor {
		blockEnd = at
		for blockEnd+1 < bodyLen && strings.Contains(rest[blockEnd+1], selBg) {
			blockEnd++
		}
	} else if caret >= 0 && caret < bodyLen {
		// 输入框在正文里（新建项目确认页的项目 id）：窗口跟着打字处（attempt 3 自查：agent 名很长时打开就滚到底，看不见输入框）
		at, blockEnd = caret, caret
	} else if pinned == nil {
		at = min(bodyLen+1, len(rest)-1)
		blockEnd = at
		// 输入框下面选中的候选（同样是「品牌色 + ›」）也要在窗口里：从输入框看起，放不下就让选中的候选落在窗口底（评审 2 已知问题 1）
		for i := at; i < len(rest); i++ {
			if strings.Contains(rest[i], brand+"›") {
				cand = i
			}
		}
	}
	show := max(1, height-head-len(pinned)-2) // 留两行写「上面 / 下面还有几行」
	st.show = show
	start := at - show/2
	if !cursor && pinned == nil {
		start = at // 输入框（或底部第一行）放在窗口顶上，下面接着是说明与报错
	}
	if blockEnd-start+1 > show {
		start = blockEnd - show + 1
	}
	start = min(start, at)
	sticky := false
	if n := blockEnd - at + 1; n > show {
		// 块比一屏还高：从块顶起，↑↓ 在块内翻（评审 1 B2）；翻过之后光标行固定在窗口顶上，看得见在哪一行（attempt 2 自查）
		st.blockMax = n - show
		st.blockOff = max(0, min(st.blockOff, st.blockMax))
		start = at + st.blockOff
		sticky = st.blockOff > 0
	} else {
		st.blockOff = 0
	}
	pin := 1 // sticky 时固定在窗口顶的行数：光标行 1 行；输入框从第一行到打字处整块
	if !cursor && pinned == nil && caret >= at {
		// 输入框里打得很长、折成多行时，从输入框看起放不下就让打字处（「▏」）落在窗口底（attempt 3 自查）
		if caret-start+1 > show {
			start = caret - show + 1
		}
		pin = min(caret-at+1, show-1)
	}
	if cand >= 0 && pin >= 1 {
		// 选中的候选连同它折下来、同样带底色的续行落在窗口底，输入框整块固定在窗口顶（合同：输入框始终可见；
		// attempt 3 自查：以前按「续行不以空格开头」认，没选中的长候选折行后也被算进来，把输入框挤掉）
		candEnd := cand
		for candEnd+1 < len(rest) && candEnd+1-cand < show-pin && strings.Contains(rest[candEnd+1], selBg) {
			candEnd++
		}
		if candEnd-start+1 > show {
			start, sticky = candEnd-show+1, true
		}
	}
	maxStart := max(0, len(rest)-show)
	base := max(0, min(start, maxStart))
	start = max(0, min(base+st.off, maxStart)) // PgUp/PgDn
	st.off = start - base
	end := min(len(rest), start+show)
	window := rest[start:end]
	if sticky && show > pin && st.off == 0 && start > at && start+pin <= end { // 只剩 1 行时不固定，否则块里一行也看不到（attempt 2 自查）
		window = append(append([]string{}, rest[at:at+pin]...), rest[start+pin:end]...)
	}
	out := append([]string{}, lines[:head]...)
	out = append(out, dimOn+"   "+T("scroll.up", start)+reset)
	out = append(out, window...)
	out = append(out, dimOn+"   "+T("scroll.down", len(rest)-end)+reset)
	return strings.Join(append(out, pinned...), "\n")
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
