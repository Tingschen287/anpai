// Package deckstore 是 deckd 的全部持久状态（n050，架构稿 §3–§5、§14）。
//
// 只有 deckd 进程打开这些文件；所有改动都在 Store 的一把互斥锁里做「读-检查-写」，落盘用原子 rename。
// 任何一次落盘失败，Store 就进入停止状态，拒绝之后的一切写入——不在无法记录的情况下执行任何被授权的动作。
package deckstore

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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

// ErrBroken 表示存储曾经写失败，已停止接受写入。
type ErrBroken struct{ Cause error }

func (e *ErrBroken) Error() string {
	return fmt.Sprintf("deck 的存储写失败过（%v），已停止接受写入与派工；修好后重启 deckd", e.Cause)
}

// Event 是一次状态变化，deckd 用它推 SSE。
type Event struct {
	Kind    string `json:"kind"` // session、decision、notification、settings、trust
	Project string `json:"project,omitempty"`
	ID      string `json:"id,omitempty"`
}

// Store 是 deckd 的状态。
type Store struct {
	mu       sync.Mutex
	dirs     Dirs
	now      func() time.Time
	broken   error
	settings Settings
	sessions map[string]*Session
	trust    []TrustGrant
	decs     map[string]*Decision
	notes    []Notification
	noteSeq  int64
	hook     func(Event)
}

// Open 读回全部状态。executing 中的决定、launching 中的会话在重启后一律标为 uncertain：
// 不知道它们执行到了哪一步，不能当成没发生，也不能自动重做。
func Open(dirs Dirs) (*Store, error) {
	s := &Store{dirs: dirs, now: time.Now, sessions: map[string]*Session{}, decs: map[string]*Decision{}}
	set, err := LoadSettings(dirs.Config)
	if err != nil {
		return nil, fmt.Errorf("agent 设置不可用：%w", err)
	}
	s.settings = set
	var sessions []*Session
	if _, err := readJSON(s.path("registry.json"), &sessions); err != nil {
		return nil, err
	}
	changed := false
	for _, x := range sessions {
		if x.Status == StatusLaunching {
			x.Status = StatusUncertain
			x.Note = "deckd 重启时仍在启动中，结果不明"
			changed = true
		}
		s.sessions[x.Key] = x
	}
	if _, err := readJSON(s.path("trust.json"), &s.trust); err != nil {
		return nil, err
	}
	var decs []*Decision
	if _, err := readJSON(s.path("decisions.json"), &decs); err != nil {
		return nil, err
	}
	for _, d := range decs {
		if d.State == StateExecuting {
			d.State = StateUncertain
			d.Result = "deckd 重启时仍在执行，效果不明；不自动重做"
			d.Version++
			changed = true
		}
		s.decs[d.ID] = d
	}
	if err := s.loadNotes(); err != nil {
		return nil, err
	}
	if changed {
		if err := s.saveRegistry(); err != nil {
			return nil, err
		}
		if err := s.saveDecisions(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// OnChange 设置变化回调（在锁外调用）。
func (s *Store) OnChange(f func(Event)) { s.mu.Lock(); s.hook = f; s.mu.Unlock() }

// SetClock 供测试固定时间。
func (s *Store) SetClock(f func() time.Time) { s.mu.Lock(); s.now = f; s.mu.Unlock() }

func (s *Store) path(name string) string { return filepath.Join(s.dirs.State, name) }

// Broken 返回停止原因；nil 表示正常。
func (s *Store) Broken() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.broken != nil {
		return &ErrBroken{Cause: s.broken}
	}
	return nil
}

// mutate 在锁内执行 f；f 返回要持久化的动作与事件。持久化失败就进入停止状态。
func (s *Store) mutate(f func() (persist func() error, ev []Event, err error)) error {
	s.mu.Lock()
	if s.broken != nil {
		s.mu.Unlock()
		return &ErrBroken{Cause: s.broken}
	}
	persist, evs, err := f()
	if err == nil && persist != nil {
		if perr := persist(); perr != nil {
			s.broken = perr
			err = &ErrBroken{Cause: perr}
		}
	}
	hook := s.hook
	s.mu.Unlock()
	if err == nil && hook != nil {
		for _, e := range evs {
			hook(e)
		}
	}
	return err
}

func newID(prefix string) string {
	b := make([]byte, 6)
	rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}

// ---------------- 设置 ----------------

// Settings 返回当前设置的副本。
func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneSettings(s.settings)
}

// ReplaceSettings 由 owner 界面调用：校验后写回文件。
func (s *Store) ReplaceSettings(next Settings) error {
	return s.mutate(func() (func() error, []Event, error) {
		if err := next.Validate(); err != nil {
			return nil, nil, err
		}
		s.settings = cloneSettings(next)
		return func() error { return SaveSettings(s.dirs.Config, next) }, []Event{{Kind: "settings"}}, nil
	})
}

func cloneSettings(x Settings) Settings {
	data, _ := json.Marshal(x)
	var y Settings
	json.Unmarshal(data, &y)
	return y
}

// ---------------- 会话注册表（兼做名额预留） ----------------

// 会话状态。launching、running、uncertain 都占名额：结果不明时继续占着，直到核对清楚。
const (
	StatusLaunching = "launching"
	StatusRunning   = "running"
	StatusUncertain = "uncertain"
	StatusDone      = "done"
	StatusFailed    = "failed"
	StatusStopped   = "stopped"
)

// GraphLink 是 Graph 通道启动的会话所对应的节点与 attempt；普通通道为 nil，不伪造。
type GraphLink struct {
	Node    string `json:"node"`
	Attempt int    `json:"attempt"`
}

// Session 是注册表里的一条。
type Session struct {
	Key            string     `json:"key"` // 启动请求 id；launch 成功后不变，用它关联 agent-deck 会话
	AgentDeckID    string     `json:"agentdeck_id,omitempty"`
	TmuxSession    string     `json:"tmux_session,omitempty"`
	Project        string     `json:"project"`
	Role           string     `json:"role"` // main 或 sub
	Parent         string     `json:"parent,omitempty"`
	Agent          string     `json:"agent"`
	Tool           string     `json:"tool"`
	Model          string     `json:"model"`
	Tier           Tier       `json:"tier"`
	Title          string     `json:"title"`
	CreatedBy      string     `json:"created_by"` // owner 或发起会话的 key
	CreatedAt      time.Time  `json:"created_at"`
	Status         string     `json:"status"`
	Graph          *GraphLink `json:"graph,omitempty"`
	BriefConfirmed bool       `json:"brief_confirmed"` // 主 agent 的第一条 brief 已确认送达（之前中间窗格对它保持只读）
	Note           string     `json:"note,omitempty"`
}

func (x Session) holdsSlot() bool {
	return x.Status == StatusLaunching || x.Status == StatusRunning || x.Status == StatusUncertain
}

// Sessions 返回注册表，按创建时间排序。project 为空返回全部。
func (s *Store) Sessions(project string) []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Session
	for _, x := range s.sessions {
		if project == "" || x.Project == project {
			out = append(out, *x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// SessionByTmux 按 tmux session 名找注册表条目（身份判定用）。
func (s *Store) SessionByTmux(tmux string) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.sessions {
		if x.TmuxSession == tmux && tmux != "" {
			return *x, true
		}
	}
	return Session{}, false
}

// Session 按 key 或 agent-deck id 找。
func (s *Store) Session(id string) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if x, ok := s.sessions[id]; ok {
		return *x, true
	}
	for _, x := range s.sessions {
		if x.AgentDeckID == id && id != "" {
			return *x, true
		}
	}
	return Session{}, false
}

// SlotUse 是一个项目当前占用的名额。
type SlotUse struct {
	Subagents int `json:"subagents"`
	Frontier  int `json:"frontier"`
}

// Slots 返回项目当前占用的子 agent 与 frontier 名额。
func (s *Store) Slots(project string) SlotUse {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.slotsLocked(project)
}

func (s *Store) slotsLocked(project string) SlotUse {
	var u SlotUse
	for _, x := range s.sessions {
		if x.Project != project || x.Role != "sub" || !x.holdsSlot() {
			continue
		}
		u.Subagents++
		if x.Tier == Frontier {
			u.Frontier++
		}
	}
	return u
}

// ErrOverEnvelope 表示超出预算包络：可审批例外。
type ErrOverEnvelope struct {
	Project string
	Use     SlotUse
	Env     Envelope
	Reason  string
}

func (e *ErrOverEnvelope) Error() string {
	return fmt.Sprintf("项目 %s 超出预算包络 %s v%d：%s（当前子 agent %d/%d，frontier %d/%d）",
		e.Project, e.Env.ID, e.Env.Version, e.Reason, e.Use.Subagents, e.Env.MaxSubagents, e.Use.Frontier, e.Env.MaxFrontier)
}

// Reservation 是一次启动前的名额预留。
type Reservation struct {
	Key string
}

// Reserve 在锁内「检查并预留」名额，写一条 launching 记录。exception 非空表示已批准的一次性超额例外：
// 它必须是本项目、approved、未过期、scope 与本次请求一致的 cost 决定，用掉后标为 applied 由调用方负责。
func (s *Store) Reserve(x Session, exceptionDecision string) (Reservation, error) {
	var res Reservation
	err := s.mutate(func() (func() error, []Event, error) {
		p, ok := s.settings.Project(x.Project)
		if !ok {
			return nil, nil, fmt.Errorf("没有项目 %s", x.Project)
		}
		if x.Role == "sub" {
			use := s.slotsLocked(x.Project)
			env := p.Envelope
			var why string
			switch {
			case use.Subagents+1 > env.MaxSubagents:
				why = "同时运行的子 agent 已达上限"
			case x.Tier == Frontier && use.Frontier+1 > env.MaxFrontier:
				why = "frontier 子 agent 已达上限"
			}
			if why != "" {
				if exceptionDecision == "" {
					return nil, nil, &ErrOverEnvelope{Project: x.Project, Use: use, Env: env, Reason: why}
				}
				d, ok := s.decs[exceptionDecision]
				if !ok || d.Kind != KindCost || d.Project != x.Project || d.State != StateExecuting {
					return nil, nil, fmt.Errorf("超额例外 %s 无效（须是本项目、正在执行的 cost 决定）", exceptionDecision)
				}
			}
		}
		x.Key = newID("ses")
		x.Status = StatusLaunching
		x.CreatedAt = s.now()
		s.sessions[x.Key] = &x
		res.Key = x.Key
		return s.saveRegistry, []Event{{Kind: "session", Project: x.Project, ID: x.Key}}, nil
	})
	return res, err
}

// LaunchSucceeded 把预留转为 running，记下 agent-deck 会话。
func (s *Store) LaunchSucceeded(key, agentDeckID, tmuxSession string) error {
	return s.setSession(key, func(x *Session) error {
		if x.Status != StatusLaunching && x.Status != StatusUncertain {
			return fmt.Errorf("会话 %s 不在启动中（%s）", key, x.Status)
		}
		x.Status, x.AgentDeckID, x.TmuxSession, x.Note = StatusRunning, agentDeckID, tmuxSession, ""
		return nil
	})
}

// LaunchFailed 明确失败就释放名额；结果不明（uncertain=true）就继续占着。
func (s *Store) LaunchFailed(key string, uncertain bool, why string) error {
	return s.setSession(key, func(x *Session) error {
		if uncertain {
			x.Status = StatusUncertain
		} else {
			x.Status = StatusFailed
		}
		x.Note = Clean(why, 300)
		return nil
	})
}

// SetSessionStatus 改状态（完成、停止等）。
func (s *Store) SetSessionStatus(key, status, note string) error {
	return s.setSession(key, func(x *Session) error {
		x.Status, x.Note = status, Clean(note, 300)
		return nil
	})
}

// ConfirmBrief 记下主 agent 的第一条 brief 已确认送达。
func (s *Store) ConfirmBrief(key string) error {
	return s.setSession(key, func(x *Session) error { x.BriefConfirmed = true; return nil })
}

func (s *Store) setSession(key string, f func(*Session) error) error {
	return s.mutate(func() (func() error, []Event, error) {
		x, ok := s.sessions[key]
		if !ok {
			return nil, nil, fmt.Errorf("注册表里没有会话 %s", key)
		}
		before := *x
		if err := f(x); err != nil {
			*x = before
			return nil, nil, err
		}
		return s.saveRegistry, []Event{{Kind: "session", Project: x.Project, ID: x.Key}}, nil
	})
}

func (s *Store) saveRegistry() error {
	list := make([]*Session, 0, len(s.sessions))
	for _, x := range s.sessions {
		list = append(list, x)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	return writeJSON(s.path("registry.json"), list)
}

// ---------------- Codex trust 记住授权（§14.3） ----------------

// TrustGrant 是 owner 对「在某项目用某工具，允许上游改写 ~/.codex/config.toml」的一次批准。
type TrustGrant struct {
	Project     string    `json:"project"`
	Tool        string    `json:"tool"`
	Dir         string    `json:"dir"`
	ToolPath    string    `json:"tool_path"`
	ToolVersion string    `json:"tool_version"`
	ApprovedAt  time.Time `json:"approved_at"`
}

// TrustValid 判断记住的授权是否仍有效：项目目录、工具路径或版本任一变化都失效（重新问）。
func (s *Store) TrustValid(project, tool, dir, toolPath, toolVersion string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.trust {
		if g.Project == project && g.Tool == tool {
			return g.Dir == dir && g.ToolPath == toolPath && g.ToolVersion == toolVersion
		}
	}
	return false
}

// GrantTrust 记住授权（同项目同工具只留最新一条）。
func (s *Store) GrantTrust(g TrustGrant) error {
	return s.mutate(func() (func() error, []Event, error) {
		var keep []TrustGrant
		for _, x := range s.trust {
			if !(x.Project == g.Project && x.Tool == g.Tool) {
				keep = append(keep, x)
			}
		}
		g.ApprovedAt = s.now()
		s.trust = append(keep, g)
		return func() error { return writeJSON(s.path("trust.json"), s.trust) }, []Event{{Kind: "trust", Project: g.Project}}, nil
	})
}

// RevokeTrust 撤销授权；返回是否真有一条被撤销。
func (s *Store) RevokeTrust(project, tool string) (bool, error) {
	found := false
	err := s.mutate(func() (func() error, []Event, error) {
		var keep []TrustGrant
		for _, x := range s.trust {
			if x.Project == project && x.Tool == tool {
				found = true
				continue
			}
			keep = append(keep, x)
		}
		if !found {
			return nil, nil, nil
		}
		s.trust = keep
		return func() error { return writeJSON(s.path("trust.json"), s.trust) }, []Event{{Kind: "trust", Project: project}}, nil
	})
	return found, err
}

// TrustGrants 列出全部记住的授权。
func (s *Store) TrustGrants() []TrustGrant {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]TrustGrant(nil), s.trust...)
}

// ---------------- 通知 ----------------

// Notification 是通知栏里的一条。append-only，序号递增。
type Notification struct {
	Seq     int64     `json:"seq"`
	At      time.Time `json:"at"`
	Project string    `json:"project,omitempty"`
	Kind    string    `json:"kind"`
	Text    string    `json:"text"`
	Source  string    `json:"source,omitempty"`
}

const keepNotes = 2000

// Notify 追加一条通知。文本先清洗。
func (s *Store) Notify(project, kind, text, source string) (Notification, error) {
	var n Notification
	err := s.mutate(func() (func() error, []Event, error) {
		s.noteSeq++
		n = Notification{Seq: s.noteSeq, At: s.now(), Project: project, Kind: kind, Text: Clean(text, 600), Source: Clean(source, 120)}
		s.notes = append(s.notes, n)
		if len(s.notes) > keepNotes {
			s.notes = s.notes[len(s.notes)-keepNotes:]
		}
		line, _ := json.Marshal(n)
		return func() error { return appendLine(s.path("notifications.jsonl"), line) }, []Event{{Kind: "notification", Project: project}}, nil
	})
	return n, err
}

// Notifications 返回 seq 之后的通知；project 非空只返回该项目与全局的。
func (s *Store) Notifications(after int64, project string) []Notification {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Notification
	for _, n := range s.notes {
		if n.Seq > after && (project == "" || n.Project == "" || n.Project == project) {
			out = append(out, n)
		}
	}
	return out
}

func (s *Store) loadNotes() error {
	f, err := os.Open(s.path("notifications.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var n Notification
		if json.Unmarshal(sc.Bytes(), &n) != nil {
			continue // 最后一行写到一半：跳过，不影响序号连续性判断
		}
		s.notes = append(s.notes, n)
		if n.Seq > s.noteSeq {
			s.noteSeq = n.Seq
		}
	}
	if len(s.notes) > keepNotes {
		s.notes = s.notes[len(s.notes)-keepNotes:]
	}
	return sc.Err()
}

func appendLine(path string, line []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
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

// ScopeHash 给一个 scope 计算稳定的哈希（键排序后 JSON）。
func ScopeHash(scope map[string]string) string {
	keys := make([]string, 0, len(scope))
	for k := range scope {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		kb, _ := json.Marshal(k)
		vb, _ := json.Marshal(scope[k])
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
