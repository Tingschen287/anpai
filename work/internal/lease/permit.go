package lease

import (
	"fmt"
	"time"
)

// 许可种类。
const (
	// KindAutomatic：Work Deck 自己发起的投递，例如派评审后投递 brief。
	KindAutomatic = "automatic"
	// KindManual：owner 在界面上明确做出的回答，例如决策队列里按 y 确认的选项。
	KindManual = "manual"
)

// Permit 是一次投递的许可。
//
// 字段全部不导出：别的包只能拿到 Store 签发的 Permit，自己构造的零值 Allows 恒为假。
// Allows 在使用时回头核对 Store 的当前记账——签发后 owner 接管了，这张 Permit 立刻失效。
type Permit struct {
	store     *Store
	sessionID string
	kind      string
}

// Automatic 为 Work Deck 自己发起的投递签发许可。
//
// 只有一种情形放行：会话登记为 Work Deck 持有、租约未过期、没有待确认的交还、记账没停用。
// 其余一律拒绝，返回的错误就是原因。
func (s *Store) Automatic(sessionID string) (Permit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkLocked(sessionID, KindAutomatic, s.now()); err != nil {
		return Permit{}, err
	}
	return Permit{store: s, sessionID: sessionID, kind: KindAutomatic}, nil
}

// Manual 为 owner 在界面上明确做出的回答签发许可。
//
// owner 本人就是写入者，所以没有记录的会话（例如 conductor 的子会话）照样放行；
// 但会话登记为 owner 接管中或等确认交还时拒绝——那说明 owner 正在别处操作它，
// 或刚操作完还没确认，这时往里再塞一句话会和他自己的操作打架。
func (s *Store) Manual(sessionID string) (Permit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkLocked(sessionID, KindManual, s.now()); err != nil {
		return Permit{}, err
	}
	return Permit{store: s, sessionID: sessionID, kind: KindManual}, nil
}

// Allows 在投递那一刻核对许可仍然有效。零值 Permit 恒为无效。
func (p Permit) Allows(sessionID string) error {
	if p.store == nil {
		return fmt.Errorf("没有投递许可：只有写入权记账能签发许可，自己构造的不算")
	}
	if p.sessionID != sessionID {
		return fmt.Errorf("投递许可是给会话 %s 的，不能用于会话 %s", p.sessionID, sessionID)
	}
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	return p.store.checkLocked(sessionID, p.kind, p.store.now())
}

// Kind 是许可种类，供审计记录。
func (p Permit) Kind() string { return p.kind }

func (s *Store) checkLocked(sessionID, kind string, now time.Time) error {
	if s.broken != nil {
		return fmt.Errorf("写入权记账已停用（%v），一律不投递", s.broken)
	}
	if sessionID == "" {
		return fmt.Errorf("没有会话号，不投递")
	}
	l, ok := s.leases[sessionID]
	switch kind {
	case KindAutomatic:
		switch {
		case !ok:
			return fmt.Errorf("会话 %s 没有写入权记录：不是 Work Deck 派出、或记录丢失，持有者未知，不自动投递", sessionID)
		case l.Holder == HolderOwner && l.PendingHandback != nil:
			return fmt.Errorf("会话 %s 等你确认交还（%s 回来），确认前不自动投递", sessionID, l.PendingHandback.ReturnedAt.Local().Format("01-02 15:04"))
		case l.Holder == HolderOwner:
			return fmt.Errorf("会话 %s 正由 owner 接管（%s 起），不自动投递", sessionID, l.Since.Local().Format("01-02 15:04"))
		case l.Holder != HolderWorkbench:
			return fmt.Errorf("会话 %s 的持有者 %q 不认识，不自动投递", sessionID, l.Holder)
		case l.ExpiresAt.IsZero() || !now.Before(l.ExpiresAt):
			return fmt.Errorf("会话 %s 的租约已于 %s 过期，需要 owner 确认续约后才自动投递", sessionID, l.ExpiresAt.Local().Format("01-02 15:04"))
		}
		return nil
	case KindManual:
		if !ok {
			return nil
		}
		switch {
		case l.PendingHandback != nil:
			return fmt.Errorf("会话 %s 你刚接管完还没确认交还，先看介入摘要并确认（按 H）", sessionID)
		case l.Holder == HolderOwner:
			return fmt.Errorf("会话 %s 登记为你正在接管，先回来再回答", sessionID)
		case l.Holder != HolderWorkbench:
			return fmt.Errorf("会话 %s 的持有者 %q 不认识，不投递", sessionID, l.Holder)
		}
		return nil
	}
	return fmt.Errorf("许可种类 %q 不认识，不投递", kind)
}
