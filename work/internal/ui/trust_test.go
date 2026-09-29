package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tingschen287/terminal-deck/work/internal/dispatch"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

func key(m Model, s string) (Model, tea.Cmd) {
	var k tea.KeyMsg
	switch s {
	case "esc":
		k = tea.KeyMsg{Type: tea.KeyEsc}
	default:
		k = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	next, cmd := m.Update(k)
	return next.(Model), cmd
}

func codexFD(tool string) *fakeDispatcher {
	return &fakeDispatcher{cands: []dispatch.Model{{ID: "gpt-5.6-terra", Tool: tool, Class: dispatch.Frontier}}}
}

// n043 owner 验收补充发现：派 codex 之前，界面把「上游会改你的 ~/.codex/config.toml」摆出来，按 y 才派。
// 确认之前既不开工也不派工；乱按键不派；Esc 回去重挑。
func TestCodexDispatchWaitsForTheOwnerToConfirmTheTrustWrite(t *testing.T) {
	fd := codexFD("codex")
	st := &fakeStarter{attempt: 1}
	m := modelWithDispatcher(t, []projectctl.Node{nodeWith("n1", projectctl.StateReady, "frontier")}, fd).WithStarter(st)
	m = pressD(m)
	m, cmd := enterWithReason(t, m, "唯一够档的")
	if cmd != nil || !m.askingTrust {
		t.Fatalf("派 codex 前应当先停在确认这一步：askingTrust=%v cmd=%v notice=%q", m.askingTrust, cmd != nil, m.notice)
	}
	view := m.View()
	for _, want := range []string{"~/.codex/config.toml", "trust", "/tmp/proj", "整份文件", "按 y 确认派出"} {
		if !strings.Contains(view, want) {
			t.Errorf("确认画面缺 %q：\n%s", want, view)
		}
	}
	m, cmd = key(m, "x")
	if cmd != nil || !m.askingTrust {
		t.Error("别的键不该派出")
	}
	if len(st.got) != 0 || len(fd.got) != 0 {
		t.Fatalf("确认之前不该开工或派工：starts=%d dispatch=%d", len(st.got), len(fd.got))
	}
	m, cmd = key(m, "y")
	if cmd == nil || m.askingTrust {
		t.Fatal("按 y 应当派出")
	}
	if done := cmd().(dispatchDoneMsg); !done.ok || len(st.got) != 1 || len(fd.got) != 1 {
		t.Errorf("按 y 之后应当开工并派工一次：%+v starts=%d dispatch=%d", done, len(st.got), len(fd.got))
	}
	if fd.got[0].ChoiceReason != "唯一够档的" {
		t.Errorf("确认这一步不该丢掉刚写的理由：%q", fd.got[0].ChoiceReason)
	}

	// Esc：回到挑模型，不派。
	fd2 := codexFD("codex")
	m = modelWithDispatcher(t, []projectctl.Node{nodeWith("n1", projectctl.StateReady, "frontier")}, fd2)
	m = pressD(m)
	m, _ = enterWithReason(t, m, "x")
	m, cmd = key(m, "esc")
	if cmd != nil || m.askingTrust || m.mode != ModePickModel || len(fd2.got) != 0 {
		t.Errorf("Esc 应当回到挑模型、不派：askingTrust=%v mode=%v got=%d", m.askingTrust, m.mode, len(fd2.got))
	}
}

// 上游内置、与 codex 不兼容的工具不问；认不出的自定义工具照实说「可能」。
func TestTrustStepOnlyForToolsThatMayWriteIt(t *testing.T) {
	fd := &fakeDispatcher{cands: []dispatch.Model{{ID: "claude-opus-5", Tool: "claude", Class: dispatch.Frontier}}}
	m := modelWithDispatcher(t, []projectctl.Node{nodeWith("n1", projectctl.StateReady, "frontier")}, fd)
	m = pressD(m)
	m, cmd := enterWithReason(t, m, "x")
	if m.askingTrust || cmd == nil {
		t.Errorf("claude 不该进确认 trust 这一步")
	}

	m = modelWithDispatcher(t, []projectctl.Node{nodeWith("n1", projectctl.StateReady, "frontier")}, codexFD("grok"))
	m = pressD(m)
	m, cmd = enterWithReason(t, m, "x")
	if !m.askingTrust || cmd != nil || !strings.Contains(m.View(), "自定义工具") {
		t.Errorf("自定义工具应当提示可能写 trust：askingTrust=%v\n%s", m.askingTrust, m.View())
	}
}

// 派评审同样要确认：评审者用 codex 时上游一样会写 trust。
func TestCodexReviewerAlsoAsks(t *testing.T) {
	n := submittedNode("n1", 1, "exec")
	n.ReviewIndependent = false
	fd := codexFD("codex")
	m := modelWithDispatcher(t, []projectctl.Node{n}, fd)
	m = pressD(m)
	m, cmd := enterWithReason(t, m, "x")
	if !m.askingTrust || cmd != nil || len(fd.reviews) != 0 {
		t.Fatalf("派 codex 评审前应当先确认：askingTrust=%v reviews=%d", m.askingTrust, len(fd.reviews))
	}
	if _, cmd = key(m, "y"); cmd == nil {
		t.Fatal("按 y 应当派出评审")
	}
	cmd()
	if len(fd.reviews) != 1 {
		t.Errorf("按 y 之后应当派评审一次：%d", len(fd.reviews))
	}
}
