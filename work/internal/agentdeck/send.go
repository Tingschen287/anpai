package agentdeck

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
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
		return i18n.E("err.agentdeck.clientsendmessage.1")
	}
	release, err := p.Begin(sessionID)
	if err != nil {
		return i18n.E("err.agentdeck.clientsendmessage.2", err)
	}
	defer release()
	// 只有空白也算空（n039 attempt 4 评审）：送出去对方照样不知道要做什么。
	if strings.TrimSpace(message) == "" {
		return i18n.E("err.agentdeck.clientsendmessage.3")
	}
	// 默认行为是等对方的 agent 起来再发，这正是刚 launch 出来的会话需要的；
	// 不等的话消息可能落在 CLI 还没就绪的窗口里。超时由 Client.Timeout 兜着。
	out, err := c.execWithStdin(ctx, strings.NewReader(message),
		"session", "send", sessionID, "--message-file", "-", "--json")
	return sendResult(sessionID, out, err, false)
}

// SendToMain 给主 agent 送一条子 agent 的汇报（架构稿 §14.4 第 1 条，n051）：带 --defer-if-busy，
// 主 agent 正在回答就等它这一轮结束，最多等 MainDeferTimeout。还在忙就返回 ErrTargetBusy——上游在打字之前就放弃了，
// 什么都没送，调用方稍后再试。等的时候 deckd 持着中间窗格的锁（送前关输入、送后再放开），所以只等一小会儿。
// 与 SendMessage 一样必须带许可、先核对登记再调上游（TestEveryWriteIntoASessionNeedsAPermit）。
func (c *Client) SendToMain(ctx context.Context, p lease.Permit, sessionID, message string) error {
	if sessionID == "" {
		return i18n.E("err.agentdeck.clientsendmessage.1")
	}
	release, err := p.Begin(sessionID)
	if err != nil {
		return i18n.E("err.agentdeck.clientsendmessage.2", err)
	}
	defer release()
	if strings.TrimSpace(message) == "" {
		return i18n.E("err.agentdeck.clientsendtomain.1")
	}
	out, err := c.execWithStdin(ctx, strings.NewReader(message),
		"session", "send", sessionID, "--message-file", "-", "--json", "--defer-if-busy", "--defer-timeout", MainDeferTimeout)
	return sendResult(sessionID, out, err, true)
}

// MainDeferTimeout 是 SendToMain 每次最多等主 agent 结束这一轮的时间（上游 --defer-timeout）。
const MainDeferTimeout = "10s"

// SendIfIdle 给子 agent 发新的话，只在它停下时发（n051 attempt 3，owner 选方案 A'）：带 --defer-if-busy 和很短的
// SubIdleCheck，对方还在跑就返回 ErrTargetBusy——上游在打字之前就放弃了，什么都没送。忙不忙由上游在打字前一刻判断，
// 所以「这条话之后到的完成属于新任务」没有歧义。不能等久：等的时候子 agent 可能正好做完上一件并报告，报告会被算到新任务上。
// 与 SendMessage 一样必须带许可、先核对登记再调上游（TestEveryWriteIntoASessionNeedsAPermit）。
func (c *Client) SendIfIdle(ctx context.Context, p lease.Permit, sessionID, message string) error {
	if sessionID == "" {
		return i18n.E("err.agentdeck.clientsendmessage.1")
	}
	release, err := p.Begin(sessionID)
	if err != nil {
		return i18n.E("err.agentdeck.clientsendmessage.2", err)
	}
	defer release()
	if strings.TrimSpace(message) == "" {
		return i18n.E("err.agentdeck.clientsendtomain.1")
	}
	out, err := c.execWithStdin(ctx, strings.NewReader(message),
		"session", "send", sessionID, "--message-file", "-", "--json", "--defer-if-busy", "--defer-timeout", SubIdleCheck, "--timeout", SubReadyTimeout)
	if err != nil && (errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil || strings.Contains(err.Error(), "signal: killed")) {
		// 上游没在时限内返回、被结束：可能已经打进去了（attempt 3 自查），按送达未确认，调用方不撤回代次
		return &ErrDeliveryUnconfirmed{SessionID: sessionID, Detail: i18n.E("err.agentdeck.clientsendifidle.1", err).Error()}
	}
	return sendResult(sessionID, out, err, true)
}

// SubReadyTimeout 是上游等子 agent 就绪的上限（上游 --timeout，默认 10 分钟）：要短于 deckd 调上游的 60 秒，
// 否则上游可能在打字之后才被结束，deck 分不清送没送到。
const SubReadyTimeout = "30s"

// SubIdleCheck 是 SendIfIdle 让上游看对方忙不忙的时限（上游 --defer-timeout）。
const SubIdleCheck = "1s"

// ErrTargetBusy：--defer-if-busy 等到时限对方还在忙，上游没有投递（session_cmd.go:2825、internal/send/deferbusy.go:80）。
var ErrTargetBusy = i18n.E("err.agentdeck.pkg.2")

// deferBusyMarker 是上游 WaitUntilNotBusy 超时报错的前缀。
const deferBusyMarker = "defer-if-busy: target still busy"

// sendResult 按上游回执判断送达。
func sendResult(sessionID string, out []byte, err error, deferIfBusy bool) error {
	var rep struct {
		Delivery  string `json:"delivery"`
		Submitted *bool  `json:"submitted"`
		Error     string `json:"error"`
	}
	jsonErr := json.Unmarshal(out, &rep)
	if err == nil {
		// 上游按成功退出，也要看 submitted：只有 true 才算送达。delivery=unverified 时上游退出 0、submitted=false，
		// 意思是「提交结果未知」（上游 session_cmd.go:3059-3066）——那不是送达（n042 attempt 2 评审 blocker）。
		// 回执不是 JSON 或没有 submitted 字段，同样说不清，按未确认处理。
		if jsonErr == nil && rep.Submitted != nil && *rep.Submitted {
			return nil
		}
		base := i18n.E("err.agentdeck.sendresult.2")
		coded := base
		if jsonErr == nil && rep.Delivery != "" {
			coded = i18n.E("err.agentdeck.sendresult.withdelivery", base, i18n.E("err.agentdeck.sendresult.delivery", rep.Delivery))
		}
		return &ErrDeliveryUnconfirmed{SessionID: sessionID, Detail: coded.Error(), coded: coded}
	}
	// --defer-if-busy 等到时限对方还忙：上游在打字之前就退出了，什么都没送
	if deferIfBusy && jsonErr == nil && strings.Contains(rep.Error, deferBusyMarker) {
		return ErrTargetBusy
	}
	// 上游失败时 --json 的输出里有 delivery 字段（上游 session_cmd.go:3056-3087）。
	// delivery=typed：正文到了会话画面，但没有证据说明对方接了这一轮——上游按失败退出（issue #1793），
	// 而 n040 终审实测到它是假阴性：评审会话其实收到并执行了 brief。这种情况单独报出来，
	// 让派工器不把会话当成没派出去丢掉（n042，n040 终审断点 5）。其余 delivery 仍是投递失败。
	if jsonErr == nil && rep.Delivery == DeliveryTyped {
		return &ErrDeliveryUnconfirmed{SessionID: sessionID, Detail: rep.Error, Reached: true}
	}
	return i18n.E("err.agentdeck.sendresult.1", sessionID, err)
}

// DeliveryTyped 是上游 session send --json 的 delivery 取值之一：正文到了画面、提交没得到确认。
const DeliveryTyped = "typed"

// ErrDeliveryUnconfirmed 表示上游没能证实对方接到了这条消息。分两种（n042 attempt 3 评审）：
//   - Reached 为真（delivery=typed）：正文到了会话画面，提交没得到确认。可能已经在跑（n040 实测），也可能还停在输入框。
//   - Reached 为假（delivery=unverified、回执缺 submitted 或不是 JSON）：连正文到没到画面都没证实。
//
// 两种 Work Deck 都分不清结果，所以不自动重发、不当成失败丢掉，而是照实记「送达未确认」、交给 owner 去看。
type ErrDeliveryUnconfirmed struct {
	SessionID string
	Detail    string
	Reached   bool
	// coded 是本地拼出来的详情。外部原文（delivery=typed 的 rep.Error）不放这里。
	coded *i18n.Error
}

func (e *ErrDeliveryUnconfirmed) Error() string { return e.I18n().Error() }

func (e *ErrDeliveryUnconfirmed) I18n() *i18n.Error {
	detail := any(e.Detail)
	if e.coded != nil {
		detail = e.coded
	}
	if e.Reached {
		return i18n.E("err.agentdeck.errdelivery.reached", e.SessionID, detail)
	}
	return i18n.E("err.agentdeck.errdelivery.missed", e.SessionID, detail)
}

// IsDeliveryUnconfirmed 判断一个错误是不是「送达未确认」。
func IsDeliveryUnconfirmed(err error) bool {
	var u *ErrDeliveryUnconfirmed
	return errors.As(err, &u)
}
