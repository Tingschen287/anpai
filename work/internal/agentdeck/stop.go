package agentdeck

import (
	"context"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"

	"github.com/Tingschen287/terminal-deck/work/internal/lease"
)

// StopSession 停掉一个会话（n044）。
//
// 只给派工器用：它刚起的会话没拿到任务说明时，停掉它，免得留下一个没人认领的会话。
// 和 SendMessage 一样要一张 lease.Permit：许可只有写入权记账能签发，owner 接管着的会话签不出来，
// 所以 owner 在里面时这里停不了。别人不经 Work Deck 连进会话，要靠 arbiter 停之前的客户端探测。
func (c *Client) StopSession(ctx context.Context, p lease.Permit, sessionID string) error {
	if sessionID == "" {
		return i18n.E("err.agentdeck.clientstopsession.1")
	}
	release, err := p.Begin(sessionID)
	if err != nil {
		return i18n.E("err.agentdeck.clientstopsession.2", err)
	}
	defer release()
	// 只收 Work Deck 自己的自动许可：停会话是派工器收拾自己刚起的空会话，不是 owner 的回答（n044 attempt 1 评审次要项）。
	if p.Kind() != lease.KindAutomatic {
		return i18n.E("err.agentdeck.clientstopsession.3", p.Kind())
	}
	if _, err := c.exec(ctx, "session", "stop", sessionID, "--json"); err != nil {
		return i18n.E("err.agentdeck.clientstopsession.4", sessionID, err)
	}
	return nil
}
