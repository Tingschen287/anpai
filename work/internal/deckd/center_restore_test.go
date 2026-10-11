package deckd

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/identity"
	"github.com/Tingschen287/terminal-deck/work/internal/lease"
	"github.com/Tingschen287/terminal-deck/work/internal/viewstate"
)

// n049 评审 3 B1-A 的第二道（真 tmux）：「找到 client」之后、执行切换之前，旧 client 退出、新 client 在同一个 tty 上
// 连上（产品里由栅栏挡住；这里用 shell 循环绕过栅栏，专门造出这一瞬）。切换落到了新 client 上：读回发现、把新 client
// 还原到起点（deck-idle、只读），操作报 ErrStaleClient 并整体中止。
func TestSwitchLandingOnReplacementIsRestored(t *testing.T) {
	w := newCenterWorld(t, func(a string) string { return "sh -c 'while true; do " + attachIdle(a) + "; done'" })
	l, err := lease.Open(filepath.Join(t.TempDir(), "leases.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	targets := map[string]viewstate.Target{
		"main": {Session: "main", Tmux: "agentdeck_main", Role: "main", BriefConfirmed: true},
		"sub":  {Session: "sub", Tmux: "agentdeck_sub", Role: "sub"},
	}
	m := viewstate.New(w.center, l, func(id string) (viewstate.Target, bool) { x, ok := targets[id]; return x, ok }, nil, nil)
	if err := m.Open("main"); err != nil {
		t.Fatal(err)
	}
	before := m.State()
	old, err := w.center.client()
	if err != nil {
		t.Fatal(err)
	}
	// 用进程链注入点在 find 认出旧 client 之后停住（第 4 次：SetReadOnly、ReadOnly、ClientID 之后是 Switch 的那次）
	entered, release := make(chan struct{}), make(chan struct{})
	orig := w.center.Ancestry
	var calls atomic.Int32
	w.center.Ancestry = func(pid int) ([]identity.Proc, error) {
		chain, e := orig(pid)
		if pid == old.PID && calls.Add(1) == 4 {
			close(entered)
			<-release
		}
		return chain, e
	}
	done := make(chan error, 1)
	go func() { done <- m.Open("sub") }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("没停在切换前")
	}
	if out, e := exec.Command("tmux", "-S", w.agent, "detach-client", "-t", old.TTY).CombinedOutput(); e != nil {
		close(release)
		t.Fatalf("detach：%v %s", e, out)
	}
	var next agentdeck.TmuxClient
	deadline := time.Now().Add(5 * time.Second)
	for {
		if next, err = w.center.find(0); err == nil && next.PID != old.PID {
			break
		}
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("新 client 没连上")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if next.TTY != old.TTY {
		close(release)
		t.Fatalf("前提：新旧 client 共用 tty（%s → %s）", old.TTY, next.TTY)
	}
	close(release)
	err = <-done
	if !errors.Is(err, viewstate.ErrStaleClient) {
		t.Fatalf("切换落到新 client 上应报 client 被替换：%v", err)
	}
	st, e := agentdeck.ClientState(context.Background(), w.agent, next.TTY)
	if e != nil {
		t.Fatal(e)
	}
	if st.PID != next.PID || st.Session != viewstate.Idle || !st.ReadOnly {
		t.Fatalf("新 client 应被还原到起点（deck-idle、只读）：%+v", st)
	}
	if after := m.State(); after.Focus != before.Focus || len(after.Open) != len(before.Open) {
		t.Fatalf("没写下任何东西应整体中止：%+v → %+v", before, after)
	}
}
