package deckui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// n049 评审 1 的返工测试（B4、B5、S3）。

// drain 把一个命令及其后续命令跑完（Update 返回的下一条命令也跑）。
func drain(m *Settings, cmd tea.Cmd) {
	for cmd != nil {
		out := cmd()
		if out == nil {
			return
		}
		_, cmd = m.Update(out)
	}
}

func TestEmptySettingsKeysDoNotPanic(t *testing.T) {
	// 没有 agent、没有项目、没有授权（n057 起只剩「加入本机的 agent」一行）：乱按不崩、不撤销任何授权
	api := &fakeSettingsAPI{set: deckstore.Settings{}}
	m := &Settings{API: api}
	m.Update(m.load())
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("没有项目、没有授权时按键崩溃：%v", r)
		}
	}()
	press(m, "down", "j", "d", "up", "k", "down", "down", "d")
	if m.cur != 0 || len(api.revoked) != 0 {
		t.Fatalf("只有一行时光标应停在 0、不做任何事：cur=%d revoked=%v", m.cur, api.revoked)
	}
	press(m, "enter", "down", "enter", "esc", "j", " ", "esc", "esc")
	if len(api.revoked) != 0 || len(api.saved) != 0 {
		t.Fatalf("不应撤销或保存：revoked=%v saved=%v", api.revoked, api.saved)
	}
}

func TestCursorClampsWhenListShrinks(t *testing.T) {
	m, api := settingsFixture()
	m.cur = len(m.rows()) + 5 // 例如授权被撤销后列表变短
	press(m, "d")
	if len(api.revoked) != 1 || m.cur != len(m.rows())-1 {
		t.Fatalf("光标应收回到最后一行（授权行）再撤销：cur=%d revoked=%v", m.cur, api.revoked)
	}
}

func TestSingleLineFieldsDropNewlinesAndTabs(t *testing.T) {
	for _, s := range []string{"name\nFAKE", "name\tFAKE", "name\r\nFAKE", "a\x1b]0;x\x07\nb"} {
		if v := sanitize(s); strings.ContainsAny(v, "\n\t\r\x1b") {
			t.Errorf("单行字段留下了控制字符：%q → %q", s, v)
		}
	}
	d := sample()
	d.Sessions[0].Title = "两行\n伪造的一行\t列"
	d.Sessions[0].Tool = "co\ndex"
	d.Projects[0].Name = "项目\n名"
	frames := []Frame{(&Sidebar{Data: d, Mode: "none"}).Render(40, 30), (&Tabs{Data: d}).Render(120)}
	d.View.Open = append(d.View.Open, "未知\n会话")
	frames = append(frames, (&Tabs{Data: d}).Render(120))
	for i, f := range frames {
		for _, l := range f.Lines {
			if strings.ContainsAny(l, "\n\t") {
				t.Fatalf("第 %d 个画面有一行带换行或制表符：%q", i, l)
			}
		}
	}
	m, _ := settingsFixture()
	m.set.Agents[0].Name = "Claude\nCode"
	m.set.Projects[0].Name = "Agent\tDeck"
	m.trust[0].Dir = "/tmp/a\nb"
	m.msg = "出错\n第二行"
	if v := m.View(); strings.Contains(v, "Claude\nCode") || strings.Contains(v, "Agent\tDeck") || strings.Contains(v, "a\nb") || strings.Contains(v, "出错\n") {
		t.Fatalf("设置界面的单行字段带换行或制表符：\n%s", v)
	}
}

func TestRevokeKeepsUnsavedEdits(t *testing.T) {
	m, api := settingsFixture()
	press(m, "down", "enter") // 勾上第二个 agent（codex），未保存
	if !picked(m.project(), "codex") || !m.dirty {
		t.Fatal("前提：有一处没保存的改动")
	}
	api.trust = nil // deckd 那边撤销后授权列表变空
	m.cur = len(m.rows()) - 1
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	drain(m, cmd)
	if len(api.revoked) != 1 || len(m.trust) != 0 {
		t.Fatalf("应撤销并刷新授权列表：revoked=%v trust=%v", api.revoked, m.trust)
	}
	if !picked(m.project(), "codex") || !m.dirty {
		t.Fatal("撤销授权丢掉了没保存的改动")
	}
}

func TestSaveSendsSnapshotAndKeepsLaterEdits(t *testing.T) {
	m, api := settingsFixture()
	press(m, "down", "enter") // 勾 codex
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	press(m, "down", "enter") // 保存还没回来时又勾一个
	drain(m, cmd)
	if len(api.saved) != 1 {
		t.Fatalf("应保存一次：%d", len(api.saved))
	}
	sent := api.saved[0].Projects[0].Agents
	if strings.Join(sent, ",") != "claude,codex" {
		t.Fatalf("发出去的应是按 s 那一刻的设置，后来的编辑不能混进去：%v", sent)
	}
	if len(m.project().Agents) != 3 || !m.dirty || !strings.Contains(m.msg, "还没保存") {
		t.Fatalf("保存期间的改动应留着并标为没保存：agents=%v dirty=%v msg=%q", m.project().Agents, m.dirty, m.msg)
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	drain(m, cmd)
	if m.dirty || len(api.saved) != 2 {
		t.Fatalf("再存一次后应干净：dirty=%v saved=%d", m.dirty, len(api.saved))
	}
}

// n049 评审 2 B7：保存成功后的重读还没回来时又改了：重读回来不覆盖新改动，仍标为没保存。
func TestReloadAfterSaveKeepsEditsMadeMeanwhile(t *testing.T) {
	m, api := settingsFixture()
	press(m, "down", "enter") // 勾 codex
	_, save := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	done := save()
	api.set, _ = cloneSettings(api.saved[0]) // deckd 那边存下的是按 s 那一刻的设置
	_, reload := m.Update(done)
	if reload == nil {
		t.Fatal("保存成功后应重读")
	}
	press(m, "down", "enter") // 重读回来之前又勾了一个
	agents := strings.Join(m.project().Agents, ",")
	m.Update(reload())
	if strings.Join(m.project().Agents, ",") != agents || !m.dirty || !strings.Contains(m.msg, "还没保存") {
		t.Fatalf("重读覆盖了新改动：%v（之前 %s）dirty=%v msg=%q", m.project().Agents, agents, m.dirty, m.msg)
	}
	// 没有新改动时，重读照常换成 deckd 存下的设置、清掉 dirty
	_, save = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	done = save()
	api.set, _ = cloneSettings(api.saved[1])
	_, reload = m.Update(done)
	m.Update(reload())
	if m.dirty || strings.Join(m.project().Agents, ",") != agents {
		t.Fatalf("没有新改动时重读应照常：%v dirty=%v", m.project().Agents, m.dirty)
	}
}
