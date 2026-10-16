package agentdeck

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// TmuxPane 是某个 tmux server 上的一个窗格（deckd 判定调用者身份用，n050）。
type TmuxPane struct {
	PaneID  string
	PanePID int
	Session string
}

// ListTmuxPanes 列出 socket 对应的 tmux server 上全部窗格。server 没在跑时返回空列表，不算错。
// 外部命令只在 adapter 包里调（边界测试 TestEachAdapterOnlyTouchesItsOwnUpstream），所以放在这里。
func ListTmuxPanes(ctx context.Context, socket string) ([]TmuxPane, error) {
	cmd := exec.CommandContext(ctx, "tmux", "-S", socket, "list-panes", "-a", "-F", "#{pane_id}\t#{pane_pid}\t#{session_name}")
	out, err := cmd.Output()
	if err != nil {
		if _, statErr := os.Stat(socket); errors.Is(statErr, os.ErrNotExist) {
			return nil, nil
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) && (strings.Contains(string(ee.Stderr), "no server running") || strings.Contains(string(ee.Stderr), "error connecting")) {
			return nil, nil
		}
		return nil, fmt.Errorf("列 tmux 窗格失败（%s）：%w", socket, err)
	}
	var panes []TmuxPane
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			continue
		}
		pid, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		panes = append(panes, TmuxPane{PaneID: f[0], PanePID: pid, Session: f[2]})
	}
	return panes, nil
}
