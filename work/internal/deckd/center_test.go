package deckd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/lease"
	"github.com/Tingschen287/terminal-deck/work/internal/viewstate"
)

// 真 tmux（n049）：布局 server 的中间窗格里起一个只读 client 连到 agent server，TmuxCenter 按进程链认出它。
type centerWorld struct {
	layout, agent string
	center        *TmuxCenter
}

func newCenterWorld(t *testing.T, centerCmd func(agent string) string) *centerWorld {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("没有 tmux")
	}
	dir, err := os.MkdirTemp("", "dcw")
	if err != nil {
		t.Fatal(err)
	}
	w := &centerWorld{layout: filepath.Join(dir, "deck"), agent: filepath.Join(dir, "agent")}
	t.Cleanup(func() {
		for _, s := range []string{w.layout, w.agent} {
			c := exec.Command("tmux", "-S", s, "kill-server")
			c.Env = append(os.Environ(), "TMUX=")
			c.Run()
		}
		os.RemoveAll(dir)
	})
	ctx := context.Background()
	for name, cmd := range map[string]string{
		viewstate.Idle: "sleep 600", "agentdeck_main": "printf 'main ready\\n'; sleep 600",
		"agentdeck_sub": "printf 'sub working\\n'; sleep 600",
	} {
		if err := agentdeck.EnsureSession(ctx, w.agent, name, cmd); err != nil {
			t.Fatal(err)
		}
	}
	conf := filepath.Join(dir, "deck.conf")
	os.WriteFile(conf, []byte("set -g prefix None\nset -g status off\n"), 0o600)
	pane, err := agentdeck.LayoutNewSession(ctx, w.layout, conf, "deck", "main", "120", "30", centerCmd(w.agent))
	if err != nil {
		t.Fatal(err)
	}
	agentdeck.LayoutSetRole(ctx, w.layout, pane, "center")
	w.center = NewTmuxCenter(w.layout, w.agent)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := w.center.client(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("中间 client 没连上")
		}
		time.Sleep(50 * time.Millisecond)
	}
	return w
}

func attachIdle(agent string) string {
	return "tmux -S " + agent + " attach-session -r -t =" + viewstate.Idle
}

// 中间窗格进程本身就是 client，或 client 是它的子进程（deck center 的情形），都要认得出。
func TestTmuxCenterFindsClientDirectlyOrThroughParent(t *testing.T) {
	for name, cmd := range map[string]func(string) string{
		"直接":   attachIdle,
		"隔一层": func(a string) string { return "sh -c '" + attachIdle(a) + "; sleep 600'" },
	} {
		t.Run(name, func(t *testing.T) {
			w := newCenterWorld(t, cmd)
			ro, err := w.center.ReadOnly()
			if err != nil || !ro {
				t.Fatalf("起点应只读：%v %v", ro, err)
			}
		})
	}
}

// 设为可写、再设为只读都能读回；对已是目标状态的再设一次不翻转。
func TestTmuxCenterSetReadOnlyIsIdempotent(t *testing.T) {
	w := newCenterWorld(t, attachIdle)
	for _, want := range []bool{false, false, true, true, false} {
		if err := w.center.SetReadOnly(want); err != nil {
			t.Fatal(err)
		}
		if ro, _ := w.center.ReadOnly(); ro != want {
			t.Fatalf("设为 %v 后读回 %v", want, ro)
		}
	}
}

// 布局没起来时如实报错，不当成成功。
func TestTmuxCenterWithoutLayoutReportsNoCenter(t *testing.T) {
	c := NewTmuxCenter(filepath.Join(t.TempDir(), "none"), filepath.Join(t.TempDir(), "none2"))
	if err := c.SetReadOnly(true); err != ErrNoCenter {
		t.Fatalf("应是 ErrNoCenter：%v", err)
	}
}

// 状态机走真实 client：子 agent 只读、确认过 brief 的主 agent 可写、隐藏回到 deck-idle、
// 接管后可写、离开时交还并带上画面摘要。
func TestViewMachineOnRealCenter(t *testing.T) {
	w := newCenterWorld(t, attachIdle)
	l, err := lease.Open(filepath.Join(t.TempDir(), "leases.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.Grant("sub", "deck 派出")
	targets := map[string]viewstate.Target{
		"main": {Session: "main", Tmux: "agentdeck_main", Role: "main", BriefConfirmed: true, Title: "主"},
		"sub":  {Session: "sub", Tmux: "agentdeck_sub", Role: "sub", Title: "子"},
	}
	var notes []string
	m := viewstate.New(w.center, l, func(id string) (viewstate.Target, bool) { x, ok := targets[id]; return x, ok },
		func(s string) error { notes = append(notes, s); return nil }, nil)
	state := func() agentdeck.TmuxClient {
		cl, err := w.center.client()
		if err != nil {
			t.Fatal(err)
		}
		st, _ := agentdeck.ClientState(context.Background(), w.agent, cl.TTY)
		return st
	}
	if err := m.Open("sub"); err != nil {
		t.Fatal(err)
	}
	if st := state(); st.Session != "agentdeck_sub" || !st.ReadOnly {
		t.Fatalf("子 agent 应只读显示：%+v", st)
	}
	if err := m.Open("main"); err != nil {
		t.Fatal(err)
	}
	if st := state(); st.Session != "agentdeck_main" || st.ReadOnly {
		t.Fatalf("确认过 brief 的主 agent 应可写：%+v", st)
	}
	if err := m.Focus("sub"); err != nil {
		t.Fatal(err)
	}
	if err := m.Takeover("sub"); err != nil {
		t.Fatal(err)
	}
	if st := state(); st.ReadOnly {
		t.Fatal("接管后应可写")
	}
	if err := m.Hide("sub"); err != nil {
		t.Fatal(err)
	}
	if st := state(); st.Session != "agentdeck_main" || st.ReadOnly {
		t.Fatalf("隐藏焦点后应回到上一个打开的主 agent：%+v", st)
	}
	if got, _ := l.Get("sub"); got.Holder != lease.HolderWorkbench {
		t.Fatalf("离开后应已交还：%+v", got)
	}
	ev, _ := l.Events()
	if hb := ev[len(ev)-1].Handback; hb == nil || !strings.Contains(strings.Join(hb.LastLines, "\n"), "sub working") {
		t.Fatalf("交还摘要应带真实画面：%+v", hb)
	}
	if err := m.Hide("main"); err != nil {
		t.Fatal(err)
	}
	if st := state(); st.Session != viewstate.Idle || !st.ReadOnly {
		t.Fatalf("全部隐藏后应显示 deck-idle 且只读：%+v", st)
	}
	if err := m.Resync(); err != nil {
		t.Fatal(err)
	}
}
