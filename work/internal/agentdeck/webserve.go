package agentdeck

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"
)

// n055：deck up 自己管 agent-deck 的 headless 服务（会话运行状态的来源）。原先要 owner 另开窗口常驻
// `agent-deck web` 并手传 token；现在 deck up 发现它没在跑就在布局的后台窗口里起（与 deckd 一样随布局 server 停止）。
// 起服务只给出命令行，由布局 tmux 的后台窗口执行：本包不另起长驻进程（execWithStdin 有超时，会把服务杀掉）。

// DefaultWebAddr 是 deck 自管的 headless 服务地址（只监听回环）。
const DefaultWebAddr = "127.0.0.1:8420"

// ErrWebDown 表示地址上没有服务在听（连接被拒或超时）：可以由 deck 起一个。
var ErrWebDown = errors.New("agent-deck headless 服务没在跑")

// ProbeWeb 用 token 读一次菜单（GET /api/menu，只读）：nil 表示服务在、token 对；ErrWebDown 表示没在听；
// 其他错误表示地址上有服务但不是能用的 agent-deck（别的程序占着端口，或 token 对不上）。
func ProbeWeb(ctx context.Context, profile, addr, token string) error {
	c, err := NewClient("agent-deck", profile, addr, token)
	if err != nil {
		return err
	}
	c.Timeout = 2 * time.Second
	_, err = c.MenuSnapshot(ctx)
	var ne *net.OpError
	if errors.As(err, &ne) {
		return ErrWebDown
	}
	return err
}

// WebServeCommand 是在布局后台窗口里起 headless 服务的命令行（交给 tmux 的 shell 执行）。
// 只监听回环；token 从文件读（上游要求文件 0600），不出现在命令行参数里；profile 与其余调用一样经环境变量给。
func WebServeCommand(binary, profile, addr, tokenFile string) string {
	return "exec env AGENTDECK_PROFILE=" + shQuote(profile) + " " + shQuote(binary) +
		" web --no-tui --listen " + shQuote(addr) + " --token-file " + shQuote(tokenFile)
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
