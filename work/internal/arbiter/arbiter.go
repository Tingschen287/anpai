// Package arbiter 把 adapter 的投递接到写入权记账上。
//
// adapter 的 SendMessage / DeliverAnswer 要一张 lease.Permit，本包负责去记账那里拿：
//   - Deck 给派工器用：launch 成功就为新会话登记 Work Deck 写入权；投递 brief 用「自动」许可。
//   - Deliverer 给决策队列用：owner 按 y 确认的回答用「手动」许可。
//
// 本包不自己判断能不能投，判断全在 lease 里；本包只决定用哪一种许可。
package arbiter

import (
	"context"
	"fmt"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/lease"
)

// Deck 实现 dispatch.Launcher。
type Deck struct {
	Client *agentdeck.Client
	Leases *lease.Store
}

// Launch 起一个新会话，成功后登记 Work Deck 写入权。
//
// 登记失败时会话已经起来了，但没有写入权：之后对它的自动投递一律被拒，返回的错误说明原因。
func (d Deck) Launch(ctx context.Context, req agentdeck.LaunchRequest) (agentdeck.LaunchResult, error) {
	if d.Leases == nil {
		return agentdeck.LaunchResult{}, fmt.Errorf("没有写入权记账，不派工")
	}
	res, err := d.Client.Launch(ctx, req)
	if err != nil {
		return res, err
	}
	if err := d.Leases.Grant(res.SessionID, "Work Deck 派出的新会话"); err != nil {
		return res, fmt.Errorf("会话 %s 已起来，但写入权登记失败，之后不会自动投递：%w", res.SessionID, err)
	}
	return res, nil
}

// SendMessage 用「自动」许可投递。
func (d Deck) SendMessage(ctx context.Context, sessionID, message string) error {
	if d.Leases == nil {
		return fmt.Errorf("没有写入权记账，不投递")
	}
	p, err := d.Leases.Automatic(sessionID)
	if err != nil {
		return fmt.Errorf("不自动投递：%w", err)
	}
	return d.Client.SendMessage(ctx, p, sessionID, message)
}

// Deliverer 实现 queue.Deliverer。
type Deliverer struct {
	Client *agentdeck.Client
	Leases *lease.Store
}

// DeliverAnswer 用「手动」许可投递 owner 的回答。
func (d Deliverer) DeliverAnswer(ctx context.Context, rec agentdeck.InboxRecord, message string) error {
	if d.Leases == nil {
		return fmt.Errorf("没有写入权记账，不投递")
	}
	p, err := d.Leases.Manual(rec.ChildSessionID)
	if err != nil {
		return fmt.Errorf("不投递：%w", err)
	}
	return d.Client.DeliverAnswer(ctx, p, rec, message)
}
