package deckd

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// bus 是 SSE 的事件总线（架构稿 §3）。
//
// 每条事件带「代号:序号」形式的 id：代号每次 deckd 启动随机生成，序号从 1 递增。客户端重连时带 Last-Event-ID：
// 代号相同且序号仍在环形缓冲里就补发其后的事件；否则（deckd 重启过、或落后太多）先发一条 resync，
// 客户端据此重新取全量快照。快照接口返回快照对应的 id，订阅从这个 id 之后开始，避免「先取快照再订阅」漏事件。
// 跟不上的订阅者（缓冲满）被断开，重连后同样走 resync。
type bus struct {
	mu    sync.Mutex
	epoch string
	seq   int64
	ring  []busEvent
	subs  map[*subscriber]struct{}
}

type busEvent struct {
	Seq   int64
	Event deckstore.Event
}

type subscriber struct {
	ch      chan busEvent
	project string // 非空：只收本项目与全局事件
	closed  bool
}

const ringSize = 512

func newBus() *bus {
	b := make([]byte, 4)
	rand.Read(b)
	return &bus{epoch: hex.EncodeToString(b), subs: map[*subscriber]struct{}{}}
}

// ID 返回某个序号的事件 id。
func (b *bus) ID(seq int64) string { return fmt.Sprintf("%s:%d", b.epoch, seq) }

// Current 返回当前最新事件的 id（快照用）。
func (b *bus) Current() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ID(b.seq)
}

func (b *bus) publish(ev deckstore.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	be := busEvent{Seq: b.seq, Event: ev}
	b.ring = append(b.ring, be)
	if len(b.ring) > ringSize {
		b.ring = b.ring[len(b.ring)-ringSize:]
	}
	for s := range b.subs {
		if !s.wants(ev) {
			continue
		}
		select {
		case s.ch <- be:
		default:
			// 跟不上：断开，让它重连后 resync
			s.closed = true
			close(s.ch)
			delete(b.subs, s)
		}
	}
}

func (s *subscriber) wants(ev deckstore.Event) bool {
	return s.project == "" || ev.Project == "" || ev.Project == s.project
}

// subscribe 从 lastID 之后开始订阅。返回需要补发的事件、是否需要 resync、以及订阅者。
func (b *bus) subscribe(lastID, project string) ([]busEvent, bool, *subscriber) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := &subscriber{ch: make(chan busEvent, 64), project: project}
	b.subs[s] = struct{}{}
	if lastID == "" {
		return nil, false, s
	}
	epoch, seqStr, ok := strings.Cut(lastID, ":")
	seq, err := strconv.ParseInt(seqStr, 10, 64)
	if !ok || err != nil || epoch != b.epoch {
		return nil, true, s
	}
	if seq == b.seq {
		return nil, false, s
	}
	if len(b.ring) == 0 || seq < b.ring[0].Seq-1 || seq > b.seq {
		return nil, true, s
	}
	var replay []busEvent
	for _, e := range b.ring {
		if e.Seq > seq && s.wants(e.Event) {
			replay = append(replay, e)
		}
	}
	return replay, false, s
}

func (b *bus) unsubscribe(s *subscriber) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.subs[s]; ok {
		delete(b.subs, s)
		if !s.closed {
			s.closed = true
			close(s.ch)
		}
	}
}
