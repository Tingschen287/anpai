// Package arbiter 把 adapter 的投递接到写入权记账上。
//
// adapter 的 SendMessage / DeliverAnswer 要一张 lease.Permit，本包负责去记账那里拿：
//   - Deck 给派工器用：launch 成功就为新会话登记 Work Deck 写入权；投递 brief 用「自动」许可；
//     刚起却没拿到任务说明的会话，用同一张许可停掉（n044）。
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

// ClientProbe 数一个会话此刻连着几个 tmux 客户端。adapter 的 AttachedClients 实现它。
type ClientProbe interface {
	AttachedClients(ctx context.Context, sessionID string) (int, error)
}

// Deck 实现 dispatch.Launcher。
type Deck struct {
	Client *agentdeck.Client
	Leases *lease.Store
	// Clients 用来发现不经 Work Deck 的接管。为 nil 时一律不自动投递。
	Clients ClientProbe
}

// Launch 起一个空会话，成功后登记 Work Deck 写入权。
//
// LaunchRequest 没有首条消息字段：launch 不往会话里写任何字。任务文本要等这里登记成功之后，
// 由调用方经 SendMessage 投递，所以登记失败时什么都没送出去。
//
// 登记失败时会话已经起来了，但没有写入权：之后对它的自动投递一律被拒，返回的错误说明原因。
func (d Deck) Launch(ctx context.Context, req agentdeck.LaunchRequest) (agentdeck.LaunchResult, error) {
	if d.Leases == nil {
		return agentdeck.LaunchResult{}, fmt.Errorf("没有写入权记账，不派工")
	}
	// 记账已停用时事后登记必然失败，起了也只是一个投不进去的空会话：先查，停用就不起。
	if err := d.Leases.Broken(); err != nil {
		return agentdeck.LaunchResult{}, fmt.Errorf("写入权记账已停用（%v），不派工", err)
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
	p, err := d.automaticPermit(ctx, sessionID, "不自动投递")
	if err != nil {
		return err
	}
	return d.Client.SendMessage(ctx, p, sessionID, message)
}

// Abandon 停掉派工器刚起、却没拿到任务说明的会话（n044）。
//
// 和自动投递同一道闸：要 Work Deck 持有写入权，还要查过此刻没人连着。有人连着就不停，
// 并按 owner 接管登记——那个会话已经有人在用了，停掉它等于替人动手。
// 停掉之后写入权记账里那一条照旧留着：会话已经不在了，留着的登记不会再放行任何投递。
func (d Deck) Abandon(ctx context.Context, sessionID string) error {
	if d.Leases == nil {
		return fmt.Errorf("没有写入权记账，不停会话")
	}
	p, err := d.automaticPermit(ctx, sessionID, "不停会话")
	if err != nil {
		return err
	}
	return d.Client.StopSession(ctx, p, sessionID)
}

// automaticPermit 是自动投递与停会话共用的闸：签「自动」许可，再探测有没有人不经 Work Deck 连着。
// verb 是拒绝时的开头（「不自动投递」「不停会话」）。
func (d Deck) automaticPermit(ctx context.Context, sessionID, verb string) (lease.Permit, error) {
	p, err := d.Leases.Automatic(sessionID)
	if err != nil {
		return lease.Permit{}, fmt.Errorf("%s：%w", verb, err)
	}
	// 记账只知道经 Work Deck 的接管。别人直接进入会话，tmux 上会多一个客户端：
	// 查到有人连着就按 owner 接管处理（登记，此后要 owner 确认交还才恢复）；查不了也不动。
	if d.Clients == nil {
		return lease.Permit{}, fmt.Errorf("%s：没有接入探测，确认不了此刻没人在会话 %s 里", verb, sessionID)
	}
	n, err := d.Clients.AttachedClients(ctx, sessionID)
	if err != nil {
		return lease.Permit{}, fmt.Errorf("%s：查不了有没有人连着会话 %s（%v）", verb, sessionID, err)
	}
	if n > 0 {
		reason := fmt.Sprintf("检测到 %d 个 tmux 客户端连着会话（不经 Work Deck 的接管）", n)
		if terr := d.Leases.ExternalTakeover(sessionID, n); terr != nil {
			return lease.Permit{}, fmt.Errorf("%s：%s；登记接管也失败：%v", verb, reason, terr)
		}
		return lease.Permit{}, fmt.Errorf("%s：%s，已按 owner 接管登记，要你确认交还后才恢复", verb, reason)
	}
	return p, nil
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
