package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/lease"
)

// leaseWiring 是写入权记账。store 为 nil 且 err 非空表示记账打不开：界面常驻告警。
type leaseWiring struct {
	store *lease.Store
	err   string
}

// attachContext 记下一次进入之前的样子，回来时拿来比。
type attachContext struct {
	sessionID  string
	via        string
	at         time.Time
	paneBefore string
	beforeErr  string
}

// armedHandback 是按了 H、等 y 确认的那一步。
type armedHandback struct {
	sessionID string
	// renew 为真表示这是续约一份过期租约，不是确认交还。
	renew bool
}

// WithLeases 接上写入权记账。
func (m Model) WithLeases(s *lease.Store) Model {
	m.lw = leaseWiring{store: s}
	return m
}

// WithLeasesError 记下记账打不开的原因。
func (m Model) WithLeasesError(err error) Model {
	m.lw = leaseWiring{err: err.Error()}
	return m
}

// beforeAttach 在交出终端之前登记 owner 接管，并记下此刻的画面。
//
// 登记失败时照样让 owner 进去：记账此时已停用（lease 包的 fail-closed），
// 所有自动投递都被拒，放行进入不会让自动投递撞上 owner。失败原因回来后告诉他。
func (m Model) beforeAttach(sessionID, via string) (Model, error) {
	ac := &attachContext{sessionID: sessionID, via: via, at: time.Now()}
	if m.attacher != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		pane, err := m.attacher.PaneOutputPlain(ctx, sessionID)
		cancel()
		if err != nil {
			ac.beforeErr = err.Error()
		} else {
			ac.paneBefore = pane
		}
	}
	if m.lw.store != nil {
		if err := m.lw.store.OwnerTakeover(sessionID, via); err != nil {
			var busy *lease.ErrDeliveryInProgress
			if errors.As(err, &busy) {
				// 有一条自动投递正在进行：此刻进去，消息可能在你进去之后才送达。不进。
				return m, err
			}
			ac.beforeErr = strings.TrimSpace(ac.beforeErr + "；登记接管失败：" + err.Error())
		}
	}
	m.attachCtx = ac
	return m, nil
}

// afterAttach 在 owner 回来后生成介入摘要，登记「等确认交还」。返回要追加给人看的一句话。
func (m Model) afterAttach(sessionID string, attachErr error) (Model, string) {
	ac := m.attachCtx
	m.attachCtx = nil
	if m.lw.store == nil {
		if m.lw.err != "" {
			return m, "写入权记账打不开（" + m.lw.err + "），自动投递一律停着"
		}
		return m, ""
	}
	hb := lease.Handback{ReturnedAt: time.Now(), Via: "unknown"}
	if ac != nil && ac.sessionID == sessionID {
		hb.AttachedAt, hb.Via = ac.at, ac.via
		if ac.beforeErr != "" {
			hb.Notes = append(hb.Notes, "进入前："+ac.beforeErr)
		}
		if ac.paneBefore != "" {
			hb.PaneBefore = digest(ac.paneBefore)
		}
	} else {
		hb.Notes = append(hb.Notes, "进入前的记录丢了，不知道何时进入、画面原样")
	}
	if !hb.AttachedAt.IsZero() {
		hb.DurationSeconds = int64(hb.ReturnedAt.Sub(hb.AttachedAt).Seconds())
	}
	if attachErr != nil {
		hb.AttachError = attachErr.Error()
	}
	if m.attacher != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		pane, err := m.attacher.PaneOutputPlain(ctx, sessionID)
		cancel()
		if err != nil {
			hb.Notes = append(hb.Notes, "回来后取画面失败："+err.Error())
		} else {
			hb.PaneAfter = digest(pane)
			hb.LastLines = lastLines(pane, 5)
		}
	}
	if hb.PaneBefore != "" && hb.PaneAfter != "" {
		changed := hb.PaneBefore != hb.PaneAfter
		hb.PaneChanged = &changed
	}
	if err := m.lw.store.OwnerReturn(sessionID, hb); err != nil {
		return m, "登记返回失败（" + err.Error() + "），这个会话的自动投递停着"
	}
	return m, "自动投递对它暂停，按 H 看介入摘要并确认交还"
}

// beginHandback 是按 H：找选中节点的会话，给出摘要或续约提示，等 y。
func (m Model) beginHandback() Model {
	if m.lw.store == nil {
		m.notice, m.noticeOK = "写入权记账没接上，没有可交还的会话", false
		return m
	}
	target := ""
	if node, ok := m.selected(); ok {
		if sid, err := m.sessionOf(node); err == nil {
			target = sid
		}
	}
	pending := m.lw.store.Pending()
	if target == "" && len(pending) == 1 {
		target = pending[0].SessionID
	}
	if target == "" {
		if len(pending) == 0 {
			m.notice, m.noticeOK = "没有等你确认交还的会话", true
		} else {
			m.notice, m.noticeOK = fmt.Sprintf("有 %d 个会话等确认交还，先用 ↑↓ 选中对应的节点再按 H", len(pending)), false
		}
		return m
	}
	l, ok := m.lw.store.Get(target)
	switch {
	case !ok:
		m.notice, m.noticeOK = "会话 "+target+" 没有写入权记录，不是 Work Deck 派出的，没有可交还或续约的", false
	case l.PendingHandback != nil:
		m.detail, m.detailOf = handbackText(target, *l.PendingHandback), "交还 "+target
		m.armed = armedHandback{sessionID: target}
		m.notice, m.noticeOK = "看完介入摘要，按 y 确认交还（自动投递恢复），其他键取消", true
	case l.Holder == lease.HolderOwner:
		m.notice, m.noticeOK = "会话 "+target+" 登记为你正在接管，还没回来，不能交还", false
	case !time.Now().Before(l.ExpiresAt):
		m.armed = armedHandback{sessionID: target, renew: true}
		m.notice, m.noticeOK = "会话 "+target+" 的租约已过期、自动投递停着。确认这期间没人在里面操作，按 y 续约，其他键取消", true
	default:
		m.notice, m.noticeOK = "会话 "+target+" 由 Work Deck 持有、租约有效，不需要交还", true
	}
	return m
}

// onArmedKey 处理 H 之后那一键。
func (m Model) onArmedKey(key string) Model {
	a := m.armed
	m.armed = armedHandback{}
	if key != "y" {
		m.notice, m.noticeOK = "已取消，自动投递仍停着", true
		return m
	}
	var err error
	if a.renew {
		err = m.lw.store.Renew(a.sessionID)
	} else {
		err = m.lw.store.ConfirmHandback(a.sessionID)
	}
	if err != nil {
		m.notice, m.noticeOK = "没记上："+err.Error(), false
		return m
	}
	m.detail, m.detailOf = "", ""
	if a.renew {
		m.notice, m.noticeOK = "已续约，会话 "+a.sessionID+" 的自动投递恢复", true
	} else {
		m.notice, m.noticeOK = "已确认交还，会话 "+a.sessionID+" 的自动投递恢复", true
	}
	return m
}

// leaseLines 是主界面上常驻的写入权提示。
func (m Model) leaseLines() []string {
	if m.lw.err != "" {
		return []string{"写入权记账打不开（" + m.lw.err + "）：所有自动投递都停着"}
	}
	if m.lw.store == nil {
		return nil
	}
	if err := m.lw.store.Broken(); err != nil {
		return []string{"写入权记账落盘失败已停用（" + err.Error() + "）：所有自动投递都停着，重启 Work Deck 前不会恢复"}
	}
	var out []string
	for _, l := range m.lw.store.Pending() {
		out = append(out, fmt.Sprintf("会话 %s 等你确认交还：自动投递对它暂停（选中对应节点按 H）", l.SessionID))
	}
	return out
}

func handbackText(sessionID string, h lease.Handback) string {
	var b strings.Builder
	fmt.Fprintf(&b, "介入摘要：会话 %s\n", sessionID)
	if !h.AttachedAt.IsZero() {
		fmt.Fprintf(&b, "进入 %s，回来 %s，共 %d 秒，方式 %s\n",
			h.AttachedAt.Local().Format("15:04:05"), h.ReturnedAt.Local().Format("15:04:05"), h.DurationSeconds, h.Via)
	} else {
		fmt.Fprintf(&b, "回来 %s；进入时间不知道\n", h.ReturnedAt.Local().Format("15:04:05"))
	}
	switch {
	case h.PaneChanged == nil:
		b.WriteString("画面有没有变：不知道（进入前或回来后没取到画面）\n")
	case *h.PaneChanged:
		b.WriteString("画面有变化：你在里面做过操作，或对方在这期间有输出\n")
	default:
		b.WriteString("画面没变化\n")
	}
	if h.AttachError != "" {
		b.WriteString("进入过程报错：" + h.AttachError + "\n")
	}
	for _, n := range h.Notes {
		b.WriteString("说明：" + n + "\n")
	}
	if len(h.LastLines) > 0 {
		b.WriteString("回来时画面最后几行：\n")
		for _, ln := range h.LastLines {
			b.WriteString("  " + ln + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func lastLines(s string, n int) []string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	var out []string
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			out = append([]string{lines[i]}, out...)
		}
	}
	return out
}
