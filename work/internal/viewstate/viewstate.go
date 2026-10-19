// Package viewstate 是中间窗格的状态机：打开集合与焦点、只读开关、接管与自动交还、给主 agent 汇报的送达门
// （n050，架构稿 §2、§7、§14.1、§14.4；第 2 次实现按 Codex 评审 B3–B5、第 3 次按 R2、R3、R5 修订）。
//
// 真实的 tmux 操作由 n049 实现 Center 接口；这里只定顺序与失败语义，并用模拟的中间窗格测透：
//   - 会话标识在入口统一成 Target.Session（agent-deck 会话 id）；焦点、lease、汇报门都只用它。
//   - 任何改变中间目标的动作，先把中间 client 设为只读并读回确认，再切换，再按新目标决定是否放开输入；
//     只读设置或读回失败就不切换。
//   - 接管有两种状态：可写（输入在 owner 手上）与等待交还（输入已关闭、lease 仍是 owner、自动派送暂停）。
//     对等待交还的会话再按接管，只需重新放开输入。
//   - 接管：每次等待醒来先比对（目标、布局代次、client 代次），不一致就作废——作废的请求从未拿到写入权，
//     不会去动别人的；拿到写入权到放开输入之间不放锁。放开失败时先确认输入关闭，关不上就保持接管与暂停。
//   - 自动交还：输入已确认关闭 → 取画面摘要 → 写「正在交还」通知（落盘）→ 释放 lease，完整摘要随 lease 事件
//     同一次落盘。取不到摘要、写不进通知、lease 落盘失败，都保持接管与暂停，可经 RetryHandback 重试。
//     会话不在中间时取不了画面：用第一次取到的摘要；从没取到过的，只有重启后恢复的接管可以不带画面交还。
//   - 存储停止（deck 存储或 lease 存储任一停止）时，一切会写 lease 或改变输入状态的操作都拒绝；
//     操作进行中发现停止（例如交还失败把存储弄停了），不再切换、不再放开输入。
//   - 重启：从 lease 存储恢复 owner 持有的会话，作为等待交还。
//   - 主 agent 会话：第一条 brief 确认送达后才可写；子 agent 会话默认只读。
//   - 给主 agent 送汇报：owner 30 秒内在它那里打过字就排队；真正送的那一刻先关输入，送完再放开。
package viewstate

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
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
	// Capture 抓指定 tmux 会话画面的最后几行（交还摘要用）。按名字抓、不按中间 client 当前显示的：
	// client 重建后中间显示的是 deck-idle，被交还的会话已不在 client 上（n049 评审 1 B2）。
	Capture(tmuxSession string, lines int) ([]string, error)
	// ClientID 是中间现在那个 client 的标识（进程号）。reset、resync 据此核对通知是不是它发的（n049 评审 1 B1）。
	ClientID() (int, error)
	// Bind 把之后的 SetReadOnly、ReadOnly、Switch、LastInput 绑定到这个 client：它被替换或不在了，这些调用返回
	// ErrStaleClient、不作用到新 client 上（重建的 client 与旧的共用中间窗格的 tty，按 tty 找 client 分不出新旧，
	// n049 评审 2 B1）。Bind(0) 解除。状态机只在持锁时调用。
	Bind(client int)
}

// NoClient 用于 Bind：操作开始时中间没有 client，之后出现的 client 都不是这次操作的。
const NoClient = -1

// ErrStaleClient 表示操作绑定的中间 client 已被替换或不在了（reset、resync 的通知来自旧 client 也是它）：
// 不作用到新 client 上。
var ErrStaleClient = &codeErr{text: "通知来自已经不在中间的旧 client", key: "err.viewstate.stale"}

// ErrRestoreUnconfirmed 表示命令落到了共用 tty 的新 client 上，且没能确认把新 client 还原成起点（deck-idle、只读）：
// 新 client 可能停在焦点会话上、输入开着。总是与 ErrStaleClient 一起出现。遇到它不交还写入权、不恢复自动派送
// （n049 评审 4，n051 承接）。
var ErrRestoreUnconfirmed = &codeErr{text: "落到新 client 上的命令没能确认还原：新 client 的输入状态不确定", key: "err.viewstate.restore"}

// ErrPartial 表示操作做到一半停下，但已经写下了不可撤销的部分（lease 交还已落盘），或中间已经切了过去。
// 状态按操作的目标补齐（焦点、打开集合一致），没做完的是 Cause（例如中间 client 被替换、放开输入失败）。
// 调用方按「已完成、但有后续问题」处理；中间 client 被替换时由新 client 的重连通知收尾（n049 评审 3 B1-B）。
type ErrPartial struct {
	Done  string
	Cause error
	done  *i18n.Error
}

func (e *ErrPartial) Error() string { return "部分完成（" + e.Done + "）：" + e.Cause.Error() }
func (e *ErrPartial) Unwrap() error { return e.Cause }

func (e *ErrPartial) I18n() *i18n.Error {
	done := any(e.Done)
	if e.done != nil {
		done = e.done
	}
	return i18n.E("err.viewstate.partial", done, e.Cause)
}

func isPartial(err error) bool {
	var pe *ErrPartial
	return errors.As(err, &pe)
}

// committed：操作的状态已按目标提交（成功、交还失败但切换完成、部分完成）。
func committed(err error) bool { return err == nil || isHandback(err) || isPartial(err) }

// Leases 是写入权记账（lease.Store 满足它）。
type Leases interface {
	OwnerTakeover(sessionID, via string) error
	AutoHandback(sessionID string, hb lease.Handback) error
	OwnerHeld() []lease.Lease
	Broken() error
}

// Target 是一个可显示的会话。
type Target struct {
	Session        string // agent-deck 会话 id：焦点、lease、汇报门统一用它
	Tmux           string // tmux session 名（中间 client 切换用）
	Role           string // main 或 sub
	BriefConfirmed bool   // 主 agent 的第一条 brief 已确认送达
	// AwaitingOwner：主 agent 的 CLI 停在启动确认上（目录信任等），等 owner 在中间处理（n055）。这时也对 owner 放开输入
	AwaitingOwner bool
	Title         string
}

// ownerInput：主 agent 显示在中间时 owner 能不能打字——第一条 brief 已送达，或 CLI 停在启动确认上等 owner 处理。
func (t Target) ownerInput() bool { return t.Role == "main" && (t.BriefConfirmed || t.AwaitingOwner) }

// Lookup 按任意标识（注册表 key 或 agent-deck id）取目标；找不到返回 false。
type Lookup func(id string) (Target, bool)

// 错误。
var (
	ErrInputNotClosed = &codeErr{text: "中间窗格的输入关不上（只读设置或读回失败），不切换；中间显示「输入已关闭（异常）」", key: "err.viewstate.inputnotclosed"}
	ErrNotOpen        = &codeErr{text: "会话不在打开集合里", key: "err.viewstate.notopen"}
	ErrNotFocused     = &codeErr{text: "只能接管当前显示的会话", key: "err.viewstate.notfocused"}
	ErrNotSub         = &codeErr{text: "只有子 agent 需要接管；主 agent 会话本来就可写", key: "err.viewstate.notsub"}
	ErrTakeoverStale  = &codeErr{text: "接管请求已作废：等待期间你切走了，或中间窗格重建了", key: "err.viewstate.takeoverstale"}
	ErrTakeoverBusy   = &codeErr{text: "该会话的自动派送 10 秒内没有完成，接管失败，保持只读", key: "err.viewstate.takeoverbusy"}
	ErrOwnerTyping    = &codeErr{text: "你最近在主 agent 那里打过字，汇报先排队", key: "err.viewstate.ownertyping"}
	ErrNotTaken       = &codeErr{text: "这个会话没有在接管中", key: "err.viewstate.nottaken"}
)

// codeErr 是哨兵错误。变量身份不变，所以 == 和 errors.Is 照旧。不提供 Unwrap。
type codeErr struct {
	text string
	key  i18n.Key
}

func (e *codeErr) Error() string {
	if e == nil {
		return ""
	}
	return e.text
}

func (e *codeErr) I18n() *i18n.Error {
	// 每个码都写成字面量调用。检查器不把结构体字段里的字符串当成「有人用」。
	switch e.key {
	case "err.viewstate.stale":
		return i18n.E("err.viewstate.stale")
	case "err.viewstate.restore":
		return i18n.E("err.viewstate.restore")
	case "err.viewstate.inputnotclosed":
		return i18n.E("err.viewstate.inputnotclosed")
	case "err.viewstate.notopen":
		return i18n.E("err.viewstate.notopen")
	case "err.viewstate.notfocused":
		return i18n.E("err.viewstate.notfocused")
	case "err.viewstate.notsub":
		return i18n.E("err.viewstate.notsub")
	case "err.viewstate.takeoverstale":
		return i18n.E("err.viewstate.takeoverstale")
	case "err.viewstate.takeoverbusy":
		return i18n.E("err.viewstate.takeoverbusy")
	case "err.viewstate.ownertyping":
		return i18n.E("err.viewstate.ownertyping")
	case "err.viewstate.nottaken":
		return i18n.E("err.viewstate.nottaken")
	case "err.viewstate.closed":
		return i18n.E("err.viewstate.closed")
	default:
		return i18n.E(e.key)
	}
}

// ErrHandback 表示交还失败：会话保持接管、自动派送保持暂停，可用 RetryHandback 重试。
type ErrHandback struct {
	Session string
	Cause   error
}

func (e *ErrHandback) Error() string {
	return fmt.Sprintf("%s 交还失败，保持接管、自动派送暂停（可重试交还）：%v", e.Session, e.Cause)
}

func (e *ErrHandback) Unwrap() error { return e.Cause }

func (e *ErrHandback) I18n() *i18n.Error {
	return i18n.E("err.viewstate.handback", e.Session, e.Cause)
}

// taken 是一个 owner 持有 lease 的会话。
type taken struct {
	since    time.Time
	writable bool            // 输入此刻在 owner 手上；false 表示等待交还
	summary  *lease.Handback // 第一次取到的画面摘要（交还失败后重试时用）
	restored bool            // 重启后从 lease 存储恢复的
}

// Machine 是中间窗格状态机。所有方法串行执行。
type Machine struct {
	mu        sync.Mutex
	center    Center
	leases    Leases
	lookup    Lookup
	notify    func(*i18n.Error) error
	stopped   func() error
	now       func() time.Time
	sleep     func(time.Duration)
	open      []string
	focus     string
	layoutGen int
	clientGen int
	closed    bool // Close 之后不再接受操作
	taken     map[string]taken
	// TakeoverWait 是接管时等待在途派送完成的上限。
	TakeoverWait time.Duration
	// OwnerIdle 是送汇报前要求 owner 未打字的时长。
	OwnerIdle time.Duration
}

// New 建状态机。中间 client 初始假定为只读、显示 Idle（n049 建 client 时 attach -r）。
// notify 必须把通知落盘并返回错误；stopped 返回 deck 存储的停止原因（nil 表示正常；lease 存储的停止由 Leases.Broken 给出）。
// lease 存储里 owner 持有的会话恢复为等待交还（n050 评审 R5）。
func New(center Center, leases Leases, lookup Lookup, notify func(*i18n.Error) error, stopped func() error) *Machine {
	if notify == nil {
		notify = func(*i18n.Error) error { return nil }
	}
	if stopped == nil {
		stopped = func() error { return nil }
	}
	m := &Machine{center: center, leases: leases, lookup: lookup, notify: notify, stopped: stopped, now: time.Now, sleep: time.Sleep,
		taken: map[string]taken{}, TakeoverWait: 10 * time.Second, OwnerIdle: 30 * time.Second}
	for _, l := range leases.OwnerHeld() {
		m.taken[l.SessionID] = taken{since: l.Since, restored: true}
	}
	return m
}

// SetClock 供测试替换时间与等待。
func (m *Machine) SetClock(now func() time.Time, sleep func(time.Duration)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now, m.sleep = now, sleep
}

// State 是对外可见的状态。
type State struct {
	Open            []string `json:"open"`
	Focus           string   `json:"focus"`
	TakenOver       []string `json:"taken_over"`       // owner 持有 lease 的全部会话
	HandbackPending []string `json:"handback_pending"` // 其中等待交还（输入已关闭）的
	LayoutGen       int      `json:"layout_gen"`
	ClientGen       int      `json:"client_gen"`
}

// State 返回当前状态。
func (m *Machine) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := State{Open: append([]string(nil), m.open...), Focus: m.focus, LayoutGen: m.layoutGen, ClientGen: m.clientGen}
	for s, tk := range m.taken {
		st.TakenOver = append(st.TakenOver, s)
		if !tk.writable {
			st.HandbackPending = append(st.HandbackPending, s)
		}
	}
	sort.Strings(st.TakenOver)
	sort.Strings(st.HandbackPending)
	return st
}

// canon 把任意标识统一成 Target.Session。
func (m *Machine) canon(id string) (Target, error) {
	t, ok := m.lookup(id)
	if !ok || t.Session == "" {
		return Target{}, i18n.E("err.viewstate.nosession", id)
	}
	return t, nil
}

// writable 汇总两个存储的停止状态（n050 评审 R3）。入口查一次；操作中途做了可能失败的写之后、
// 切换或放开输入之前再查一次。
func (m *Machine) writable() error {
	if m.closed {
		return ErrClosed
	}
	if err := m.stopped(); err != nil {
		return i18n.E("err.viewstate.stopped", err)
	}
	if err := m.leases.Broken(); err != nil {
		return i18n.E("err.viewstate.leasebroken", err)
	}
	return nil
}

// Stopped 返回 deck 存储或写入权记账的停止原因（nil 表示都正常），供 HTTP 层映射成 503。
func (m *Machine) Stopped() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writable()
}

// Open 打开一个会话并让它成为焦点。
func (m *Machine) Open(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.writable(); err != nil {
		return err
	}
	defer m.bind()()
	t, err := m.canon(id)
	if err != nil {
		return err
	}
	herr := m.switchLocked(t.Session)
	if !committed(herr) {
		return herr
	}
	if !contains(m.open, t.Session) {
		m.open = append(m.open, t.Session)
	}
	return herr
}

// Focus 让一个已打开的会话成为焦点。
func (m *Machine) Focus(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.writable(); err != nil {
		return err
	}
	defer m.bind()()
	t, err := m.canon(id)
	if err != nil {
		return err
	}
	if !contains(m.open, t.Session) {
		return ErrNotOpen
	}
	return m.switchLocked(t.Session)
}

// Hide 把会话放回后台（标签条、侧栏眼睛、中间 Ctrl-Q 都走这里）。被移出的若是焦点，焦点移到上一个打开的会话，
// 没有就显示 Idle。隐藏非焦点会话不影响中间；它若被接管着（输入早已不在它那里），同样交还。
// 交还失败时会话仍移出打开集合，但保持接管与暂停，返回 *ErrHandback。
func (m *Machine) Hide(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.writable(); err != nil {
		return err
	}
	defer m.bind()()
	return m.hideLocked(id)
}

func (m *Machine) hideLocked(id string) error {
	if id == "" {
		// 空串表示「中间正显示的那个」：由状态机在锁内取焦点。界面手里的焦点可能是一秒前的，
		// 拿它去隐藏或接管会作用到别的会话上（n049 真机演示：Ctrl-Q 隐藏了上一个会话）
		if m.focus == "" {
			return ErrNotOpen
		}
		id = m.focus
	}
	t, err := m.canon(id)
	if err != nil {
		return err
	}
	session := t.Session
	if !contains(m.open, session) {
		return ErrNotOpen
	}
	rest := remove(m.open, session)
	var herr error
	if m.focus == session {
		next := ""
		if len(rest) > 0 {
			next = rest[len(rest)-1]
		}
		herr = m.switchLocked(next)
		if !committed(herr) {
			return herr // 关不上输入、或中途 client 被替换而没写下任何东西：不改打开集合
		}
	} else if _, ok := m.taken[session]; ok {
		herr = m.handbackLocked(session, i18n.E("err.viewstate.why.hide"))
	}
	m.open = rest
	return herr
}

// CenterReset 在中间 client 断开或重建后调用（例如 Ctrl-Q）：client 代次 +1，之前等待中的接管请求全部作废；
// 焦点会话按隐藏处理（新 client 由 n049 以只读方式建好后再调这里）。client 是发通知的那个 client：
// 它已不是中间现在的 client 就返回 ErrStaleClient、什么都不做。核对与隐藏在同一把锁里。
func (m *Machine) CenterReset(client int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.writable(); err != nil {
		return err
	}
	unbind, err := m.bindTo(client)
	if err != nil {
		return err
	}
	defer unbind()
	if m.focus != "" {
		if err := m.hideLocked(m.focus); err != nil {
			if errors.Is(err, ErrStaleClient) && !isPartial(err) {
				return err // 做到一半 client 被替换：什么都没改（输入关闭之前就停了，或停在切换之前）
			}
			m.clientGen++
			return err
		}
	}
	m.clientGen++
	return nil
}

// bind 把这一次操作绑定到中间现在的 client（见 Center.Bind），返回解除函数。中间没有 client（布局没起来、
// 正在重连）时绑定为 NoClient：操作中途冒出来的 client 一律不认，由各步自己报错（n049 评审 3 建议）。
func (m *Machine) bind() func() {
	id, err := m.center.ClientID()
	if err != nil {
		id = NoClient
	}
	m.center.Bind(id)
	return func() { m.center.Bind(0) }
}

// bindTo 同 bind，但要求中间现在的 client 就是 client（reset、resync 的发通知者），否则 ErrStaleClient。
func (m *Machine) bindTo(client int) (func(), error) {
	id, err := m.center.ClientID()
	if err != nil {
		return nil, err
	}
	if client <= 0 || id != client {
		return nil, i18n.E("err.viewstate.bindto", ErrStaleClient, client, id)
	}
	m.center.Bind(id)
	return func() { m.center.Bind(0) }, nil
}

// Fence 等正在进行的状态机操作做完再返回（拿一次锁）。deck center 在旧 client 退出后、起新 client 之前调它：
// 新 client 连上时不会有操作做到一半——否则那个操作在「找 client」与「执行 tmux 命令」之间，
// 可能作用到共用同一个 tty 的新 client 上（n049 评审 2 B1）。
func (m *Machine) Fence() {
	m.mu.Lock()
	m.mu.Unlock()
}

// ErrClosed 表示 deckd 正在退出，状态机不再接受操作。
var ErrClosed = &codeErr{text: "deckd 正在退出，不再改变中间窗格与写入权", key: "err.viewstate.closed"}

// Close 让状态机不再接受操作：等正在进行的那个做完（拿一次锁），之后的一律返回 ErrClosed。
// deckd 退出、放单实例锁之前调用（n049 评审 4）。
func (m *Machine) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
}

// Resync 在中间 client 新建之后（deck center 首次连上、或 deckd 重启后）调用：client 代次 +1，
// 按当前焦点重做一次切换（先只读确认、再切、再按目标放开），让新 client 显示的与 deckd 记的一致。
// 与 CenterReset 不同，它不隐藏任何会话（n049）。client 的核对同 CenterReset。
func (m *Machine) Resync(client int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.writable(); err != nil {
		return err
	}
	unbind, err := m.bindTo(client)
	if err != nil {
		return err
	}
	defer unbind()
	if err := m.switchLocked(m.focus); err != nil {
		if !errors.Is(err, ErrStaleClient) || isPartial(err) {
			m.clientGen++
		}
		return err
	}
	m.clientGen++
	return nil
}

// RetryHandback 重试一次失败的交还。会话必须不在中间可写（是焦点时先关输入并确认）。
func (m *Machine) RetryHandback(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.writable(); err != nil {
		return err
	}
	t, err := m.canon(id)
	if err != nil {
		return err
	}
	if _, ok := m.taken[t.Session]; !ok {
		return ErrNotTaken
	}
	if m.focus == t.Session {
		defer m.bind()()
		if err := m.closeInputLocked(); err != nil {
			return err
		}
	}
	return m.handbackLocked(t.Session, i18n.E("err.viewstate.why.retry"))
}

// switchLocked：先只读并确认 → 被接管的旧焦点交还 → 再查存储 → 切换 → 按新目标放开输入。
// 旧焦点交还失败不阻止切换（它的输入已关闭、自动派送仍暂停），切换完成后返回 *ErrHandback；
// 但交还过程中存储停了，就不切换、不放开输入，返回停止原因（n050 评审 R3）。
func (m *Machine) switchLocked(target string) error {
	m.layoutGen++
	if err := m.closeInputLocked(); err != nil {
		return err
	}
	prev := m.focus
	var herr error
	handedBack := false // 交还已落盘：之后再出错也不能整体撤回，只能补齐到目标
	if prev != "" && prev != target {
		if _, ok := m.taken[prev]; ok {
			herr = m.handbackLocked(prev, i18n.E("err.viewstate.why.switch"))
			handedBack = herr == nil
		}
	}
	if err := m.writable(); err != nil {
		if herr != nil {
			return i18n.E("err.viewstate.noswitchboth", err, herr)
		}
		return i18n.E("err.viewstate.noswitch", err)
	}
	tmux := Idle
	var t Target
	if target != "" {
		var err error
		if t, err = m.canon(target); err != nil {
			return err
		}
		tmux = t.Tmux
	}
	if err := m.center.Switch(tmux); err != nil {
		m.protectUnconfirmed(err)
		if handedBack {
			m.focus = target
			done := i18n.E("err.viewstate.handed", prev)
			return &ErrPartial{Done: done.Error(), Cause: i18n.E("err.viewstate.switchfail", err), done: done}
		}
		return i18n.E("err.viewstate.switchfail", err)
	}
	m.focus = target
	if target != "" && t.ownerInput() {
		if err := m.center.SetReadOnly(false); err != nil {
			m.protectUnconfirmed(err)
			if errors.Is(err, ErrStaleClient) && !handedBack {
				m.focus = prev // 只动过内存与已不在的旧 client：整体撤回
				return err
			}
			// 中间已经切了过去（或交还已落盘）：焦点按目标提交，输入保持只读
			done := i18n.E("err.viewstate.switched", title(t))
			return &ErrPartial{Done: done.Error(), Cause: i18n.E("err.viewstate.openinput", err), done: done}
		}
	}
	return herr
}

func (m *Machine) closeInputLocked() error {
	if err := m.center.SetReadOnly(true); errors.Is(err, ErrStaleClient) {
		m.protectUnconfirmed(err)
		return err // 绑定的 client 已被替换：不是「关不上」，什么都没做
	} else if err != nil {
		m.notify(i18n.E("err.viewstate.inputset"))
		return ErrInputNotClosed
	}
	ro, err := m.center.ReadOnly()
	if errors.Is(err, ErrStaleClient) {
		return err
	}
	if err != nil || !ro {
		m.notify(i18n.E("err.viewstate.inputread"))
		return ErrInputNotClosed
	}
	if tk, ok := m.taken[m.focus]; ok && tk.writable {
		tk.writable = false // 输入已确认关闭：中间这个被接管的会话转为等待交还
		m.taken[m.focus] = tk
	}
	return nil
}

// Takeover 接管当前显示的子 agent。
func (m *Machine) Takeover(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.writable(); err != nil {
		return err
	}
	if id == "" {
		// 空串表示「中间正显示的那个」：由状态机在锁内取焦点。界面手里的焦点可能是一秒前的，
		// 拿它去隐藏或接管会作用到别的会话上（n049 真机演示：Ctrl-Q 隐藏了上一个会话）
		if m.focus == "" {
			return ErrNotOpen
		}
		id = m.focus
	}
	t, err := m.canon(id)
	if err != nil {
		return err
	}
	session := t.Session
	if m.focus != session {
		return ErrNotFocused
	}
	if t.Role != "sub" {
		return ErrNotSub
	}
	client, _ := m.center.ClientID()
	unbind := m.bind()
	defer func() { unbind() }()
	if tk, ok := m.taken[session]; ok {
		if tk.writable {
			return nil // 已在接管中，输入在你手上
		}
		// 等待交还的会话（交还失败或重启后恢复）：lease 仍在 owner 手上，重新放开输入即可（n050 评审 R5）
		return m.openTakenLocked(session, t, i18n.E("err.viewstate.retook", title(t)))
	}
	reqLayout, reqClient := m.layoutGen, m.clientGen
	deadline := m.now().Add(m.TakeoverWait)
	for {
		// 每次（含醒来后）先比对：作废的请求还没拿到写入权，直接放弃，不碰任何 lease
		if m.layoutGen != reqLayout || m.clientGen != reqClient || m.focus != session {
			return ErrTakeoverStale
		}
		// 醒来后别的操作可能换过绑定：重新绑定，并要求还是开始时那个 client（被替换就放弃，不碰 lease）
		if client > 0 {
			unbind()
			u, err := m.bindTo(client)
			if err != nil {
				unbind = func() {}
				return ErrTakeoverStale
			}
			unbind = u
		}
		if err := m.writable(); err != nil {
			return err
		}
		err := m.leases.OwnerTakeover(session, "deck")
		if err == nil {
			break
		}
		var busy *lease.ErrDeliveryInProgress
		if !errors.As(err, &busy) {
			return i18n.E("err.viewstate.takeoverreg", err)
		}
		if !m.now().Before(deadline) {
			return ErrTakeoverBusy
		}
		unbind() // 绑定只在持锁时有效：放锁前解除，醒来在循环开头重新绑定原 client
		unbind = func() {}
		m.mu.Unlock()
		m.sleep(100 * time.Millisecond)
		m.mu.Lock()
	}
	// 从拿到写入权到放开输入，锁一直在手上：中间不会发生切换
	m.taken[session] = taken{since: m.now()}
	return m.openTakenLocked(session, t, i18n.E("err.viewstate.tookover", title(t)))
}

// openTakenLocked 为一个 lease 已在 owner 手上、正显示在中间的会话放开输入，并写接管通知。
// 存储已停止就不放开（保持等待交还）。放开失败或通知写不进：先确认输入关闭再交还；
// 关不上或交还失败都如实返回，会话保持接管、自动派送暂停（n050 评审 R5）。
func (m *Machine) openTakenLocked(session string, t Target, note *i18n.Error) error {
	if err := m.writable(); err != nil {
		return i18n.E("err.viewstate.inputstay", err)
	}
	if err := m.center.SetReadOnly(false); err != nil {
		if errors.Is(err, ErrRestoreUnconfirmed) {
			// 放开输入的命令落到了新 client 上，又没能确认还原：新 client 可能在这个会话上、输入开着。
			// 不交还：写入权留在 owner 手上，自动派送保持暂停，等 owner 在中间确认后重新接管或交还
			m.protectUnconfirmed(err)
			return i18n.E("err.viewstate.takeoverhold", err)
		}
		if errors.Is(err, ErrStaleClient) {
			// 新 client 一连上就是只读、旧的已不在：输入从没放开过。按交还收回（带画面摘要），
			// 不留一个没生效的接管；交还不成就保持接管与暂停（既有的安全恢复）（n049 评审 3 B1-B）
			if herr := m.handbackLocked(session, i18n.E("err.viewstate.why.replaced")); herr != nil {
				return i18n.E("err.viewstate.takeoverboth", err, herr)
			}
			return i18n.E("err.viewstate.takeoverback", err)
		}
		if cerr := m.closeInputLocked(); cerr != nil {
			return i18n.E("err.viewstate.openfailunconfirmed", err)
		}
		if herr := m.handbackLocked(session, i18n.E("err.viewstate.why.openfail")); herr != nil {
			return i18n.E("err.viewstate.openfailclosed", err, herr)
		}
		return i18n.E("err.viewstate.openfailback", err)
	}
	// 输入回到 owner 手上：之前缓存的画面摘要已过时，重启恢复的豁免也到此为止——
	// 下一次交还必须重新取到画面（n050 评审 A3-2）
	tk := m.taken[session]
	tk.writable, tk.summary, tk.restored = true, nil, false
	m.taken[session] = tk
	if err := m.notify(note); err != nil {
		// 记不下来就不维持一个没人知道的接管：关输入、交还；哪一步不成都照实说
		if cerr := m.closeInputLocked(); cerr != nil {
			return i18n.E("err.viewstate.notenotclosed", err)
		}
		if herr := m.handbackLocked(session, i18n.E("err.viewstate.why.note")); herr != nil {
			return i18n.E("err.viewstate.noteclosed", err, herr)
		}
		return i18n.E("err.viewstate.notereverted", err)
	}
	return nil
}

// handbackLocked 交还。前提：这个会话的输入已经关闭（调用方保证）。
// 顺序：取画面摘要 → 写「正在交还」通知（落盘）→ 释放 lease（完整摘要随 lease 事件同一次落盘）。
// 任一步失败都保持接管（m.taken 不删）、自动派送暂停，返回 *ErrHandback（n050 评审 B5、R2）。
func (m *Machine) handbackLocked(session string, why *i18n.Error) error {
	tk := m.taken[session]
	tk.writable = false
	m.taken[session] = tk
	t, known := m.lookup(session)
	fail := func(cause error) error {
		m.notify(i18n.E("err.viewstate.handbacknotify", title(t), cause))
		return &ErrHandback{Session: session, Cause: cause}
	}
	var hb lease.Handback
	switch {
	case m.focus == session:
		if !known || t.Tmux == "" {
			return fail(i18n.E("err.viewstate.notmux"))
		}
		lines, err := m.center.Capture(t.Tmux, 5)
		if err != nil {
			return fail(i18n.E("err.viewstate.nocapture", err))
		}
		for i := range lines {
			lines[i] = deckstore.Clean(lines[i], 200)
		}
		hb.LastLines = lines
		tk.summary = &lease.Handback{LastLines: append([]string(nil), lines...)}
		m.taken[session] = tk
	case tk.summary != nil:
		hb.LastLines = append([]string(nil), tk.summary.LastLines...)
		hb.Notes = append(hb.Notes, i18n.E("err.viewstate.summaryprev").Error())
	case tk.restored:
		hb.Notes = append(hb.Notes, i18n.E("err.viewstate.summaryrestored").Error())
	default:
		return fail(i18n.E("err.viewstate.nosummary"))
	}
	now := m.now()
	hb.AttachedAt, hb.ReturnedAt, hb.DurationSeconds, hb.Via = tk.since, now, int64(now.Sub(tk.since).Seconds()), "deck"
	hb.Notes = append([]string{i18n.E("err.viewstate.leftway", why).Error()}, hb.Notes...)
	if len(hb.LastLines) > 0 {
		hb.Notes = append(hb.Notes, i18n.E("err.viewstate.lastline", hb.LastLines[len(hb.LastLines)-1]).Error())
	}
	text := i18n.E("err.viewstate.left", title(t), why, hb.DurationSeconds)
	if len(hb.LastLines) > 0 {
		text = i18n.E("err.viewstate.leftline", title(t), why, hb.DurationSeconds, hb.LastLines[len(hb.LastLines)-1])
	}
	if err := m.notify(text); err != nil {
		return &ErrHandback{Session: session, Cause: i18n.E("err.viewstate.handbacknote", err)}
	}
	if err := m.leases.AutoHandback(session, hb); err != nil {
		return fail(err)
	}
	delete(m.taken, session)
	return nil
}

// DeliverToMain 给主 agent 送一条汇报（n051 调）。deliver 真正发送（agent-deck send --defer-if-busy）。
// 主 agent 正显示在中间、owner 在 OwnerIdle 内打过字：返回 ErrOwnerTyping，调用方排队。
// 正显示在中间：送前先关输入并确认，送完按 brief 状态放开。不在中间：直接送。
func (m *Machine) DeliverToMain(id string, deliver func() error) error {
	return m.deliverToMain(id, deliver, false)
}

// DeliverToMainNow 是 owner 按键要求「立刻送」（§14.4 第 2 条，n051 评审 1 B6）：不看 30 秒打字门，
// 其余照旧——送前先关输入并确认，送完按 brief 状态放开。
func (m *Machine) DeliverToMainNow(id string, deliver func() error) error {
	return m.deliverToMain(id, deliver, true)
}

func (m *Machine) deliverToMain(id string, deliver func() error, now bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.writable(); err != nil {
		return err
	}
	t, err := m.canon(id)
	if err != nil || t.Role != "main" {
		return i18n.E("err.viewstate.notmain", id)
	}
	if m.focus != t.Session {
		return deliver()
	}
	defer m.bind()()
	ro, err := m.center.ReadOnly()
	if err != nil {
		return ErrInputNotClosed
	}
	if !ro && !now {
		last, err := m.center.LastInput()
		if err != nil {
			return i18n.E("err.viewstate.nolastinput", err)
		}
		if m.now().Sub(last) < m.OwnerIdle {
			return ErrOwnerTyping
		}
	}
	if err := m.closeInputLocked(); err != nil {
		return err
	}
	derr := deliver()
	if serr := m.writable(); serr != nil {
		kept := i18n.E("err.viewstate.inputkept", serr)
		if derr == nil {
			return kept
		}
		return i18n.E("err.viewstate.joinline", derr, kept)
	}
	// 送完按最新状态放开（第一条 brief 就是经这里送的：送前还在等 owner 处理启动确认、送后已确认，n055）
	if cur, ok := m.lookup(id); ok && cur.ownerInput() {
		if err := m.center.SetReadOnly(false); err != nil {
			m.notify(i18n.E("err.viewstate.reopenfail"))
		}
	}
	return derr
}

// OpenMainInput：主 agent 的状态变了（第一条 brief 刚确认送达，或 CLI 停在启动确认上等 owner 处理），它正显示在中间、
// 又没有被别的操作关着输入时，按最新状态放开输入（n055；原先要等下一次切换才放开）。不在中间就什么都不做。
func (m *Machine) OpenMainInput(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.writable(); err != nil {
		return err
	}
	t, err := m.canon(id)
	if err != nil || m.focus != t.Session || !t.ownerInput() {
		return err
	}
	defer m.bind()()
	return m.center.SetReadOnly(false)
}

func isHandback(err error) bool {
	var he *ErrHandback
	return errors.As(err, &he)
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

// protectUnconfirmed：命令落到新 client 上且没能确认还原（ErrRestoreUnconfirmed）时调用。新 client 可能停在焦点会话上、
// 输入开着：焦点是子 agent，就把写入权留在（或收到）owner 手上、自动派送暂停，等 owner 处理；任何情况都发通知说明原因。
// 其他错误什么都不做。调用方持锁。
func (m *Machine) protectUnconfirmed(err error) {
	if !errors.Is(err, ErrRestoreUnconfirmed) {
		return
	}
	session := m.focus
	t, ok := m.lookup(session)
	base := i18n.E("err.viewstate.unconfirmed")
	note := base
	if ok && t.Role == "sub" {
		if tk, held := m.taken[session]; held {
			tk.writable = false
			m.taken[session] = tk
			note = i18n.E("err.viewstate.unconfirmed.hold", title(t), base)
		} else if lerr := m.leases.OwnerTakeover(session, "deck"); lerr == nil {
			m.taken[session] = taken{since: m.now()}
			note = i18n.E("err.viewstate.unconfirmed.take", title(t), base)
		} else {
			note = i18n.E("err.viewstate.unconfirmed.fail", title(t), base, lerr)
		}
	}
	m.notify(note)
}
