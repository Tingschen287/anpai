package agentdeck

import (
	"context"
	"fmt"
	"strings"

	"github.com/Tingschen287/terminal-deck/work/internal/lease"
)

// SendMessage 把一段文本送进一个已经在跑的会话。
//
// 这是评审 brief 的投递口。为什么单独有它，而不是在 launch 时把 brief 一起带上：
// 会话号要等 launch 返回才存在，而「评审有没有落进执行者的会话」这个判断
// 必须拿真实会话号来做。brief 若随 launch 出去，万一撞上就已经进了执行者的会话，
// 撤不回来。所以分两步：先起空会话，核验会话号，再把 brief 送进去。
//
// 文本走标准输入，不进 argv。上游自己提供了 `--message-file -`，
// 一大段 brief 不必经过 shell 引号，也不会被当成参数解释。
//
// 必须带一张 lease.Permit（n039）：许可只有写入权记账能签发，并在这里、投递之前再核一次，
// 同时登记「有投递在进行」，直到本次 exec 结束——这期间写入权记账拒绝登记 owner 接管。
// 本包内没有别的往会话里写字的路（TestEveryWriteIntoASessionNeedsAPermit）。
// 边界：它挡得住经 Work Deck 登记的接管；别人不经 Work Deck 直接进入会话，要靠 arbiter 投递前的客户端探测。
func (c *Client) SendMessage(ctx context.Context, p lease.Permit, sessionID, message string) error {
	if sessionID == "" {
		return fmt.Errorf("没有会话号，无法投递消息")
	}
	release, err := p.Begin(sessionID)
	if err != nil {
		return fmt.Errorf("不投递：%w", err)
	}
	defer release()
	// 只有空白也算空（n039 attempt 4 评审）：送出去对方照样不知道要做什么。
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("消息是空的，不投递：说不清要对方做什么，就别派这一趟")
	}
	// 默认行为是等对方的 agent 起来再发，这正是刚 launch 出来的会话需要的；
	// 不等的话消息可能落在 CLI 还没就绪的窗口里。超时由 Client.Timeout 兜着。
	_, err = c.execWithStdin(ctx, strings.NewReader(message),
		"session", "send", sessionID, "--message-file", "-")
	if err != nil {
		return fmt.Errorf("向会话 %s 投递消息失败：%w", sessionID, err)
	}
	return nil
}
