// Package binding 记住「哪个节点的哪次 attempt，跑在哪个会话里」。
//
// 这份对应关系存在 Work Deck 自己的文件里，不往 upstream agent-deck 的 schema
// 里加任何字段：上游的数据结构是它自己的事，往里塞外人的字段，升级时第一个碎。
//
// 存储要经得起 Work Deck 重启，所以每次改动都落盘，且是原子落盘——
// 写一半断电留下的半个文件，比没有文件更难查。
package binding

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Binding 是一条绑定记录。
type Binding struct {
	NodeID  string `json:"node_id"`
	Attempt int    `json:"attempt"`
	// 上游回传的会话号。这是整条记录的锚，拿不到它就不该有记录。
	SessionID string `json:"session_id"`

	// 派工时说清楚的事，一条都不能少，事后要靠它们核对派工纪律。
	Tool       string `json:"tool"`
	Model      string `json:"model"`
	RouteClass string `json:"route_class"`

	ProjectPath string `json:"project_path"`
	Profile     string `json:"profile"`
	TmuxSession string `json:"tmux_session,omitempty"`

	DispatchedAt time.Time `json:"dispatched_at"`
	DispatchedBy string    `json:"dispatched_by"`
}

// Key 是一次 attempt 的唯一标识。
func (b Binding) Key() string { return fmt.Sprintf("%s#%d", b.NodeID, b.Attempt) }

// Validate 检查一条记录说不说得清楚。
func (b Binding) Validate() error {
	switch {
	case b.NodeID == "":
		return fmt.Errorf("绑定缺少节点号")
	case b.Attempt <= 0:
		return fmt.Errorf("绑定的 attempt 必须是正数，拿到的是 %d", b.Attempt)
	case b.SessionID == "":
		return fmt.Errorf("绑定缺少会话号")
	case b.Model == "":
		return fmt.Errorf("绑定缺少模型：派工时模型未知就不该落下这条记录")
	case b.RouteClass == "":
		return fmt.Errorf("绑定缺少 route class")
	}
	return nil
}

// file 是落盘格式。带版本号，将来改结构时好辨认。
type file struct {
	Version  int       `json:"version"`
	Bindings []Binding `json:"bindings"`
}

const currentVersion = 1

// Store 是绑定的存放处。
type Store struct {
	path string

	mu    sync.Mutex
	items map[string]Binding
}

// Open 打开或新建一个存储。文件不存在时给一个空的，不报错。
func Open(path string) (*Store, error) {
	s := &Store{path: path, items: map[string]Binding{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("读不到绑定存储 %s：%w", path, err)
	}
	if len(data) == 0 {
		return s, nil
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		// 存储坏了要说出来，不要静默当成空的——那会让人以为派工记录丢了是正常的。
		return nil, fmt.Errorf("绑定存储 %s 解析失败：%w", path, err)
	}
	for _, b := range f.Bindings {
		s.items[b.Key()] = b
	}
	return s, nil
}

// Put 写入一条绑定并立即落盘。
func (s *Store) Put(b Binding) error {
	if err := b.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[b.Key()] = b
	return s.flushLocked()
}

// Get 取某个节点某次 attempt 的绑定。
func (s *Store) Get(nodeID string, attempt int) (Binding, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.items[fmt.Sprintf("%s#%d", nodeID, attempt)]
	return b, ok
}

// List 返回全部绑定，按节点号与 attempt 排序，保证输出稳定。
func (s *Store) List() []Binding {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Binding, 0, len(s.items))
	for _, b := range s.items {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].NodeID != out[j].NodeID {
			return out[i].NodeID < out[j].NodeID
		}
		return out[i].Attempt < out[j].Attempt
	})
	return out
}

// BySession 反查某个会话对应的绑定。
func (s *Store) BySession(sessionID string) (Binding, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.items {
		if b.SessionID == sessionID {
			return b, true
		}
	}
	return Binding{}, false
}

// flushLocked 原子落盘：先写同目录的临时文件，再改名顶上。
// 调用方必须已经持有锁。
func (s *Store) flushLocked() error {
	f := file{Version: currentVersion, Bindings: make([]Binding, 0, len(s.items))}
	for _, b := range s.items {
		f.Bindings = append(f.Bindings, b)
	}
	sort.Slice(f.Bindings, func(i, j int) bool {
		if f.Bindings[i].NodeID != f.Bindings[j].NodeID {
			return f.Bindings[i].NodeID < f.Bindings[j].NodeID
		}
		return f.Bindings[i].Attempt < f.Bindings[j].Attempt
	})

	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".bindings-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}
