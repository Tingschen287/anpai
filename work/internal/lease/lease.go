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

// Event 是一次持有者切换，写进事件文件，一行一条。
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
)

type file struct {
	Version int     `json:"version"`
	Leases  []Lease `json:"leases"`
}

const currentVersion = 1

// Store 是持久化的写入权记账。
type Store struct {
	path, eventsPath string
	mu               sync.Mutex
	leases           map[string]*Lease
	// broken 非空表示落盘出过错：此后一律不签发 Permit，直到重新打开且读回正常。
	broken error
	ttl    time.Duration
	now    func() time.Time
}

// Open 打开记账文件。文件不存在是空记账（没有任何会话有写入权，全部 fail-closed）；
// 文件坏了报错，不当成空记账——那会把一个 owner 正接管着的会话当成无主。
func Open(path string) (*Store, error) {
	// 切换事件另写一个文件，一行一条。文件名避开 Graph 账本的名字，那是 projectctl 的地盘。
	s := &Store{path: path, eventsPath: strings.TrimSuffix(path, ".json") + "-log.jsonl", leases: map[string]*Lease{}, ttl: DefaultTTL, now: time.Now}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读写入权记账失败：%w", err)
	}
	var f struct {
		Version int      `json:"version"`
		Leases  *[]Lease `json:"leases"`
	}
	bad := func(why string) error {
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
	return s, nil
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
	now := s.now()
	from := ""
	if l, ok := s.leases[sessionID]; ok {
		from = l.Holder
	}
	next := &Lease{SessionID: sessionID, Holder: HolderOwner, Since: now}
	return s.commitLocked(sessionID, next, Event{At: now, SessionID: sessionID, Kind: EventTakeover, From: from, To: HolderOwner, Reason: "owner 经 " + via + " 进入会话"})
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

// commitLocked 先写事件、再写记账；任何一步失败，内存退回原样并把存储标成停用。
//
// 标成停用是 fail-closed：落盘失败意味着磁盘上的记账可能与内存不一致，
// 这时签发任何 Permit 都没有依据。
func (s *Store) commitLocked(sessionID string, next *Lease, ev Event) error {
	if s.broken != nil {
		return fmt.Errorf("写入权记账已停用（%v）", s.broken)
	}
	prev, had := s.leases[sessionID]
	s.leases[sessionID] = next
	if err := s.appendEventLocked(ev); err != nil {
		s.restore(sessionID, prev, had)
		s.broken = err
		return fmt.Errorf("写入权事件落盘失败，记账停用：%w", err)
	}
	if err := s.flushLocked(); err != nil {
		s.restore(sessionID, prev, had)
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

func (s *Store) appendEventLocked(ev Event) error {
	if err := os.MkdirAll(filepath.Dir(s.eventsPath), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.eventsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	line, _ := json.Marshal(ev)
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (s *Store) flushLocked() error {
	list := make([]Lease, 0, len(s.leases))
	for _, l := range s.leases {
		list = append(list, *l)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].SessionID < list[j].SessionID })
	data, err := json.MarshalIndent(file{Version: currentVersion, Leases: list}, "", "  ")
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

// Events 读回事件文件，供界面与测试观察切换。读不了返回错误。
func (s *Store) Events() ([]Event, error) {
	data, err := os.ReadFile(s.eventsPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, line := range splitLines(data) {
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("写入权事件文件有坏行：%w", err)
		}
		out = append(out, ev)
	}
	return out, nil
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			if i > start {
				out = append(out, b[start:i])
			}
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
