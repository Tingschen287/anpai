package lease

import (
	"sync"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
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

// Begin 在投递开始前核对许可，并把这个会话登记为「有投递在进行」，直到调用返回的 release。
//
// 在途期间 OwnerTakeover 会被拒（ErrDeliveryInProgress）：核过许可与消息真正送达之间，
// owner 不可能已经登记接管。release 必须调用，且只调一次。
func (p Permit) Begin(sessionID string) (release func(), err error) {
	if err := p.Allows(sessionID); err != nil {
		return nil, err
	}
	s := p.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkLocked(sessionID, p.kind, s.now()); err != nil {
		return nil, err
	}
	s.inflight[sessionID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.inflight[sessionID]--
			if s.inflight[sessionID] <= 0 {
				delete(s.inflight, sessionID)
			}
			s.mu.Unlock()
		})
	}, nil
}

// Allows 在投递那一刻核对许可仍然有效。零值 Permit 恒为无效。
func (p Permit) Allows(sessionID string) error {
	if p.store == nil {
		return i18n.E("err.lease.allows.none")
	}
	if p.sessionID != sessionID {
		return i18n.E("err.lease.allows.session", p.sessionID, sessionID)
	}
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	return p.store.checkLocked(sessionID, p.kind, p.store.now())
}

// Kind 是许可种类，供审计记录。
func (p Permit) Kind() string { return p.kind }

func (s *Store) checkLocked(sessionID, kind string, now time.Time) error {
	if s.broken != nil {
		return i18n.E("err.lease.check.broken", s.broken)
	}
	if sessionID == "" {
		return i18n.E("err.lease.check.nosession")
	}
	l, ok := s.leases[sessionID]
	switch kind {
	case KindAutomatic:
		switch {
		case !ok:
			return i18n.E("err.lease.check.norecord", sessionID)
		case l.Holder == HolderOwner && l.PendingHandback != nil:
			return i18n.E("err.lease.check.pending", sessionID, l.PendingHandback.ReturnedAt.Local().Format("01-02 15:04"))
		case l.Holder == HolderOwner:
			return i18n.E("err.lease.check.owner", sessionID, l.Since.Local().Format("01-02 15:04"))
		case l.Holder != HolderWorkbench:
			return i18n.E("err.lease.check.holderauto", sessionID, l.Holder)
		case l.ExpiresAt.IsZero() || !now.Before(l.ExpiresAt):
			return i18n.E("err.lease.check.expired", sessionID, l.ExpiresAt.Local().Format("01-02 15:04"))
		}
		return nil
	case KindManual:
		if !ok {
			return nil
		}
		switch {
		case l.PendingHandback != nil:
			return i18n.E("err.lease.check.manualpending", sessionID)
		case l.Holder == HolderOwner:
			return i18n.E("err.lease.check.manualowner", sessionID)
		case l.Holder != HolderWorkbench:
			return i18n.E("err.lease.check.holdermanual", sessionID, l.Holder)
		}
		return nil
	}
	return i18n.E("err.lease.check.kind", kind)
}
