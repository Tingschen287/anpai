package deckd

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/viewstate"
)

// n049 评审 2 B1（真 tmux）：中间窗格里的 client 重建（新旧共用同一个窗格的 tty）。绑定到旧 client 的操作
// 不作用到新 client 上；解除绑定后照常操作新 client。
func TestBoundCenterRefusesTheReplacementClient(t *testing.T) {
	w := newCenterWorld(t, func(a string) string { return "sh -c 'while true; do " + attachIdle(a) + "; done'" })
	old, err := w.center.ClientID()
	if err != nil {
		t.Fatal(err)
	}
	oldCl, _ := w.center.client()
	w.center.Bind(old)
	defer w.center.Bind(0)
	if out, err := exec.Command("tmux", "-S", w.agent, "detach-client", "-t", oldCl.TTY).CombinedOutput(); err != nil {
		t.Fatalf("detach：%v %s", err, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if cur, err := w.center.ClientID(); err == nil && cur != old {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("新 client 没连上")
		}
		time.Sleep(20 * time.Millisecond)
	}
	newCl, _ := w.center.find(0)
	if newCl.TTY != oldCl.TTY {
		t.Fatalf("前提：重建的 client 与旧的共用中间窗格的 tty（%s → %s）", oldCl.TTY, newCl.TTY)
	}
	for name, op := range map[string]func() error{
		"放开输入": func() error { return w.center.SetReadOnly(false) },
		"切换":   func() error { return w.center.Switch("agentdeck_main") },
		"读只读":  func() error { _, err := w.center.ReadOnly(); return err },
	} {
		if err := op(); !errors.Is(err, viewstate.ErrStaleClient) {
			t.Fatalf("%s：绑定的 client 已被替换，应拒绝：%v", name, err)
		}
	}
	st, _ := agentdeck.ClientState(context.Background(), w.agent, newCl.TTY)
	if st.Session != viewstate.Idle || !st.ReadOnly {
		t.Fatalf("新 client 应保持起点（只读、deck-idle）：%+v", st)
	}
	w.center.Bind(0)
	if err := w.center.Switch("agentdeck_main"); err != nil {
		t.Fatalf("解除绑定后应能操作新 client：%v", err)
	}
}
