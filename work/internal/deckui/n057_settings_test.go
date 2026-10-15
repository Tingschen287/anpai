package deckui

import (
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// n057：设置界面里加入本机的 agent、改它的模型与档位、移出；项目每档有序多选；主 agent 只能选清单里有它模型的档位。

func n057Fixture() (*Settings, *fakeSettingsAPI) {
	agents := []deckstore.AgentDef{}
	for _, a := range deckstore.DefaultAgents() {
		if a.ID == "claude" || a.ID == "codex" {
			agents = append(agents, a)
		}
	}
	api := &fakeSettingsAPI{
		set: deckstore.Settings{Generation: 7, Agents: agents, Projects: []deckstore.ProjectDef{{ID: "ai", Name: "ai", Path: "/tmp/ai",
			Agents: []string{"claude", "codex"}, Models: map[deckstore.Tier]deckstore.ModelList{deckstore.Frontier: {"claude-opus-5-5"}},
			MainAgent: deckstore.MainAgent{Agent: "claude", Tier: deckstore.Frontier}, Envelope: deckstore.DefaultEnvelope("ai")}}},
		tools: []ToolInfo{
			{Tool: "claude", Name: "Claude Code", Binary: "claude", Installed: true, Added: true},
			{Tool: "opencode", Name: "OpenCode", Binary: "opencode", Installed: true, Path: "/usr/bin/opencode", Version: "1.2", CanList: true,
				Models: []deckstore.ModelDef{{ID: "opencode/muse", Tier: deckstore.Frontier}}},
			{Tool: "grok", Name: "Grok Build", Binary: "grok", Agent: true, Installed: true, CanList: true,
				Models: []deckstore.ModelDef{{ID: "grok-4.7", Tier: deckstore.Frontier}, {ID: "grok-4.5", Tier: deckstore.Economy}}},
			{Tool: "gemini", Name: "Gemini CLI", Binary: "gemini"},
		},
		models: map[string][]string{"grok": {"grok-4.7", "grok-4.7-build-fast", "grok-4.5"}},
	}
	m := &Settings{API: api, Installed: func(string) bool { return true }}
	m.Update(m.load())
	return m, api
}

func rowIdx(t *testing.T, m *Settings, kind rowKind, agent string, tier deckstore.Tier) int {
	t.Helper()
	for i, r := range m.rows() {
		if r.kind == kind && r.agent == agent && r.tier == tier {
			return i
		}
	}
	t.Fatalf("没有这一行：%v %s %s", kind, agent, tier)
	return -1
}

func plainView(m *Settings) string { return ansi.Strip(m.View()) }

// goRow 切到这一行所在的页（n059 分页），光标放到这一行。
func goRow(t *testing.T, m *Settings, kind rowKind, agent string, tier deckstore.Tier) {
	t.Helper()
	to := pageProject
	switch kind {
	case rowGlobal, rowAddAgent:
		to = pageAgents
	case rowTrust:
		to = pageTrust
	}
	if m.page != to {
		m.switchPage(to)
	}
	m.cur = rowIdx(t, m, kind, agent, tier)
}

// 验收：加入本机的 grok、改它的模型（从 grok models 的候选里加、改档位、改 id、删），项目勾选、frontier 多选并排顺序，保存带代数。
func TestAddAgentEditModelsAndMultiSelect(t *testing.T) {
	m, api := n057Fixture()
	v := plainView(m)
	for _, want := range []string{"项目  ‹ ai ›", "用哪些 agent", "都能点名用", "主 agent  Claude Code · 最强  起来时用 claude-opus-5-5"} {
		if !strings.Contains(v, want) {
			t.Fatalf("项目页缺 %q：\n%s", want, v)
		}
	}
	// 加入：只列没加的，装了的在前，没装的标出且加不进
	press(m, "a")
	v = plainView(m)
	if !strings.Contains(v, "加入本机的 agent") || strings.Contains(v, "Claude Code") || !strings.Contains(v, "Gemini CLI") || !strings.Contains(v, "○ （没装）") {
		t.Fatalf("加入列表：\n%s", v)
	}
	press(m, "down", "down", "enter") // 第三项是没装的 gemini
	if len(m.set.Agents) != 2 || !strings.Contains(m.msg, "没装") {
		t.Fatalf("没装的不能加：%q", m.msg)
	}
	press(m, "up", "enter") // grok
	g, ok := m.agent("grok")
	if !ok || g.Binary != "grok" || !g.Agent || len(g.Models) != 2 || m.dirty || m.sub == nil || m.sub.kind != subAgent { // n059：加入后已自动保存
		t.Fatalf("加入 grok：带默认模型、照目录的命令与 agent 标记，并打开它：%+v %v", g, m.sub)
	}
	// 加模型：候选来自 grok models，打字过滤
	press(m, "+", "b", "u", "i", "l", "d", "down", "enter")
	g, _ = m.agent("grok")
	if len(g.Models) != 3 || g.Models[2].ID != "grok-4.7-build-fast" {
		t.Fatalf("从候选里加 grok-4.7-build-fast：%+v（%s）", g.Models, plainView(m))
	}
	// 新模型沿用加之前光标所在模型的档位（grok-4.7 是 frontier）；光标在新模型上按 ⏎ 换档位（frontier → balanced）
	press(m, "enter")
	g, _ = m.agent("grok")
	if g.Models[2].Tier != deckstore.Balanced {
		t.Fatalf("⏎ 换档位：%+v", g.Models[2])
	}
	// 手填 id、空格不收
	press(m, "+", "b", "a", "d", " ", "i", "d", "enter")
	if g, _ = m.agent("grok"); len(g.Models) != 3 || m.sub.input == nil || !strings.Contains(m.sub.input.note, "空格") {
		t.Fatalf("带空格的 id 不收：%+v", m.sub.input)
	}
	press(m, "esc", "esc") // 关输入框、回主界面
	// 项目里勾选 grok，frontier 档 ⏎ 打开多选：放进 grok-4.7、再调到第一
	goRow(t, m, rowAgentPick, "grok", "")
	if !picked(m.project(), "grok") {
		press(m, " ")
	}
	goRow(t, m, rowTier, "", deckstore.Frontier)
	press(m, "enter")
	if m.sub == nil || m.sub.kind != subTier || !strings.Contains(plainView(m), "额度未知照常用") {
		t.Fatalf("应打开 frontier 档：\n%s", plainView(m))
	}
	opts := m.tierOptions(m.project(), deckstore.Frontier)
	pos := -1
	for i, o := range opts {
		if o == "grok-4.7" {
			pos = i
		}
	}
	for i := 0; i < pos; i++ {
		press(m, "down")
	}
	press(m, "enter", "K")
	if got := strings.Join(m.project().Models[deckstore.Frontier], ","); got != "grok-4.7,claude-opus-5-5" {
		t.Fatalf("放进并调到第一：%s", got)
	}
	press(m, "esc")
	if !strings.Contains(plainView(m), "最强  grok-4.7 → claude-opus-5-5") {
		t.Fatalf("项目页显示有顺序的清单：\n%s", plainView(m))
	}
	// n059：每改一处自动保存；第一次带读到的代数，之后带 deckd 回的；最后存下的能过校验
	if len(api.saved) < 2 || api.saved[0].Generation != 7 || api.saved[1].Generation != 1007 {
		t.Fatalf("自动保存带读到的代数、之后带存下的：%d 次", len(api.saved))
	}
	last := api.saved[len(api.saved)-1]
	if last.Validate() != nil || last.CheckMainAgents() != nil || m.dirty {
		t.Fatalf("最后存下的能过校验：%+v", last)
	}
	if !strings.Contains(plainView(m), "已保存：对之后起的会话生效") {
		t.Fatalf("保存后说明生效范围：\n%s", plainView(m))
	}
}

func TestRenameRemoveAndGuards(t *testing.T) {
	m, _ := n057Fixture()
	goRow(t, m, rowGlobal, "claude", "")
	press(m, "enter")
	// 项目在用的模型：不能删、不能改档位
	press(m, "x")
	if a, _ := m.agent("claude"); len(a.Models) != 4 || !strings.Contains(m.msg, "项目「ai」") {
		t.Fatalf("在用的模型不能删：%q", m.msg)
	}
	press(m, "enter")
	if a, _ := m.agent("claude"); a.Models[0].Tier != deckstore.Frontier {
		t.Fatal("在用的模型不能改档位")
	}
	// 改 id：项目清单里的一并改
	press(m, "e")
	for range len("claude-opus-5-5") {
		press(m, "backspace")
	}
	press(m, "o", "p", "u", "s", "-", "n", "e", "w", "enter")
	if a, _ := m.agent("claude"); a.Models[0].ID != "opus-new" || m.project().Models[deckstore.Frontier].First() != "opus-new" {
		t.Fatalf("改 id 连带项目：%+v %v", a.Models[0], m.project().Models)
	}
	// 移出：项目勾选着就不让
	press(m, "r")
	if _, ok := m.agent("claude"); !ok || !strings.Contains(m.msg, "先在这些项目里取消勾选") {
		t.Fatalf("项目在用的 agent 不能移出：%q", m.msg)
	}
	press(m, "esc")
	// 取消勾选 codex 后可以移出 codex
	goRow(t, m, rowAgentPick, "codex", "")
	press(m, " ")
	goRow(t, m, rowGlobal, "codex", "")
	press(m, "enter", "r")
	if _, ok := m.agent("codex"); ok || m.sub != nil {
		t.Fatalf("没人用的 agent 可以移出：%q", m.msg)
	}
	// 启动额外参数只读显示
	m.set.Agents[0].ExtraArgs = [][]string{{"--allowedTools", "Read"}}
	goRow(t, m, rowGlobal, "claude", "")
	press(m, "enter")
	if v := plainView(m); !strings.Contains(v, "启动额外参数（安全白名单，只读") || !strings.Contains(v, "--allowedTools Read") {
		t.Fatalf("extra_args 只显示、说明去哪改：\n%s", v)
	}
}

// 主 agent 只能选「这一档清单里有它的模型」的组合；清单变了让主 agent 落空时自动换并说明。
func TestMainAgentFollowsTierLists(t *testing.T) {
	m, _ := n057Fixture()
	p := m.project()
	p.Models[deckstore.Balanced] = deckstore.ModelList{"gpt-5.6-terra"}
	opts := m.mainOptions(p)
	if len(opts) != 2 || opts[0] != (deckstore.MainAgent{Agent: "claude", Tier: deckstore.Frontier}) || opts[1] != (deckstore.MainAgent{Agent: "codex", Tier: deckstore.Balanced}) {
		t.Fatalf("可选的主 agent：%v", opts)
	}
	goRow(t, m, rowMain, "", "")
	press(m, "enter")
	if p.MainAgent != (deckstore.MainAgent{Agent: "codex", Tier: deckstore.Balanced}) {
		t.Fatalf("轮换到 codex · balanced：%v", p.MainAgent)
	}
	// 把 balanced 清单清空：主 agent 落空，自动换到还能选的
	goRow(t, m, rowTier, "", deckstore.Balanced)
	press(m, "enter", "enter", "esc")
	if p.MainAgent != (deckstore.MainAgent{Agent: "claude", Tier: deckstore.Frontier}) || !strings.Contains(m.msg, "主 agent 改为") {
		t.Fatalf("主 agent 应自动换并说明：%v %q", p.MainAgent, m.msg)
	}
}

func TestEmptyStatesTellNextStep(t *testing.T) {
	api := &fakeSettingsAPI{set: deckstore.Settings{}}
	m := &Settings{API: api}
	m.Update(m.load())
	v := plainView(m)
	press(m, "tab")
	v += plainView(m)
	for _, want := range []string{"还没有项目：按 n 新建", "n 新建项目：选一个目录", "还没有：按 a 从本机装了的 CLI 里加入"} {
		if !strings.Contains(v, want) {
			t.Fatalf("空状态缺 %q：\n%s", want, v)
		}
	}
}

// 自查：保存途中又改了，回来后接着存、带上存下的代数；R 与 q 各自确认；排好的多选清单按空格不被压成一个；
// 晚到的别的 agent 的候选不填进来；移出带手写启动参数的 agent 要再按一次；矮窗口滚动、窄窗口标题折行。
func TestSettingsSelfCheckFixes(t *testing.T) {
	m, api := n057Fixture()
	goRow(t, m, rowAgentPick, "codex", "")
	_, save := m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}) // 取消勾选 codex：自动保存发出
	if save == nil || !m.saving {
		t.Fatal("改动后应发出保存")
	}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}); cmd != nil || !m.pending { // 保存还没回来又改了一处
		t.Fatal("保存在路上时不再发第二个，记下回来再存")
	}
	_, again := m.Update(save())
	if m.set.Generation != 1007 || len(api.saved) != 1 || api.saved[0].Generation != 7 || again == nil {
		t.Fatalf("保存回来后代数跟上、接着存：%d", m.set.Generation)
	}
	m.Update(again())
	if len(api.saved) != 2 || api.saved[1].Generation != 1007 || !picked(&api.saved[1].Projects[0], "codex") || m.dirty || m.saving {
		t.Fatalf("第二次存的是最新的那份（codex 又勾上了）：%d %v", len(api.saved), m.dirty)
	}
	// 保存没存上（有没存上的改动）时：R 再 q，q 仍要确认
	api.fail = fmt.Errorf("x")
	press(m, " ")
	api.fail = nil
	if !m.dirty || !strings.Contains(plainView(m), "没存上：x") {
		t.Fatalf("保存失败要显示原因：\n%s", plainView(m))
	}
	press(m, "R")
	if _, cmd := m.Update(keyMsg("q")); cmd != nil {
		t.Fatal("先按 R 再按 q：有没保存的改动时 q 仍要先提醒")
	}
	// 多选清单按空格：打开多选，不压成一个
	m.project().Models[deckstore.Frontier] = deckstore.ModelList{"claude-opus-5-5", "gpt-6.1-sol"}
	goRow(t, m, rowTier, "", deckstore.Frontier)
	press(m, " ")
	if m.sub == nil || m.sub.kind != subTier || len(m.project().Models[deckstore.Frontier]) != 2 {
		t.Fatalf("应打开多选、清单不变：%v %v", m.sub, m.project().Models)
	}
	press(m, "esc")
	// 晚到的候选
	goRow(t, m, rowGlobal, "codex", "")
	press(m, "enter")
	m.sub.input = &modelInput{cur: -1}
	m.Update(toolModelsMsg{tool: "grok", models: []string{"grok-4.7"}})
	if len(m.sub.input.all) != 0 {
		t.Fatal("别的工具的候选不能填进来")
	}
	press(m, "esc", "esc")
	// 移出带手写启动参数的 agent
	goRow(t, m, rowAgentPick, "codex", "")
	if picked(m.project(), "codex") {
		press(m, " ")
	}
	for i := range m.set.Agents {
		if m.set.Agents[i].ID == "codex" {
			m.set.Agents[i].ExtraArgs = [][]string{{"-c", "x=1"}}
		}
	}
	goRow(t, m, rowGlobal, "codex", "")
	press(m, "enter", "r")
	if _, ok := m.agent("codex"); !ok || !strings.Contains(m.msg, "再按一次 r") {
		t.Fatalf("第一次 r 只提醒：%q", m.msg)
	}
	press(m, "r")
	if _, ok := m.agent("codex"); ok {
		t.Fatal("第二次 r 移出")
	}
	// 矮窗口、窄窗口
	m2, _ := n057Fixture()
	m2.width, m2.height = 60, 14
	m2.cur = len(m2.rows()) - 1 // 最后一行（便宜档）
	v := plainView(m2)
	lines := strings.Split(v, "\n")
	if len(lines) > 14 || !strings.Contains(lines[0], "设置") || !strings.Contains(v, "便宜") || !strings.Contains(v, "上面还有") {
		t.Fatalf("矮窄窗口：页签在、光标行看得见：\n%s", v)
	}
	for _, l := range lines {
		if ansi.StringWidth(l) > 60 {
			t.Fatalf("太宽：%q", l)
		}
	}
}

func keyMsg(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// 评审 1 已知问题 1：模型多时 agent 编辑与档位多选也按窗口高度滚动，光标行与顶部按键提示看得见。
func TestSubViewsFitHeight(t *testing.T) {
	m, _ := n057Fixture()
	m.width, m.height = 120, 24
	for i := 0; i < 30; i++ {
		m.set.Agents[0].Models = append(m.set.Agents[0].Models, deckstore.ModelDef{ID: fmt.Sprintf("claude-extra-%02d", i), Tier: deckstore.Balanced})
	}
	goRow(t, m, rowGlobal, "claude", "")
	press(m, "enter")
	for i := 0; i < 20; i++ {
		press(m, "down")
	}
	check := func(what, mark string) {
		t.Helper()
		v := plainView(m)
		lines := strings.Split(v, "\n")
		if len(lines) > 24 || !strings.Contains(lines[0], "esc 返回") || !strings.Contains(v, mark) {
			t.Fatalf("%s：%d 行\n%s", what, len(lines), v)
		}
	}
	check("agent 编辑", "› claude-extra-16")
	press(m, "esc", "esc")
	m.project().Agents = []string{"claude"}
	goRow(t, m, rowTier, "", deckstore.Balanced)
	press(m, "enter")
	for i := 0; i < 25; i++ {
		press(m, "down")
	}
	check("档位多选", "›")
	press(m, "esc")
	// 模型很多时按 + 打开输入框：输入框与候选固定在底部，看得见；拒绝提示也看得见（attempt 2 自查）
	goRow(t, m, rowGlobal, "claude", "")
	press(m, "enter", "+", "x", "y")
	v := plainView(m)
	if !strings.Contains(v, "加模型：xy") || len(strings.Split(v, "\n")) > 24 {
		t.Fatalf("输入框应看得见：\n%s", v)
	}
	press(m, "esc", "x") // 光标在第一个模型（项目在用）：删不掉，提示要看得见
	if v := plainView(m); !strings.Contains(v, "先在项目里拿掉它") {
		t.Fatalf("拒绝提示应看得见：\n%s", v)
	}
	// 窄窗口：子视图标题的按键提示另起一行，「esc 返回」不被截
	m.width = 60
	for _, l := range strings.Split(plainView(m), "\n") {
		if ansi.StringWidth(l) > 60 && strings.Contains(l, "esc") {
			t.Fatalf("窄窗口标题行太宽：%q", l)
		}
	}
	if !strings.Contains(plainView(m), "esc 返回") {
		t.Fatal("窄窗口也要看得见 esc 返回")
	}
}
