package queue

import (
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

// MergeGraph 把一份 Graph 快照里的门并进队列。交互界面与 --once 都走这里，判断只有一份。
//
// 两种情形不能拿来结案旧门，因为那是「没取到」，不是「门没了」：
//   - 快照零节点且带着取数问题：整份没取到，本轮不合并，skipped 为真。
//   - 某个节点在快照里、但状态取不到：它原有的条目原样保留，不转 resolved。
//
// 零节点且没有问题，是合法的空 Graph：照常合并，旧门转为 resolved。
func MergeGraph(s *Store, snap projectctl.Snapshot, b Bindings, script string, now time.Time) (added int, skipped bool, err error) {
	if len(snap.Nodes) == 0 && len(snap.Problems) > 0 {
		return 0, true, nil
	}
	unknown := map[string]bool{}
	for _, n := range snap.Nodes {
		if _, ok := n.State.Get(); !ok {
			unknown[n.ID] = true
		}
	}
	keep := func(it Item) bool { return unknown[it.Source.NodeID] }
	added, err = s.mergeKeeping(SourceGraph, FromGraph(snap, b, script, now), now, keep)
	return added, false, err
}
