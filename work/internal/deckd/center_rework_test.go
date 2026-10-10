package deckd

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/lease"
	"github.com/Tingschen287/terminal-deck/work/internal/viewstate"
)

// n049 评审 1 B2：client 重建后（新 client 只读、停在 deck-idle）做 reset，交还摘要必须是被接管会话的画面，
// 不能是 deck-idle 的占位画面。
func TestResetAfterRebuildCapturesTheDepartedSession(t *testing.T) {
	w := newCenterWorld(t, attachIdle)
	l, err := lease.Open(filepath.Join(t.TempDir(), "leases.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.Grant("sub", "deck 派出")
	targets := map[string]viewstate.Target{"sub": {Session: "sub", Tmux: "agentdeck_sub", Role: "sub", Title: "子"}}
	m := viewstate.New(w.center, l, func(id string) (viewstate.Target, bool) { x, ok := targets[id]; return x, ok },
		func(string) error { return nil }, nil)
	if err := m.Open("sub"); err != nil {
		t.Fatal(err)
	}
	if err := m.Takeover("sub"); err != nil {
		t.Fatal(err)
	}
	// 模拟新 client：只读、显示 deck-idle（deck center 重建后的起点）
	if err := w.center.SetReadOnly(true); err != nil {
		t.Fatal(err)
	}
	cl, err := w.center.client()
	if err != nil {
		t.Fatal(err)
	}
	if err := agentdeck.SwitchClient(context.Background(), w.agent, cl.TTY, viewstate.Idle); err != nil {
		t.Fatal(err)
	}
	if err := m.CenterReset(clientID(t, w)); err != nil {
		t.Fatal(err)
	}
	ev, _ := l.Events()
	hb := ev[len(ev)-1].Handback
	if hb == nil || !strings.Contains(strings.Join(hb.LastLines, "\n"), "sub working") {
		t.Fatalf("交还摘要应是被接管会话的画面：%+v", hb)
	}
}

// 被交还的会话已经没了：取不到摘要，保持接管、等待交还，不拿别的画面顶替。
func TestHandbackOfVanishedSessionStaysPending(t *testing.T) {
	w := newCenterWorld(t, attachIdle)
	l, err := lease.Open(filepath.Join(t.TempDir(), "leases.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.Grant("gone", "deck 派出")
	targets := map[string]viewstate.Target{
		"sub":  {Session: "sub", Tmux: "agentdeck_sub", Role: "sub", Title: "子"},
		"gone": {Session: "gone", Tmux: "agentdeck_gone", Role: "sub", Title: "没了"},
	}
	m := viewstate.New(w.center, l, func(id string) (viewstate.Target, bool) { x, ok := targets[id]; return x, ok },
		func(string) error { return nil }, nil)
	// 当成已经显示并接管（agent-deck 上并没有 agentdeck_gone）：直接构造不了，就用 sub 打开再改目标的 tmux 名
	if err := m.Open("sub"); err != nil {
		t.Fatal(err)
	}
	targets["gone"] = viewstate.Target{Session: "gone", Tmux: "agentdeck_sub", Role: "sub", Title: "没了"}
	if err := m.Open("gone"); err != nil {
		t.Fatal(err)
	}
	if err := m.Takeover("gone"); err != nil {
		t.Fatal(err)
	}
	targets["gone"] = viewstate.Target{Session: "gone", Tmux: "agentdeck_gone", Role: "sub", Title: "没了"}
	err = m.CenterReset(clientID(t, w))
	if err == nil {
		t.Fatal("会话没了、取不到摘要，不应交还成功")
	}
	if got, _ := l.Get("gone"); got.Holder != lease.HolderOwner {
		t.Fatalf("应保持接管：%+v", got)
	}
	if st := m.State(); !contains(st.HandbackPending, "gone") {
		t.Fatalf("应标为等待交还：%+v", st)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// TmuxCenter 不把占位会话的画面当交还摘要。
func TestCaptureRefusesIdle(t *testing.T) {
	c := NewTmuxCenter(filepath.Join(t.TempDir(), "l"), filepath.Join(t.TempDir(), "a"))
	for _, s := range []string{"", viewstate.Idle} {
		if _, err := c.Capture(s, 5); err == nil {
			t.Fatalf("不应抓 %q", s)
		}
	}
}

func clientID(t *testing.T, w *centerWorld) int {
	t.Helper()
	id, err := w.center.ClientID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// n049 评审 1 B1（真 tmux）：通知里的进程号不是中间现在的 client，reset 什么都不做。
func TestRealCenterRefusesResetFromAnotherClient(t *testing.T) {
	w := newCenterWorld(t, attachIdle)
	l, err := lease.Open(filepath.Join(t.TempDir(), "leases.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.Grant("sub", "deck 派出")
	targets := map[string]viewstate.Target{"sub": {Session: "sub", Tmux: "agentdeck_sub", Role: "sub", Title: "子"}}
	m := viewstate.New(w.center, l, func(id string) (viewstate.Target, bool) { x, ok := targets[id]; return x, ok },
		func(string) error { return nil }, nil)
	if err := m.Open("sub"); err != nil {
		t.Fatal(err)
	}
	cur := clientID(t, w)
	if err := m.CenterReset(cur + 100000); !errors.Is(err, viewstate.ErrStaleClient) {
		t.Fatalf("别的 client 的 reset 应拒绝：%v", err)
	}
	if st := m.State(); st.Focus != "sub" {
		t.Fatalf("被拒绝的 reset 不应隐藏焦点：%+v", st)
	}
	if err := m.CenterReset(cur); err != nil {
		t.Fatal(err)
	}
	if st := m.State(); st.Focus != "" {
		t.Fatalf("现在这个 client 的 reset 应执行：%+v", st)
	}
}
