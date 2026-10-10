// Package lease 管「谁有权往一个会话里写」。
//
// Work Deck 是唯一的自动写入者，写入权由它自己持有、自己记账。owner 接管（进入）一个会话时，
// 写入权交给 owner，Work Deck 对那个会话的一切自动投递停止；owner 交还后，要先看介入摘要、
// 明确确认，自动投递才恢复。
//
// 所有投递都要一张 Permit。Permit 只能由本包的 Store 签发，字段不导出，别的包造不出有效的；
// 使用时 Allows 还会回头核对当前记账，签发之后 owner 接管了，旧 Permit 也立刻失效。
// adapter 的 SendMessage 要求传入 Permit，所以绕开本包就发不出消息
// （TestOnlyLeaseCanMintPermits 与 adapter 的签名守着）。
//
// 判断一律 fail-closed：没有记录、持有者是 owner、有未确认的交还、租约过期、存储读写失败，
// 都不投递，并给出明确原因。
package lease

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// 持有者。
const (
	HolderWorkbench = "workbench"
	HolderOwner     = "owner"
)

// DefaultTTL 是 Work Deck 租约的有效期。过期后自动投递停止，要 owner 确认续约。
//
// 为什么要过期：租约是「此刻没有人在这个会话里操作」的判断。隔得越久，这个判断越不可信——
// owner 可能在 Work Deck 之外直接进过这个会话。过期就退回到 fail-closed。
const DefaultTTL = 12 * time.Hour

// Handback 是一次接管结束后的介入摘要。owner 确认之前，自动投递不恢复。
type Handback struct {
	AttachedAt time.Time `json:"attached_at"`
	ReturnedAt time.Time `json:"returned_at"`
	// Duration 以秒记，便于人读。
	DurationSeconds int64 `json:"duration_seconds"`
	// Via 是进入方式：upstream（按 a，上游 session attach）或 tmux（按 T，直连）。
	Via string `json:"via"`
	// 画面在进入前后的指纹。取不到时为空，并在 Notes 里说明。
	PaneBefore string `json:"pane_before_sha256"`
	PaneAfter  string `json:"pane_after_sha256"`
	// PaneChanged 为空表示不知道（前后有一边没取到）。
	PaneChanged *bool `json:"pane_changed"`
	// LastLines 是回来时画面的最后几行，给 owner 回忆自己做了什么。
	LastLines []string `json:"last_lines"`
	// AttachError 是进入过程报的错；空表示正常返回。
	AttachError string   `json:"attach_error,omitempty"`
	Notes       []string `json:"notes,omitempty"`
}

// Lease 是一个会话的写入权记录。
type Lease struct {
	SessionID string    `json:"session_id"`
	Holder    string    `json:"holder"`
	Since     time.Time `json:"since"`
	// ExpiresAt 只对 workbench 持有有意义。
	ExpiresAt time.Time `json:"expires_at"`
	// PendingHandback 非空：owner 已经回来，但还没确认交还。
	PendingHandback *Handback `json:"pending_handback"`
}

// Event 是一次持有者切换。事件与持有者同存 leases.json，同一次原子落盘（最近 500 条）。
type Event struct {
	At        time.Time `json:"at"`
	SessionID string    `json:"session_id"`
	Kind      string    `json:"kind"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Reason    string    `json:"reason"`
}

// 事件种类。
const (
	EventGrant    = "grant"          // Work Deck 为自己派出的会话取得写入权
	EventTakeover = "owner_takeover" // owner 进入会话
	EventReturn   = "owner_return"   // owner 回来，等确认
	EventConfirm  = "handback_confirmed"
	EventRenew    = "renewed"
	EventExternal = "external_takeover" // 投递前查到不经 Work Deck 的接入
	// EventAutoHandback 是 Agent Deck 的自动交还（owner 在 n048 验收时选择，架构稿 §14.1）：
	// owner 离开被接管的会话、中间窗格已确认只读之后，记下摘要并立即交还，不再等确认。
	EventAutoHandback = "auto_handback"
)

type file struct {
	Version int     `json:"version"`
	Leases  []Lease `json:"leases"`
	// Events 与持有者写在同一个文件、同一次原子落盘里：进程在两次写之间中断，
	// 不会留下「事件说接管了、快照还是旧持有者」这种不一致。只留最近 maxEvents 条。
	Events []Event `json:"events"`
}

// maxEvents 是记账文件里保留的切换事件条数。
const maxEvents = 500

const currentVersion = 1

// Store 是持久化的写入权记账。
type Store struct {
	path     string
	lockFile *os.File
	mu       sync.Mutex
	leases   map[string]*Lease
	events   []Event
	// inflight 是正在进行的投递数。有投递在进行时不许登记接管，
	// 否则「核过许可」与「消息真正送达」之间，owner 可能已经进去了。
	inflight map[string]int
	// broken 非空表示落盘出过错：此后一律不签发 Permit，直到重新打开且读回正常。
	broken error
	ttl    time.Duration
	now    func() time.Time
}

// Open 打开记账文件。文件不存在是空记账（没有任何会话有写入权，全部 fail-closed）；
// 文件坏了报错，不当成空记账——那会把一个 owner 正接管着的会话当成无主。
//
// 打开时对 path+".lock" 取排他文件锁（flock，不阻塞）：同一份记账只许一个 Work Deck 进程持有，
// 第二个进程打开失败，拿不到记账，它的一切投递都被拒。用完调 Close 释放。
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	lf, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("打不开写入权记账的锁文件：%w", err)
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lf.Close()
		return nil, fmt.Errorf("写入权记账 %s 正被另一个 Work Deck 进程持有（%v）：两份记账不能互斥，本进程不投递任何东西", path, err)
	}
	s := &Store{path: path, lockFile: lf, leases: map[string]*Lease{}, inflight: map[string]int{}, ttl: DefaultTTL, now: time.Now}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("读写入权记账失败：%w", err)
	}
	var f struct {
		Version int      `json:"version"`
		Leases  *[]Lease `json:"leases"`
		Events  []Event  `json:"events"`
	}
	bad := func(why string) error {
		s.Close()
		return fmt.Errorf("写入权记账 %s %s；不当成空记账打开，免得把 owner 接管中的会话当成无主", path, why)
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, bad(fmt.Sprintf("解析不了（%v）", err))
	}
	if f.Version != currentVersion {
		return nil, bad(fmt.Sprintf("版本是 %d，只认 %d", f.Version, currentVersion))
	}
	if f.Leases == nil {
		return nil, bad("缺少 leases 字段")
	}
	for _, l := range *f.Leases {
		l := l
		if l.SessionID == "" || (l.Holder != HolderWorkbench && l.Holder != HolderOwner) {
			return nil, bad(fmt.Sprintf("有一条记录不完整（会话 %q、持有者 %q）", l.SessionID, l.Holder))
		}
		if _, dup := s.leases[l.SessionID]; dup {
			return nil, bad("会话 " + l.SessionID + " 有两条记录")
		}
		s.leases[l.SessionID] = &l
	}
	s.events = f.Events
	return s, nil
}

// Close 释放文件锁。之后这个 Store 不该再用。
func (s *Store) Close() {
	if s.lockFile != nil {
		syscall.Flock(int(s.lockFile.Fd()), syscall.LOCK_UN)
		s.lockFile.Close()
		s.lockFile = nil
	}
}

// ErrDeliveryInProgress 表示这个会话有一条投递正在进行，此刻不能登记接管。
type ErrDeliveryInProgress struct{ SessionID string }

func (e *ErrDeliveryInProgress) Error() string {
	return "会话 " + e.SessionID + " 有一条自动投递正在进行，等它结束再进入（否则消息可能在你进去之后才送达）"
}

// Get 取一个会话的记录。
func (s *Store) Get(sessionID string) (Lease, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[sessionID]
	if !ok {
		return Lease{}, false
	}
	return *l, true
}

// Pending 列出等 owner 确认交还的会话。
func (s *Store) Pending() []Lease {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Lease
	for _, l := range s.leases {
		if l.PendingHandback != nil {
			out = append(out, *l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out
}

// Grant 让 Work Deck 为一个刚由它自己派出的会话取得写入权。
//
// 只用于新会话：已有记录的会话（例如 owner 正接管着、或等确认交还）不许借 Grant 夺回。
func (s *Store) Grant(sessionID, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sessionID == "" {
		return fmt.Errorf("没有会话号，不能登记写入权")
	}
	if l, ok := s.leases[sessionID]; ok && (l.Holder == HolderOwner || l.PendingHandback != nil) {
		return fmt.Errorf("会话 %s 在 owner 手上，Work Deck 不能自行取回写入权", sessionID)
	}
	now := s.now()
	from := ""
	if l, ok := s.leases[sessionID]; ok {
		from = l.Holder
	}
	next := &Lease{SessionID: sessionID, Holder: HolderWorkbench, Since: now, ExpiresAt: now.Add(s.ttl)}
	return s.commitLocked(sessionID, next, Event{At: now, SessionID: sessionID, Kind: EventGrant, From: from, To: HolderWorkbench, Reason: reason})
}

// OwnerTakeover 在 owner 进入会话之前调用：写入权交给 owner。
//
// 没有记录的会话也登记成 owner 持有：那是 owner 在接管，不能因为 Work Deck 之前不认识它就放任。
func (s *Store) OwnerTakeover(sessionID, via string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sessionID == "" {
		return fmt.Errorf("没有会话号，不能登记接管")
	}
	if s.inflight[sessionID] > 0 {
		return &ErrDeliveryInProgress{SessionID: sessionID}
	}
	now := s.now()
	from := ""
	if l, ok := s.leases[sessionID]; ok {
		from = l.Holder
	}
	next := &Lease{SessionID: sessionID, Holder: HolderOwner, Since: now}
	return s.commitLocked(sessionID, next, Event{At: now, SessionID: sessionID, Kind: EventTakeover, From: from, To: HolderOwner, Reason: "owner 经 " + via + " 进入会话"})
}

// ExternalTakeover 登记一次不经 Work Deck 的接管：投递前查到 tmux 上有客户端连着这个会话。
//
// 一步里既转给 owner、也挂上一份「等确认交还」的摘要（进入时间与画面都不知道）：Work Deck 看不到这次接管何时结束，
// 只能让 owner 自己确认。他在还连着的时候就确认也不会误投——下一次自动投递前会再查一次客户端，查到就再登记接管。
func (s *Store) ExternalTakeover(sessionID string, clients int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sessionID == "" {
		return fmt.Errorf("没有会话号，不能登记接管")
	}
	if s.inflight[sessionID] > 0 {
		return &ErrDeliveryInProgress{SessionID: sessionID}
	}
	now := s.now()
	from := ""
	if l, ok := s.leases[sessionID]; ok {
		from = l.Holder
	}
	hb := &Handback{ReturnedAt: now, Via: "external",
		Notes: []string{fmt.Sprintf("不经 Work Deck 的接入：投递前查到 %d 个 tmux 客户端连着这个会话；进入时间与进入前画面不知道", clients)}}
	next := &Lease{SessionID: sessionID, Holder: HolderOwner, Since: now, PendingHandback: hb}
	return s.commitLocked(sessionID, next, Event{At: now, SessionID: sessionID, Kind: EventExternal, From: from, To: HolderOwner,
		Reason: fmt.Sprintf("查到 %d 个 tmux 客户端连着会话，按 owner 接管处理，等确认交还", clients)})
}

// OwnerReturn 在 owner 回来之后调用：写入权仍在 owner 手上，挂一份介入摘要等确认。
func (s *Store) OwnerReturn(sessionID string, hb Handback) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[sessionID]
	if !ok || l.Holder != HolderOwner {
		return fmt.Errorf("会话 %s 没有登记为 owner 接管中，不能登记返回", sessionID)
	}
	next := *l
	h := hb
	next.PendingHandback = &h
	return s.commitLocked(sessionID, &next, Event{At: s.now(), SessionID: sessionID, Kind: EventReturn, From: HolderOwner, To: HolderOwner, Reason: "owner 已回来，自动投递仍暂停，等确认交还"})
}

// AutoHandback 是 Agent Deck 的自动交还：会话须由 owner 持有；摘要随事件留档，写入权立即回到 workbench。
// 调用方负责先确认 owner 的输入已关闭（中间窗格只读），再调这里。
func (s *Store) AutoHandback(sessionID string, hb Handback) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[sessionID]
	if !ok || l.Holder != HolderOwner {
		return fmt.Errorf("会话 %s 没有登记为 owner 接管中，不能自动交还", sessionID)
	}
	now := s.now()
	reason := fmt.Sprintf("owner 离开，自动交还：接管 %d 秒", hb.DurationSeconds)
	if len(hb.Notes) > 0 {
		reason += "；" + strings.Join(hb.Notes, "；")
	}
	next := &Lease{SessionID: sessionID, Holder: HolderWorkbench, Since: now, ExpiresAt: now.Add(s.ttl)}
	return s.commitLocked(sessionID, next, Event{At: now, SessionID: sessionID, Kind: EventAutoHandback, From: HolderOwner, To: HolderWorkbench, Reason: reason})
}

// ConfirmHandback 是 owner 看过摘要后的确认：写入权回到 Work Deck，自动投递恢复。
func (s *Store) ConfirmHandback(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[sessionID]
	if !ok || l.PendingHandback == nil {
		return fmt.Errorf("会话 %s 没有等确认的交还", sessionID)
	}
	now := s.now()
	next := &Lease{SessionID: sessionID, Holder: HolderWorkbench, Since: now, ExpiresAt: now.Add(s.ttl)}
	return s.commitLocked(sessionID, next, Event{At: now, SessionID: sessionID, Kind: EventConfirm, From: HolderOwner, To: HolderWorkbench, Reason: "owner 看过介入摘要，确认交还"})
}

// Renew 是 owner 对一份过期租约的续约确认。
func (s *Store) Renew(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[sessionID]
	if !ok || l.Holder != HolderWorkbench || l.PendingHandback != nil {
		return fmt.Errorf("会话 %s 不是 Work Deck 持有的租约，不能续约", sessionID)
	}
	now := s.now()
	next := &Lease{SessionID: sessionID, Holder: HolderWorkbench, Since: l.Since, ExpiresAt: now.Add(s.ttl)}
	return s.commitLocked(sessionID, next, Event{At: now, SessionID: sessionID, Kind: EventRenew, From: HolderWorkbench, To: HolderWorkbench, Reason: "owner 确认续约"})
}

// Broken 返回存储是否因落盘失败而停用。
func (s *Store) Broken() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.broken
}

// commitLocked 把新持有者与这条事件一起、一次原子落盘；失败时内存退回原样并把存储标成停用。
//
// 标成停用是 fail-closed：落盘失败意味着磁盘上的记账可能与内存不一致，
// 这时签发任何 Permit 都没有依据。
func (s *Store) commitLocked(sessionID string, next *Lease, ev Event) error {
	if s.broken != nil {
		return fmt.Errorf("写入权记账已停用（%v）", s.broken)
	}
	prev, had := s.leases[sessionID]
	prevEvents := s.events
	s.leases[sessionID] = next
	s.events = append(append([]Event{}, s.events...), ev)
	if len(s.events) > maxEvents {
		s.events = s.events[len(s.events)-maxEvents:]
	}
	if err := s.flushLocked(); err != nil {
		s.restore(sessionID, prev, had)
		s.events = prevEvents
		s.broken = err
		return fmt.Errorf("写入权记账落盘失败，记账停用：%w", err)
	}
	return nil
}

func (s *Store) restore(sessionID string, prev *Lease, had bool) {
	if had {
		s.leases[sessionID] = prev
	} else {
		delete(s.leases, sessionID)
	}
}

func (s *Store) flushLocked() error {
	list := make([]Lease, 0, len(s.leases))
	for _, l := range s.leases {
		list = append(list, *l)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].SessionID < list[j].SessionID })
	data, err := json.MarshalIndent(file{Version: currentVersion, Leases: list, Events: s.events}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".leases-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, s.path)
}

// Events 返回记账里保留的切换事件，供界面与测试观察。
func (s *Store) Events() ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event{}, s.events...), nil
}
