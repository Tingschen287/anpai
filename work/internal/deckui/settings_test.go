package deckui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

type fakeSettingsAPI struct {
	set     deckstore.Settings
	trust   []deckstore.TrustGrant
	saved   []deckstore.Settings
	revoked []string
	tools   []ToolInfo
	models  map[string][]string
	fail    error // 不为 nil：保存返回这个错误（n059：测自动保存失败、冲突）
	// modelsErr 不为 nil：问 CLI 有哪些模型时返回这个错误（n059 attempt 3）
	modelsErr error
}

func (f *fakeSettingsAPI) Load(context.Context) (deckstore.Settings, []deckstore.TrustGrant, error) {
	set, err := cloneSettings(f.set) // 与 deckd 一样给一份新的：界面上的改动不能改到「deckd 那边」
	return set, f.trust, err
}
func (f *fakeSettingsAPI) Save(_ context.Context, s deckstore.Settings) (int, error) {
	if f.fail != nil {
		return 0, f.fail
	}
	f.saved = append(f.saved, s)
	// 存下的代数不是 +1（n057 rev 127：deckd 按时间前推）：界面要用回来的这个
	return s.Generation + 1000, s.Validate() // 与 deckd 同一套校验
}
func (f *fakeSettingsAPI) Revoke(_ context.Context, project, tool string) error {
	f.revoked = append(f.revoked, project+"/"+tool)
	return nil
}
func (f *fakeSettingsAPI) Tools(context.Context) ([]ToolInfo, error) { return f.tools, nil }
func (f *fakeSettingsAPI) ToolModels(_ context.Context, tool string) ([]string, error) {
	return f.models[tool], f.modelsErr
}

func settingsFixture() (*Settings, *fakeSettingsAPI) {
	api := &fakeSettingsAPI{
		set: deckstore.Settings{
			Agents: deckstore.DefaultAgents(),
			Projects: []deckstore.ProjectDef{{ID: "deck", Name: "Agent Deck", Path: "/tmp/deck",
				Agents: []string{"claude"}, Models: map[deckstore.Tier]deckstore.ModelList{deckstore.Frontier: {"claude-opus-5-5"}},
				MainAgent: deckstore.MainAgent{Agent: "claude", Tier: deckstore.Frontier}, Envelope: deckstore.DefaultEnvelope("deck")}},
		},
		trust: []deckstore.TrustGrant{{Project: "deck", Tool: "codex", Dir: "/tmp/deck", ApprovedAt: time.Now()}},
	}
	m := &Settings{API: api, Installed: func(bin string) bool { return bin == "claude" }}
	m.Update(m.load())
	return m, api
}

func press(m *Settings, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "down", "up", "left", "right", "enter", "esc":
			msg = tea.KeyMsg{Type: map[string]tea.KeyType{"down": tea.KeyDown, "up": tea.KeyUp, "left": tea.KeyLeft, "right": tea.KeyRight, "enter": tea.KeyEnter, "esc": tea.KeyEsc}[k]}
		case " ":
			msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		_, cmd := m.Update(msg)
		run(m, cmd)
	}
}

// run 执行一个命令并把结果交回界面（Batch 拆开逐个执行；自动保存的命令也在这里同步跑完）。
func run(m *Settings, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	out := cmd()
	switch out := out.(type) {
	case nil, tea.QuitMsg:
	case tea.BatchMsg:
		for _, c := range out {
			run(m, c)
		}
	default:
		_, next := m.Update(out)
		run(m, next)
	}
}

// n059：分三页（项目、全局 agent、授权），Tab 切换；档位用中文；全局 agent 页列全部模型、没有命令名一列。
func TestSettingsViewShowsInstalledAndProject(t *testing.T) {
	m, _ := settingsFixture()
	v := ansi.Strip(m.View())
	for _, want := range []string{"设置", "项目", "全局 agent", "授权 1", "项目  ‹ Agent Deck ›", "用哪些 agent", "✓ Claude Code",
		"最强  claude-opus-5-5", "主 agent  Claude Code · 最强  起来时用 claude-opus-5-5", "Tab 换页"} {
		if !strings.Contains(v, want) {
			t.Fatalf("项目页缺 %q：\n%s", want, v)
		}
	}
	for _, bad := range []string{"frontier", "balanced", "economy", "s 保存", "有没保存的改动"} {
		if strings.Contains(v, bad) {
			t.Fatalf("项目页不该有 %q：\n%s", bad, v)
		}
	}
	press(m, "tab")
	v = ansi.Strip(m.View())
	for _, want := range []string{"Claude Code", "最强 claude-opus-5-5", "日常 claude-sonnet-5-5", "+ 加入本机的 agent"} {
		if !strings.Contains(v, want) {
			t.Fatalf("全局 agent 页缺 %q：\n%s", want, v)
		}
	}
	for _, l := range strings.Split(v, "\n") {
		if strings.Contains(l, "Claude Code") && strings.Contains(l, "没装") {
			t.Fatalf("装了的不应标没装：%q", l)
		}
		if strings.Contains(l, "Codex") && !strings.Contains(l, "没装") && !strings.HasPrefix(strings.TrimSpace(l), "最强") {
			t.Fatalf("没装的应标出：%q", l)
		}
		if strings.Contains(l, " claude  ") || strings.Contains(l, " codex  ") {
			t.Fatalf("不该有命令名一列：%q", l)
		}
	}
	press(m, "tab")
	if v = ansi.Strip(m.View()); !strings.Contains(v, "deck · codex · /tmp/deck") || !strings.Contains(v, "d 撤销") {
		t.Fatalf("授权页：\n%s", v)
	}
	press(m, "tab")
	if m.page != pageGeneral { // 段 3 返工：多了「通用」页，Tab 转一圈多一步
		t.Fatal("授权页之后是通用页")
	}
	press(m, "tab")
	if m.page != pageProject {
		t.Fatal("Tab 转一圈回到项目页")
	}
}

// 勾选与取消：取消某个 agent 时，引用它的档位模型与主 agent 一并清掉，保存的设置能通过 deckd 的校验。
func TestSettingsPickCycleAndSave(t *testing.T) {
	m, api := settingsFixture()
	rows := m.rows()
	idx := func(kind rowKind, agent string, tier deckstore.Tier) int {
		for i, r := range rows {
			if r.kind == kind && r.agent == agent && r.tier == tier {
				return i
			}
		}
		t.Fatalf("没有这一行")
		return -1
	}
	// 勾选 kimi，balanced 档轮换到 kimi 的模型
	m.cur = idx(rowAgentPick, "kimi", "")
	press(m, " ")
	if !picked(m.project(), "kimi") || m.dirty || len(api.saved) != 1 {
		t.Fatal("应勾上 kimi，并已自动保存")
	}
	m.cur = idx(rowTier, "", deckstore.Balanced)
	for i := 0; i < 5 && !strings.HasPrefix(m.project().Models[deckstore.Balanced].First(), "kimi"); i++ {
		press(m, " ") // 空格：只留一个并轮换（n057 起 ⏎ 打开多选）
	}
	if bal := m.project().Models[deckstore.Balanced].First(); !strings.HasPrefix(bal, "kimi") {
		t.Fatalf("balanced 档应能换成 kimi 的模型：%q", bal)
	}
	for i := 0; i < 5; i++ {
		if _, set := m.project().Models[deckstore.Balanced]; !set {
			break
		}
		press(m, " ")
	}
	if _, set := m.project().Models[deckstore.Balanced]; set {
		t.Fatal("轮换到最后应是「不设」")
	}
	for i := 0; i < 5 && !strings.HasPrefix(m.project().Models[deckstore.Balanced].First(), "kimi"); i++ {
		press(m, " ")
	}
	// 主 agent 换成 kimi
	m.cur = idx(rowMain, "", "")
	for i := 0; i < 6 && m.project().MainAgent.Agent != "kimi"; i++ {
		press(m, "enter")
	}
	if m.project().MainAgent.Agent != "kimi" {
		t.Fatal("主 agent 应能换到 kimi")
	}
	// 取消 kimi：它的 balanced 模型与主 agent 都要清掉
	m.cur = idx(rowAgentPick, "kimi", "")
	press(m, " ")
	if picked(m.project(), "kimi") || strings.HasPrefix(m.project().Models[deckstore.Balanced].First(), "kimi") || m.project().MainAgent.Agent == "kimi" {
		t.Fatalf("取消 kimi 后应清掉引用：%+v", *m.project())
	}
	// n059：每改一处就自动保存，最后存下的就是界面上的这份，能通过校验，底部显示已保存
	if len(api.saved) == 0 || m.dirty || !m.saveErr.empty() {
		t.Fatalf("应自动保存：%d 次，dirty=%v，%q", len(api.saved), m.dirty, m.saveErr.String())
	}
	last := api.saved[len(api.saved)-1]
	if last.Validate() != nil || last.Projects[0].MainAgent != m.project().MainAgent || len(last.Projects[0].Agents) != len(m.project().Agents) {
		t.Fatalf("最后存下的应是界面上的这份：%+v", last.Projects[0])
	}
	if !strings.Contains(ansi.Strip(m.View()), "✓ 已保存") {
		t.Fatalf("应显示已保存：\n%s", ansi.Strip(m.View()))
	}
}

func TestSettingsRevokeTrustAndQuitGuard(t *testing.T) {
	m, api := settingsFixture()
	press(m, "3") // 授权页
	press(m, "d")
	if len(api.revoked) != 0 || !strings.Contains(m.msg.String(), "再按一次 d") {
		t.Fatalf("第一次 d 只提醒（n059：撤销要确认）：%v %q", api.revoked, m.msg.String())
	}
	press(m, "d")
	if len(api.revoked) != 1 || api.revoked[0] != "deck/codex" {
		t.Fatalf("应撤销 deck 的 codex 授权：%v", api.revoked)
	}
	// 有改动还没存上时按 q：先提示，再按一次才关（保存还在路上）
	press(m, "1")
	for i, r := range m.rows() {
		if r.kind == rowAgentPick && r.agent == "codex" {
			m.cur = i
		}
	}
	m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}) // 自动保存的命令先不执行：保存在路上
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd != nil {
		t.Fatal("有没保存的改动时，第一次 q 不应关闭")
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("第二次 q 应关闭")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("应是退出")
	}
}
