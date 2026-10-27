package deckui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
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
	press(m, "3")             // 授权页
	m.cur = len(m.rows()) + 5 // 例如授权被撤销后列表变短
	press(m, "d", "d")        // n059：撤销要再按一次确认
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
	m.msg = lit("出错\n第二行")
	if v := m.View(); strings.Contains(v, "Claude\nCode") || strings.Contains(v, "Agent\tDeck") || strings.Contains(v, "a\nb") || strings.Contains(v, "出错\n") {
		t.Fatalf("设置界面的单行字段带换行或制表符：\n%s", v)
	}
}

func TestRevokeKeepsUnsavedEdits(t *testing.T) {
	m, api := settingsFixture()
	api.fail = errors.New("deckd 不在") // 存不上：改动留在界面上
	press(m, "down", "enter")         // 勾上第二个 agent（codex）
	if !picked(m.project(), "codex") || !m.dirty {
		t.Fatal("前提：有一处没存上的改动")
	}
	api.fail = nil
	api.trust = nil // deckd 那边撤销后授权列表变空
	press(m, "3")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	drain(m, cmd)
	if len(api.revoked) != 1 || len(m.trust) != 0 {
		t.Fatalf("应撤销并刷新授权列表：revoked=%v trust=%v", api.revoked, m.trust)
	}
	if !picked(m.project(), "codex") || !m.dirty {
		t.Fatal("撤销授权丢掉了没存上的改动")
	}
}

// n059 自动保存：保存在路上时又改了，发出去的仍是那一刻的设置；回来后自动把最新的再存一次，存完才算干净。
func TestSaveSendsSnapshotAndKeepsLaterEdits(t *testing.T) {
	m, api := settingsFixture()
	press(m, "down")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // 勾 codex：自动保存发出
	press(m, "down")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // 保存还没回来时又勾一个
	if !m.pending || len(api.saved) != 0 {
		t.Fatal("保存在路上时不发第二个，记下回来再存")
	}
	drain(m, cmd)
	if len(api.saved) != 2 {
		t.Fatalf("应存两次：%d", len(api.saved))
	}
	if sent := strings.Join(api.saved[0].Projects[0].Agents, ","); sent != "claude,codex" {
		t.Fatalf("第一次发出去的应是那一刻的设置，后来的编辑不能混进去：%v", sent)
	}
	if len(api.saved[1].Projects[0].Agents) != 3 || api.saved[1].Generation != api.saved[0].Generation+1000 || m.dirty || m.saving {
		t.Fatalf("第二次存最新的、带存下的代数，之后干净：%v dirty=%v", api.saved[1].Projects[0].Agents, m.dirty)
	}
}

// n059：别处改过（409）就停下自动保存、提示按 R；按 R 重读后恢复自动保存。存不上时之后的改动照样再试。
func TestConflictStopsAutosaveUntilReload(t *testing.T) {
	m, api := settingsFixture()
	api.fail = &deckclient.Error{Status: 409, Message: "changed"}
	press(m, "down", "enter")
	if !m.conflict || !strings.Contains(ansi.Strip(m.View()), "按 R 重新读取") {
		t.Fatalf("冲突要停下并提示按 R：\n%s", ansi.Strip(m.View()))
	}
	api.fail = nil
	press(m, "down", "enter")
	if len(api.saved) != 0 {
		t.Fatal("冲突后不再自动保存（会盖掉别处的改动）")
	}
	press(m, "R")
	if !strings.Contains(m.msg.String(), "再按一次 R") {
		t.Fatalf("有没存上的改动时 R 要确认：%q", m.msg.String())
	}
	press(m, "R")
	if m.conflict || m.dirty || picked(m.project(), "codex") {
		t.Fatal("R 重读后丢掉没存上的改动、恢复正常")
	}
	press(m, "up", "up", "down", "enter")
	if len(api.saved) != 1 || m.dirty {
		t.Fatalf("重读后改动又能自动保存：%d", len(api.saved))
	}
	// 存不上（不是冲突）：显示原因，下一处改动再试
	api.fail = errors.New("坏了")
	press(m, "enter")
	if !m.dirty || m.conflict || !strings.Contains(ansi.Strip(m.View()), "没存上：坏了") {
		t.Fatalf("存不上要显示原因：\n%s", ansi.Strip(m.View()))
	}
	api.fail = nil
	press(m, "enter")
	if m.dirty || len(api.saved) != 2 {
		t.Fatalf("下一处改动再试就存上：%d", len(api.saved))
	}
}
