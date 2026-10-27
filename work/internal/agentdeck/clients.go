package agentdeck

import (
	"context"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"os"
	"os/exec"
	"strings"
)

// AttachedClients 数一个会话此刻连着几个 tmux 客户端（n039）。
//
// 写入仲裁用它发现「不经 Work Deck 的接管」：owner 直接用 agent-deck session attach 或 tmux 进入会话，
// Work Deck 的写入权记账并不知道，但 tmux 知道有客户端连着。上游的 session attach 本身也是一个 tmux 客户端。
//
// 找会话的办法与按 T 直连相同：先问菜单拿会话名与 socket 名，菜单没有就退到 list 取会话名，
// 再按会话身份扫 socket（ResolveSocketFor）。任何一步不成都返回错误——调用方据此不投递，
// 不把「查不到」当成「没人连着」。
//
// 边界：它数的是查询那一刻。查询之后、消息送达之前有人进来，这里看不见（见 test-report 已知限制）。
func (c *Client) AttachedClients(ctx context.Context, sessionID string) (int, error) {
	if sessionID == "" {
		return 0, i18n.E("err.agentdeck.clientattachedclients.1")
	}
	tmuxSession, socket := "", ""
	if menu, err := c.MenuSnapshot(ctx); err == nil {
		if s, sock, ok := menu.TmuxTargetOf(sessionID); ok {
			tmuxSession, socket = s, sock
		}
	}
	if tmuxSession == "" {
		listed, err := c.ListSessions(ctx)
		if err != nil {
			return 0, i18n.E("err.agentdeck.clientattachedclients.2", sessionID, err)
		}
		s, ok := TmuxSessionOf(listed, sessionID)
		if !ok {
			return 0, i18n.E("err.agentdeck.clientattachedclients.3", sessionID)
		}
		sock, err := ResolveSocketFor(ctx, s, sessionID)
		if err != nil {
			return 0, i18n.E("err.agentdeck.clientattachedclients.4", sessionID, err)
		}
		tmuxSession, socket = s, sock
	}
	return listClientsOn(ctx, socket, tmuxSession)
}

// listClientsOn 在一个 tmux server 上数某个会话连着的客户端。写法与 instanceIDOf 相同：
// socket 与会话名都作为入参进来，只落在 -L 与 -t 之后的数据位。
func listClientsOn(ctx context.Context, socket, tmuxSession string) (int, error) {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	args := []string{}
	if socket != "" && socket != "default" {
		args = append(args, "-L", socket)
	}
	args = append(args, "list-clients", "-t", exactTarget(tmuxSession), "-F", "#{client_tty}")
	list := exec.CommandContext(probeCtx, "tmux", args...)
	list.Env = withoutTmux(os.Environ())
	out, err := list.Output()
	if err != nil {
		return 0, i18n.E("err.agentdeck.listclientson.1", tmuxSession, err)
	}
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n, nil
}
