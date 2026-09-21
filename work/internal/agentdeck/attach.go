package agentdeck

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// AttachCommand 造一条「进入某个会话」的命令，但不执行它。
//
// 为什么返回命令而不是直接跑：进入会话要把整个终端交给对方，这件事必须由
// 界面层在挂起自己之后做，adapter 在后台跑它只会把两边的终端搅在一起。
//
// 走的是上游自己的 `session attach`：会话在哪个 tmux socket 上，上游自己最清楚，
// 这条路不需要本包操心 socket。
//
// （socket 名其实是拿得到的，见 DirectTmuxAttachCommand 与 MANIFEST.md 第 7 节。
// 这里不用它，是因为没必要，不是因为取不到。）
func (c *Client) AttachCommand(ctx context.Context, sessionID string) (*exec.Cmd, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("没有会话号，无法进入")
	}
	if !isAllowedSubcommand("session", "attach") {
		return nil, &ErrSubcommandNotAllowed{Subcommand: "session attach"}
	}
	cmd := exec.CommandContext(ctx, c.Binary, "session", "attach", sessionID)
	cmd.Env = c.env()
	// Work Deck 常开在 tmux 里，进去就是两层，tmux 默认会拒绝。
	return prepareNested(cmd), nil
}

// DirectTmuxAttachCommand 造一条直连 tmux 的命令。
//
// **本函数目前没有生产调用点，只有单测。** 这一点写在这里，免得下一个人
// 以为它在某条路上跑着。
//
// 为什么留着：节点合同要求说清 tmux_socket_name 怎么取、怎么处理
// `list --json` 不含它，这个函数连同 TmuxTargetOf 就是那份答案的可执行形式，
// 单测盯着 argv 的构造（空 socket 不带 -L，非空带）。
//
// 为什么不接进界面：attempt 4 曾把它接成 AttachCommand 失败时的退路，
// 独立评审指出那是死代码——AttachCommand 只在会话号为空或白名单不含
// `session attach` 时报错，前者早被界面的 sessionOf 拦掉，后者是编译期常量，
// 生产里它不会失败。而真正需要退路的那个故障（上游 headless 服务的菜单缓存
// 看不见刚 launch 的会话）发生在 tea.ExecProcess **执行之后**，
// 要在那里接退路是另一个设计：进程已经跑过一遍，终端状态得先收拾干净。
// 那件事留给后续节点，不在 n037 的范围里。
//
// socketName 来自上游菜单的 tmuxSocketName 字段。它带 omitempty：会话跑在默认
// tmux server 上时该字段不出现，此时空串就是正确答案，直接 attach 即可；
// 非默认 socket 时它给出 tmux -L 的选择器，必须带上，否则会连到错的 server。
//
// 参数按 argv 逐个传，不拼 shell 字符串——socket 名来自上游数据，
// 拼进 shell 就等于让它决定要跑什么。
func (c *Client) DirectTmuxAttachCommand(ctx context.Context, tmuxSession, socketName string) (*exec.Cmd, error) {
	if tmuxSession == "" {
		return nil, fmt.Errorf("上游没有给出 tmux 会话名，无法直连")
	}
	args := []string{}
	if socketName != "" {
		args = append(args, "-L", socketName)
	}
	args = append(args, "attach-session", "-t", tmuxSession)
	return prepareNested(exec.CommandContext(ctx, "tmux", args...)), nil
}

// TmuxTargetOf 从菜单里找某个会话的 tmux 会话名与 socket 名。
//
// socket 名为空是正常的，表示默认 server，不是「没查到」。
func (m MenuEvent) TmuxTargetOf(sessionID string) (session, socket string, ok bool) {
	for _, s := range m.SessionsOf() {
		if s.ID == sessionID {
			return s.TmuxSession, s.TmuxSocketName, s.TmuxSession != ""
		}
	}
	return "", "", false
}

// PaneOutput 取一个会话当前画面的文本，不接管它。
//
// 用于瞄一眼某个会话在干什么。这条路是只读的：它跑的是 capture-pane，
// 不发按键，不改会话状态。
func (c *Client) PaneOutput(ctx context.Context, sessionID string) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("没有会话号，无法预览")
	}
	out, err := c.exec(ctx, "session", "output", sessionID, "-pane")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// MenuSnapshot 取一份菜单快照。
//
// 与 Sessions 的区别：这是上游菜单的原始形态，含分组层级。
// tmux 会话名和 socket 名都在里面，直连 tmux 的那条路就靠它取数——
// socket 名带 omitempty，用默认 server 时字段不出现，空值即默认，
// 不是「没查到」。见 MANIFEST.md 第 7 节。
func (c *Client) MenuSnapshot(ctx context.Context) (MenuEvent, error) {
	var out MenuEvent
	if err := c.get(ctx, "/api/menu", &out); err != nil {
		return MenuEvent{}, err
	}
	return out, nil
}

// stripANSI 去掉画面里的转义序列，留纯文本。
//
// 预览要给人看，也要进证据文件。带着转义序列的文本在报告里没法读，
// 也没法逐行比对。
func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && !((s[j] >= 'a' && s[j] <= 'z') || (s[j] >= 'A' && s[j] <= 'Z')) {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// PaneOutputPlain 取画面文本并去掉转义序列。
func (c *Client) PaneOutputPlain(ctx context.Context, sessionID string) (string, error) {
	raw, err := c.PaneOutput(ctx, sessionID)
	if err != nil {
		return "", err
	}
	return stripANSI(raw), nil
}

