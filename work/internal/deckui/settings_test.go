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
}

func (f *fakeSettingsAPI) Load(context.Context) (deckstore.Settings, []deckstore.TrustGrant, error) {
	return f.set, f.trust, nil
}
func (f *fakeSettingsAPI) Save(_ context.Context, s deckstore.Settings) error {
	f.saved = append(f.saved, s)
	return s.Validate() // 与 deckd 同一套校验
}
func (f *fakeSettingsAPI) Revoke(_ context.Context, project, tool string) error {
	f.revoked = append(f.revoked, project+"/"+tool)
	return nil
}

func settingsFixture() (*Settings, *fakeSettingsAPI) {
	api := &fakeSettingsAPI{
		set: deckstore.Settings{
			Agents: deckstore.DefaultAgents(),
			Projects: []deckstore.ProjectDef{{ID: "deck", Name: "Agent Deck", Path: "/tmp/deck",
				Agents: []string{"claude"}, Models: map[deckstore.Tier]string{deckstore.Frontier: "claude-opus-5-5"},
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
		if cmd != nil {
			if out := cmd(); out != nil {
				if _, isQuit := out.(tea.QuitMsg); !isQuit {
					m.Update(out)
				}
			}
		}
	}
}

func TestSettingsViewShowsInstalledAndProject(t *testing.T) {
	m, _ := settingsFixture()
	v := ansi.Strip(m.View())
	for _, want := range []string{"全局可用的 agent", "Claude Code", "（没装）", "项目：Agent Deck", "frontier  claude-opus-5-5", "主 agent  claude · frontier", "deck · codex · /tmp/deck"} {
		if !strings.Contains(v, want) {
			t.Fatalf("缺 %q：\n%s", want, v)
		}
	}
	for _, l := range strings.Split(v, "\n") {
		if strings.Contains(l, "Claude Code") && strings.Contains(l, "没装") {
			t.Fatalf("装了的不应标没装：%q", l)
		}
		if strings.Contains(l, "Codex") && strings.Contains(l, " codex ") && !strings.Contains(l, "没装") {
			t.Fatalf("没装的应标出：%q", l)
		}
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
	if !picked(m.project(), "kimi") || !m.dirty {
		t.Fatal("应勾上 kimi")
	}
	m.cur = idx(rowTier, "", deckstore.Balanced)
	for i := 0; i < 5 && !strings.HasPrefix(m.project().Models[deckstore.Balanced], "kimi"); i++ {
		press(m, "enter") // 选项含 claude 与 kimi 的 balanced 模型，按顺序轮换
	}
	if bal := m.project().Models[deckstore.Balanced]; !strings.HasPrefix(bal, "kimi") {
		t.Fatalf("balanced 档应能换成 kimi 的模型：%q", bal)
	}
	for i := 0; i < 5; i++ {
		if _, set := m.project().Models[deckstore.Balanced]; !set {
			break
		}
		press(m, "enter")
	}
	if _, set := m.project().Models[deckstore.Balanced]; set {
		t.Fatal("轮换到最后应是「不设」")
	}
	for i := 0; i < 5 && !strings.HasPrefix(m.project().Models[deckstore.Balanced], "kimi"); i++ {
		press(m, "enter")
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
	if picked(m.project(), "kimi") || strings.HasPrefix(m.project().Models[deckstore.Balanced], "kimi") || m.project().MainAgent.Agent == "kimi" {
		t.Fatalf("取消 kimi 后应清掉引用：%+v", *m.project())
	}
	press(m, "s")
	if len(api.saved) != 1 || api.saved[0].Validate() != nil {
		t.Fatalf("保存的设置应能通过校验：%v", api.saved)
	}
	if !strings.Contains(m.msg, "保存完成") {
		t.Fatalf("应提示保存完成：%q", m.msg)
	}
}

func TestSettingsRevokeTrustAndQuitGuard(t *testing.T) {
	m, api := settingsFixture()
	m.cur = len(m.rows()) - 1 // 最后一行是授权
	press(m, "d")
	if len(api.revoked) != 1 || api.revoked[0] != "deck/codex" {
		t.Fatalf("应撤销 deck 的 codex 授权：%v", api.revoked)
	}
	// 有改动时按 q：先提示，再按一次才关
	m.cur = 0
	press(m, " ")
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
