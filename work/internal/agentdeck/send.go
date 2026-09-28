package agentdeck

import (
	"context"
	"encoding/json"
	"errors"
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
	out, err := c.execWithStdin(ctx, strings.NewReader(message),
		"session", "send", sessionID, "--message-file", "-", "--json")
	if err == nil {
		return nil
	}
	// 上游失败时 --json 的输出里有 delivery 字段（上游 session_cmd.go:3056-3087）。
	// delivery=typed：正文到了会话画面，但没有证据说明对方接了这一轮——上游按失败退出（issue #1793），
	// 而 n040 终审实测到它是假阴性：评审会话其实收到并执行了 brief。这种情况单独报出来，
	// 让派工器不把会话当成没派出去丢掉（n042，n040 终审断点 5）。其余 delivery 仍是投递失败。
	var rep struct {
		Delivery string `json:"delivery"`
		Error    string `json:"error"`
	}
	if jsonErr := json.Unmarshal(out, &rep); jsonErr == nil && rep.Delivery == DeliveryTyped {
		return &ErrDeliveryUnconfirmed{SessionID: sessionID, Detail: rep.Error}
	}
	return fmt.Errorf("向会话 %s 投递消息失败：%w", sessionID, err)
}

// DeliveryTyped 是上游 session send --json 的 delivery 取值之一：正文到了画面、提交没得到确认。
const DeliveryTyped = "typed"

// ErrDeliveryUnconfirmed 表示消息到了会话画面，但上游确认不了对方接了这一轮。
//
// 它不等于没送到：可能已经在跑（n040 实测），也可能还停在输入框。Work Deck 看画面也分不清这两种，
// 所以不自动重发、不当成失败丢掉，而是照实记「送达未确认」、交给 owner 去看。
type ErrDeliveryUnconfirmed struct {
	SessionID string
	Detail    string
}

func (e *ErrDeliveryUnconfirmed) Error() string {
	return fmt.Sprintf("消息到了会话 %s 的画面，但上游没确认提交（%s）", e.SessionID, e.Detail)
}

// IsDeliveryUnconfirmed 判断一个错误是不是「送达未确认」。
func IsDeliveryUnconfirmed(err error) bool {
	var u *ErrDeliveryUnconfirmed
	return errors.As(err, &u)
}
