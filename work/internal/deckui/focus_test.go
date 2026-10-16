package deckui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/layout"
)

// 点完侧栏或标签后，键盘焦点要回到中间窗格（打字应进 CLI，而不是侧栏）。真 tmux。
func TestSelectCenterHandsKeyboardBack(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("没有 tmux")
	}
	dir, err := os.MkdirTemp("", "dui")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "deck")
	t.Cleanup(func() {
		c := exec.Command("tmux", "-S", sock, "kill-server")
		c.Env = append(os.Environ(), "TMUX=")
		c.Run()
		os.RemoveAll(dir)
	})
	conf := filepath.Join(dir, "c.conf")
	os.WriteFile(conf, []byte("set -g prefix None\n"), 0o600)
	ctx := context.Background()
	center, err := agentdeck.LayoutNewSession(ctx, sock, conf, "deck", "main", "120", "30", "sleep 600")
	if err != nil {
		t.Fatal(err)
	}
	agentdeck.LayoutSetRole(ctx, sock, center, layout.RoleCenter)
	side, _ := agentdeck.LayoutSplit(ctx, sock, center, agentdeck.SplitSpec{Horizontal: true, Before: true, Size: "30"}, "sleep 600")
	agentdeck.LayoutSetRole(ctx, sock, side, layout.RoleSidebar)
	agentdeck.LayoutSelect(ctx, sock, side) // 模拟：鼠标点了侧栏，焦点在侧栏
	selectCenter(sock)
	panes, _ := agentdeck.LayoutPanes(ctx, sock)
	for _, p := range panes {
		if p.Role == layout.RoleCenter && !p.Active {
			t.Fatal("焦点应回到中间窗格")
		}
	}
}
