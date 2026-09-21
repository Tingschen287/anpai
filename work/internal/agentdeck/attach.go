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
// 调用它的是 DirectAttachCommand，界面上对应 owner 按 T。
//
// 这条路的入口换过两次，记一下免得再绕回去：
// attempt 4 曾把它接成 AttachCommand 失败时的自动退路，那是死代码——
// AttachCommand 只在会话号为空或白名单不含 `session attach` 时报错，
// 前者早被界面的 sessionOf 拦掉，后者是编译期常量，生产里它不会失败。
// attempt 5 于是把入口整个删了，只留能力和文档，独立评审判定
// 「只有 helper 和文档不算一条路径」。attempt 6 改成显式按键：
// owner 随时可按，一定可达，也不需要猜主路什么时候算失败。
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


// DirectAttachCommand 造一条直连会话所在 tmux 的命令，界面上对应按 T。
//
// 两处取数，顺序有讲究：
//
//	1. `GET /api/menu`——它同时给 tmux 会话名和 socket 名，是最准的一份。
//	2. 菜单里没有这个会话时，退到 CLI `list --json`。
//
// 第 2 步不是可有可无的保险，它正是这条路存在的理由。上游 headless 服务对
// 菜单快照做内存缓存，只在自己处理变更时失效；CLI `launch` 是另一个进程，
// 服务感知不到，刚派出去的会话在菜单里一直是看不见的，直到服务重启。
// 也就是说：**最需要按 T 的时候，菜单恰好是空的。**
//
// attempt 6 只取菜单，于是这条路在它自己宣称的主场景下必然失败——
// 独立评审复现了这一点。而这个缓存事实是本包自己的集成测试早就钉死的，
// 我把它写进了注释当作 T 存在的理由，却没想到 T 自己也依赖那个被缓存的接口。
//
// `list` 不经服务，看得见；代价是它的行里没有 socket 名（上游结构体里就没这个
// 字段，见 MANIFEST.md 第 7 节），只能按默认 tmux server 连。
// 这件事必须说出来，所以第二个返回值是给人看的说明——降级不许静默。
//
// 实测两条来源的差异：TestIntegrationListSeesWhatTheCachedMenuCannot。
func (c *Client) DirectAttachCommand(ctx context.Context, sessionID string) (*exec.Cmd, string, error) {
	if sessionID == "" {
		return nil, "", fmt.Errorf("没有会话号，无法直连")
	}

	// 先问菜单。拿得到就用它，socket 名也在里面。
	menu, menuErr := c.MenuSnapshot(ctx)
	if menuErr == nil {
		if tmuxSession, socketName, ok := menu.TmuxTargetOf(sessionID); ok {
			cmd, err := c.DirectTmuxAttachCommand(ctx, tmuxSession, socketName)
			return cmd, "", err
		}
	}

	// 菜单没有它。多半就是那个缓存——这正是按 T 的时候。
	listed, listErr := c.ListSessions(ctx)
	if listErr != nil {
		if menuErr != nil {
			return nil, "", fmt.Errorf("菜单读不到（%v），list 也读不到：%w", menuErr, listErr)
		}
		return nil, "", fmt.Errorf("菜单里没有会话 %s，list 也读不到：%w", sessionID, listErr)
	}
	tmuxSession, ok := TmuxSessionOf(listed, sessionID)
	if !ok {
		return nil, "", fmt.Errorf("菜单和 list 里都没有会话 %s 的 tmux 会话名，无法直连", sessionID)
	}
	cmd, err := c.DirectTmuxAttachCommand(ctx, tmuxSession, "")
	note := "菜单里没有这个会话，多半是上游服务的菜单缓存；已改从 list 取 tmux 会话名。" +
		"list 不含 socket 名，这次按默认 tmux server 连——" +
		"要是这个 profile 配了非默认 socket，就会连不上。"
	return cmd, note, err
}
