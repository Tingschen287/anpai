// Package viewstate 是中间窗格的状态机：打开集合与焦点、只读开关、接管与自动交还、给主 agent 汇报的送达门
// （n050，架构稿 §2、§7、§14.1、§14.4）。
//
// 真实的 tmux 操作由 n049 实现 Center 接口；这里只定顺序与失败语义，并用模拟的中间窗格测透：
//   - 任何改变中间目标的动作，先把中间 client 设为只读并读回确认，再切换，再按新目标决定是否放开输入；
//     只读设置或读回失败就不切换。
//   - 接管请求绑定（目标、布局代次、client 代次），放开输入前再比对一次，切走或重建就作废。
//   - 离开被接管的会话：先确认只读，再记摘要、自动交还（lease 的 auto_handback）。交还失败就保持暂停并提示。
//   - 主 agent 会话：第一条 brief 确认送达后才可写；子 agent 会话默认只读。
//   - 给主 agent 送汇报：owner 30 秒内在它那里打过字就排队；真正送的那一刻先关输入，送完再放开。
package viewstate

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/lease"
)

// Idle 是 agent-deck server 上的空白占位会话。
const Idle = "deck-idle"

// Center 是中间窗格的嵌套 client（n049 用 tmux 实现）。
type Center interface {
	SetReadOnly(ro bool) error
	ReadOnly() (bool, error)
	Switch(tmuxSession string) error
	LastInput() (time.Time, error) // tmux client_activity：owner 最后一次在中间打字的时间
}

// Leases 是写入权记账（lease.Store 满足它）。
type Leases interface {
	OwnerTakeover(sessionID, via string) error
	AutoHandback(sessionID string, hb lease.Handback) error
}

// Target 是一个可显示的会话。
type Target struct {
	Session        string // agent-deck 会话 id（lease 的键）
	Tmux           string // tmux session 名（中间 client 切换用）
	Role           string // main 或 sub
	BriefConfirmed bool   // 主 agent 的第一条 brief 已确认送达
	Title          string
}

// Lookup 按会话 id 取目标信息；找不到返回 false。
type Lookup func(session string) (Target, bool)

// 错误。
var (
	ErrInputNotClosed = errors.New("中间窗格的输入关不上（只读设置或读回失败），不切换；中间显示「输入已关闭（异常）」")
	ErrNotOpen        = errors.New("会话不在打开集合里")
	ErrNotFocused     = errors.New("只能接管当前显示的会话")
	ErrNotSub         = errors.New("只有子 agent 需要接管；主 agent 会话本来就可写")
	ErrTakeoverStale  = errors.New("接管请求已作废：等待期间你切走了，或中间窗格重建了")
	ErrTakeoverBusy   = errors.New("该会话的自动派送 10 秒内没有完成，接管失败，保持只读")
	ErrOwnerTyping    = errors.New("你最近在主 agent 那里打过字，汇报先排队")
)

// Machine 是中间窗格状态机。所有方法串行执行。
type Machine struct {
	mu        sync.Mutex
	center    Center
	leases    Leases
	lookup    Lookup
	notify    func(text string)
	now       func() time.Time
	sleep     func(time.Duration)
	open      []string
	focus     string
	layoutGen int
	clientGen int
	taken     map[string]time.Time // 被 owner 接管的会话 → 接管时刻
	// TakeoverWait 是接管时等待在途派送完成的上限。
	TakeoverWait time.Duration
	// OwnerIdle 是送汇报前要求 owner 未打字的时长。
	OwnerIdle time.Duration
}

// New 建状态机。中间 client 初始假定为只读、显示 Idle（n049 建 client 时 attach -r）。
func New(center Center, leases Leases, lookup Lookup, notify func(string)) *Machine {
	if notify == nil {
		notify = func(string) {}
	}
	return &Machine{center: center, leases: leases, lookup: lookup, notify: notify, now: time.Now, sleep: time.Sleep,
		taken: map[string]time.Time{}, TakeoverWait: 10 * time.Second, OwnerIdle: 30 * time.Second}
}

// SetClock 供测试替换时间与等待。
func (m *Machine) SetClock(now func() time.Time, sleep func(time.Duration)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now, m.sleep = now, sleep
}

// State 是对外可见的状态。
type State struct {
	Open      []string `json:"open"`
	Focus     string   `json:"focus"`
	TakenOver []string `json:"taken_over"`
	LayoutGen int      `json:"layout_gen"`
	ClientGen int      `json:"client_gen"`
}

// State 返回当前状态。
func (m *Machine) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := State{Open: append([]string(nil), m.open...), Focus: m.focus, LayoutGen: m.layoutGen, ClientGen: m.clientGen}
	for s := range m.taken {
		st.TakenOver = append(st.TakenOver, s)
	}
	return st
}

// Open 打开一个会话并让它成为焦点。
func (m *Machine) Open(session string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.lookup(session); !ok {
		return fmt.Errorf("没有会话 %s", session)
	}
	if err := m.switchLocked(session); err != nil {
		return err
	}
	if !contains(m.open, session) {
		m.open = append(m.open, session)
	}
	return nil
}

// Focus 让一个已打开的会话成为焦点。
func (m *Machine) Focus(session string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !contains(m.open, session) {
		return ErrNotOpen
	}
	return m.switchLocked(session)
}

// Hide 把会话放回后台（标签条、侧栏眼睛、中间 Ctrl-Q 都走这里）。被移出的若是焦点，焦点移到上一个打开的会话，
// 没有就显示 Idle。隐藏非焦点会话不影响中间，但它若被接管着，同样自动交还。
func (m *Machine) Hide(session string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !contains(m.open, session) {
		return ErrNotOpen
	}
	rest := remove(m.open, session)
	if m.focus == session {
		next := ""
		if len(rest) > 0 {
			next = rest[len(rest)-1]
		}
		if err := m.switchLocked(next); err != nil {
			return err // 关不上输入：不改打开集合
		}
	} else if _, ok := m.taken[session]; ok {
		m.handbackLocked(session, "隐藏")
	}
	m.open = rest
	return nil
}

// CenterReset 在中间 client 断开或重建后调用（例如 Ctrl-Q）：client 代次 +1，之前的接管请求全部作废；
// 焦点会话按隐藏处理。
func (m *Machine) CenterReset() error {
	m.mu.Lock()
	m.clientGen++
	focus := m.focus
	m.mu.Unlock()
	if focus == "" {
		return nil
	}
	return m.Hide(focus)
}

// switchLocked：先只读并确认 → 被接管的旧焦点自动交还 → 切换 → 按新目标放开输入。
func (m *Machine) switchLocked(target string) error {
	m.layoutGen++
	if err := m.closeInputLocked(); err != nil {
		return err
	}
	if old := m.focus; old != "" && old != target {
		if _, ok := m.taken[old]; ok {
			m.handbackLocked(old, "切走")
		}
	}
	tmux := Idle
	var t Target
	if target != "" {
		var ok bool
		t, ok = m.lookup(target)
		if !ok {
			return fmt.Errorf("没有会话 %s", target)
		}
		tmux = t.Tmux
	}
	if err := m.center.Switch(tmux); err != nil {
		return fmt.Errorf("切换中间窗格失败（输入保持关闭）：%w", err)
	}
	m.focus = target
	if target != "" && t.Role == "main" && t.BriefConfirmed {
		if err := m.center.SetReadOnly(false); err != nil {
			return fmt.Errorf("主 agent 会话放开输入失败（保持只读）：%w", err)
		}
	}
	return nil
}

func (m *Machine) closeInputLocked() error {
	if err := m.center.SetReadOnly(true); err != nil {
		m.notify("中间窗格输入已关闭（异常）：设置只读失败")
		return ErrInputNotClosed
	}
	ro, err := m.center.ReadOnly()
	if err != nil || !ro {
		m.notify("中间窗格输入已关闭（异常）：读回只读状态失败")
		return ErrInputNotClosed
	}
	return nil
}

// Takeover 接管当前显示的子 agent：等在途派送完成，拿到 owner 写入权，再比对代次，最后才放开输入。
func (m *Machine) Takeover(session string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.focus != session {
		return ErrNotFocused
	}
	t, ok := m.lookup(session)
	if !ok {
		return fmt.Errorf("没有会话 %s", session)
	}
	if t.Role != "sub" {
		return ErrNotSub
	}
	reqLayout, reqClient := m.layoutGen, m.clientGen
	deadline := m.now().Add(m.TakeoverWait)
	for {
		err := m.leases.OwnerTakeover(session, "deck")
		if err == nil {
			break
		}
		var busy *lease.ErrDeliveryInProgress
		if !errors.As(err, &busy) {
			return fmt.Errorf("登记接管失败（保持只读）：%w", err)
		}
		if !m.now().Before(deadline) {
			return ErrTakeoverBusy
		}
		// 等待时放开锁：切走、隐藏、重建都能发生，回来后靠代次判定
		m.mu.Unlock()
		m.sleep(100 * time.Millisecond)
		m.mu.Lock()
	}
	since := m.now()
	m.taken[session] = since
	if m.layoutGen != reqLayout || m.clientGen != reqClient || m.focus != session {
		m.handbackLocked(session, "接管请求作废")
		return ErrTakeoverStale
	}
	if err := m.center.SetReadOnly(false); err != nil {
		m.handbackLocked(session, "放开输入失败")
		return fmt.Errorf("放开输入失败，已交还：%w", err)
	}
	m.notify(fmt.Sprintf("你接管了 %s：它的自动派送已暂停", title(t)))
	return nil
}

// handbackLocked 自动交还。调用前输入必须已经关闭。失败就保持暂停（lease 仍由 owner 持有）并提示。
func (m *Machine) handbackLocked(session, why string) {
	since := m.taken[session]
	t, _ := m.lookup(session)
	now := m.now()
	hb := lease.Handback{AttachedAt: since, ReturnedAt: now, DurationSeconds: int64(now.Sub(since).Seconds()), Via: "deck",
		Notes: []string{"离开方式：" + why}}
	if err := m.leases.AutoHandback(session, hb); err != nil {
		m.notify(fmt.Sprintf("%s 自动交还失败，自动派送保持暂停：%v", title(t), err))
		return
	}
	delete(m.taken, session)
	m.notify(fmt.Sprintf("你离开了 %s（%s），接管 %d 秒，已自动交还", title(t), why, hb.DurationSeconds))
}

// DeliverToMain 给主 agent 送一条汇报（n051 调）。deliver 真正发送（agent-deck send --defer-if-busy）。
// 主 agent 正显示在中间、owner 在 OwnerIdle 内打过字：返回 ErrOwnerTyping，调用方排队。
// 正显示在中间：送前先关输入并确认，送完按 brief 状态放开。不在中间：直接送。
func (m *Machine) DeliverToMain(session string, deliver func() error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.lookup(session)
	if !ok || t.Role != "main" {
		return fmt.Errorf("%s 不是主 agent 会话", session)
	}
	if m.focus != session {
		return deliver()
	}
	ro, err := m.center.ReadOnly()
	if err != nil {
		return ErrInputNotClosed
	}
	if !ro {
		last, err := m.center.LastInput()
		if err != nil {
			return fmt.Errorf("读不到你最后的输入时间，先不送：%w", err)
		}
		if m.now().Sub(last) < m.OwnerIdle {
			return ErrOwnerTyping
		}
	}
	if err := m.closeInputLocked(); err != nil {
		return err
	}
	derr := deliver()
	if t.BriefConfirmed {
		if err := m.center.SetReadOnly(false); err != nil {
			m.notify("送完汇报后放开主 agent 输入失败，中间保持只读")
		}
	}
	return derr
}

func title(t Target) string {
	if t.Title != "" {
		return t.Title
	}
	return t.Session
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func remove(xs []string, x string) []string {
	var out []string
	for _, y := range xs {
		if y != x {
			out = append(out, y)
		}
	}
	return out
}
