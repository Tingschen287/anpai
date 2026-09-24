package queue

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// 落盘格式带版本号，将来改结构时好辨认。
const currentVersion = 1

type file struct {
	Version int    `json:"version"`
	Items   []Item `json:"items"`
}

// Store 是持久化的队列。
//
// 每次改动都原子落盘（写临时文件再 rename）：崩溃时磁盘上要么是改动前的完整文件，
// 要么是改动后的完整文件，不会留下半个。重启时 Open 读回来，已回答的条目保持已回答。
type Store struct {
	path  string
	mu    sync.Mutex
	items map[string]*Item
}

// Open 打开队列文件；文件不存在就是空队列。
//
// 文件存在但解析不了时报错，不当成空队列：那样会把 owner 已经回答过的记录悄悄丢掉。
func Open(path string) (*Store, error) {
	s := &Store{path: path, items: map[string]*Item{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读队列文件失败：%w", err)
	}
	// items 用指针接：缺这个字段（例如 {"version":1}）与「有、但是空数组」要分得开。
	var f struct {
		Version int     `json:"version"`
		Items   *[]Item `json:"items"`
	}
	bad := func(why string) error {
		return fmt.Errorf("队列文件 %s %s。不当成空队列打开，免得丢掉已回答的记录；请先检查或移走它", path, why)
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, bad(fmt.Sprintf("解析不了（%v）", err))
	}
	if f.Version != currentVersion {
		return nil, bad(fmt.Sprintf("版本是 %d，本程序只认 %d", f.Version, currentVersion))
	}
	if f.Items == nil {
		return nil, bad("缺少 items 字段")
	}
	for i := range *f.Items {
		it := (*f.Items)[i]
		if err := validateItem(it); err != nil {
			return nil, bad(fmt.Sprintf("第 %d 条不完整（%v）", i+1, err))
		}
		if _, dup := s.items[it.ID]; dup {
			return nil, bad("有重复的条目 " + it.ID)
		}
		s.items[it.ID] = &it
	}
	return s, nil
}

// validateItem 检查读回来的条目说不说得清楚。字段集以 schema.json 为准，这里只挡
// 读回后会让队列行为出错的缺项：没有 id、来源或状态不认识、没有选项。
func validateItem(it Item) error {
	switch {
	case it.ID == "":
		return fmt.Errorf("缺 id")
	case !contains(categoryStrings(), string(it.Category)):
		return fmt.Errorf("类别 %q 不认识", it.Category)
	case it.Source.Kind != SourceGraph && it.Source.Kind != SourceSession:
		return fmt.Errorf("来源 %q 不认识", it.Source.Kind)
	case !contains(AllStatuses, it.Status):
		return fmt.Errorf("状态 %q 不认识", it.Status)
	case len(it.Options) == 0:
		return fmt.Errorf("没有选项")
	}
	return nil
}

func categoryStrings() []string {
	out := make([]string, 0, len(AllCategories))
	for _, c := range AllCategories {
		out = append(out, string(c))
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Merge 把一轮取到的条目并进队列，返回新增的条数。
//
// fresh 是这一轮某个来源（graph 或 session）取到的全部条目，kind 说明是哪个来源。
// 同一 id 已在队列里的是重复上报：只刷新 LastSeen 与内容字段，状态与回答原样保留。
// 这一轮没再出现的旧条目：graph 条目说明节点已换状态，open 的标为 resolved；
// session 条目只标 Present=false（记录可能被 conductor drain 走了，owner 未必看过）。
func (s *Store) Merge(kind string, fresh []Item, now time.Time) (int, error) {
	return s.mergeKeeping(kind, fresh, now, nil)
}

// mergeKeeping 与 Merge 相同，另外 keep 返回真的旧条目即使这一轮没出现也原样保留
// （不转 resolved、不标 present=false）。用于「这一轮没取到它的状态」的情形。
func (s *Store) mergeKeeping(kind string, fresh []Item, now time.Time, keep func(Item) bool) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 落盘失败时整轮退回：先留一份原样。
	backup := make(map[string]Item, len(s.items))
	for id, it := range s.items {
		backup[id] = *it
	}
	restore := func() {
		s.items = make(map[string]*Item, len(backup))
		for id, it := range backup {
			it := it
			s.items[id] = &it
		}
	}
	seen := map[string]bool{}
	added := 0
	for _, f := range fresh {
		if f.Source.Kind != kind {
			restore()
			return 0, fmt.Errorf("条目 %s 的来源是 %q，不是这一轮合并的 %q", f.ID, f.Source.Kind, kind)
		}
		seen[f.ID] = true
		if old, ok := s.items[f.ID]; ok {
			old.Category, old.Risk, old.Title, old.Detail = f.Category, f.Risk, f.Title, f.Detail
			old.Options, old.Recommendation = f.Options, f.Recommendation
			old.EvidenceRefs, old.BlockingSessions, old.Deadline = f.EvidenceRefs, f.BlockingSessions, f.Deadline
			old.Source = f.Source
			old.LastSeen = now
			if kind == SourceGraph && old.Status == StatusResolved {
				// 节点又回到了同一个 attempt 的同一状态：重新打开。
				old.Status = StatusOpen
			}
			continue
		}
		it := f
		it.FirstSeen, it.LastSeen = now, now
		if it.Status == "" {
			it.Status = StatusOpen
		}
		s.items[it.ID] = &it
		added++
	}
	for id, it := range s.items {
		if it.Source.Kind != kind || seen[id] {
			continue
		}
		if keep != nil && keep(*it) {
			continue
		}
		switch kind {
		case SourceGraph:
			if it.Status == StatusOpen {
				it.Status = StatusResolved
			}
		case SourceSession:
			it.Source.Present = false
		}
	}
	if err := s.flushLocked(); err != nil {
		restore()
		return 0, err
	}
	return added, nil
}

// Record 记下 owner 对一个条目的回答，并落盘。
func (s *Store) Record(id string, ans Answer) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.items[id]
	if !ok {
		return Item{}, fmt.Errorf("队列里没有条目 %s", id)
	}
	if _, ok := it.OptionByKey(ans.Option); !ok {
		return Item{}, fmt.Errorf("条目 %s 没有选项 %q", id, ans.Option)
	}
	prevAnswer, prevStatus := it.Answer, it.Status
	a := ans
	a.InFlight = false
	it.Answer = &a
	// 投递失败的不算答完：留在 open，owner 能看到失败原因并重试。
	// Graph 条目选了选项也不算答完：门还在，Graph 状态变了才转 resolved。
	if a.DeliveryError == "" && it.Source.Kind == SourceSession {
		it.Status = StatusAnswered
	}
	if err := s.flushLocked(); err != nil {
		// 没落盘就不算：内存退回原样，免得下一次别的落盘把它带上去。
		it.Answer, it.Status = prevAnswer, prevStatus
		return Item{}, err
	}
	return *it, nil
}

// MarkInFlight 在投递之前把「正在投递」落盘。
func (s *Store) MarkInFlight(id, option string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.items[id]
	if !ok {
		return fmt.Errorf("队列里没有条目 %s", id)
	}
	prev := it.Answer
	it.Answer = &Answer{Option: option, AnsweredAt: now, InFlight: true}
	if err := s.flushLocked(); err != nil {
		// 标记没落盘：内存退回原样，否则下一次成功的 Merge 会把一个实际没发的在途标记写上去。
		it.Answer = prev
		return err
	}
	return nil
}

// ResolveInFlight 是 owner 预览过会话之后，对一次中断的投递下结论。
//
// received 为真：对方确实收到了，条目标为已回答。为假：没收到，清掉标记，条目回到待拍板，
// owner 可以照常再选一次选项投递。两种都不发任何消息——要不要再发，由 owner 下一步明确选。
func (s *Store) ResolveInFlight(id string, received bool, now time.Time) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.items[id]
	if !ok {
		return Item{}, fmt.Errorf("队列里没有条目 %s", id)
	}
	if it.Answer == nil || !it.Answer.InFlight {
		return Item{}, fmt.Errorf("条目 %s 没有中断的投递", id)
	}
	prevAnswer, prevStatus := it.Answer, it.Status
	if received {
		a := *it.Answer
		a.InFlight, a.Delivered = false, true
		a.Note = "投递中断后，owner 预览确认会话已收到"
		it.Answer = &a
		it.Status = StatusAnswered
	} else {
		it.Answer = nil
		it.Status = StatusOpen
	}
	if err := s.flushLocked(); err != nil {
		it.Answer, it.Status = prevAnswer, prevStatus
		return Item{}, err
	}
	return *it, nil
}

// Get 取一个条目。
func (s *Store) Get(id string) (Item, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.items[id]
	if !ok {
		return Item{}, false
	}
	return *it, true
}

// Items 按展示顺序返回全部条目：open 在前，其中风险高的在前，同风险按先来后到；
// 之后是 answered，最后 resolved。
func (s *Store) Items() []Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Item, 0, len(s.items))
	for _, it := range s.items {
		out = append(out, *it)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ra, rb := statusRank(a.Status), statusRank(b.Status); ra != rb {
			return ra < rb
		}
		if ra, rb := riskRank(a.Risk), riskRank(b.Risk); ra != rb {
			return ra < rb
		}
		if !a.FirstSeen.Equal(b.FirstSeen) {
			return a.FirstSeen.Before(b.FirstSeen)
		}
		return a.ID < b.ID
	})
	return out
}

// OpenCount 统计还没回答的条目数。
func (s *Store) OpenCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, it := range s.items {
		if it.Status == StatusOpen {
			n++
		}
	}
	return n
}

func statusRank(s string) int {
	switch s {
	case StatusOpen:
		return 0
	case StatusAnswered:
		return 1
	}
	return 2
}

func riskRank(r string) int {
	switch r {
	case RiskCritical:
		return 0
	case RiskHigh:
		return 1
	case RiskMedium:
		return 2
	}
	return 3
}

func (s *Store) flushLocked() error {
	items := make([]Item, 0, len(s.items))
	for _, it := range s.items {
		items = append(items, *it)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	data, err := json.MarshalIndent(file{Version: currentVersion, Items: items}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".queue-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, s.path)
}
