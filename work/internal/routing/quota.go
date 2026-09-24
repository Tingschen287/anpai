package routing

import (
	"fmt"
	"sort"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/queue"
)

// ObservationFreshFor 是一条额度观测的有效期。更早的观测不算数，额度回到 unknown。
const ObservationFreshFor = 6 * time.Hour

// Observation 是 Work Deck 自己看到的一次会话结果，折算成对某个模型额度的判断。
type Observation struct {
	Model  string
	State  string
	At     time.Time
	Source string
}

// Observed 用 Work Deck 自己的观测回答额度问题。
type Observed struct {
	Obs []Observation
}

// QuotaOf 取有效期内最新的一条观测；没有就是 unknown，并说明为什么。
func (o Observed) QuotaOf(model string, now time.Time) Quota {
	var best *Observation
	for i := range o.Obs {
		ob := o.Obs[i]
		if ob.Model != model || now.Sub(ob.At) > ObservationFreshFor || ob.At.After(now) {
			continue
		}
		if best == nil || ob.At.After(best.At) {
			best = &ob
		}
	}
	if best == nil {
		return Quota{State: QuotaUnknown, Source: fmt.Sprintf("Work Deck 最近 %d 小时没观测到这个模型的会话结果", int(ObservationFreshFor.Hours()))}
	}
	at := best.At
	return Quota{State: best.State, ObservedAt: &at, Source: best.Source}
}

// ObservationsFromQueue 从决策队列里取 Work Deck 读到的会话结果，按绑定折算到模型：
//   - 卡在额度上（session_usage_limit）→ exhausted；
//   - 完成回报且 done_status 为 ok → available；
//   - 其余（出错、等待、失败的完成）不说明额度，不折算。
//
// modelOf 把会话号映射到派它时用的模型（取自 Work Deck 的绑定表）；不是 Work Deck 派的会话映射不到，跳过。
// 这里不读任何 CLI 的用量回执：看的是「会话实际停在了哪儿」，不是它自报用了多少。
func ObservationsFromQueue(items []queue.Item, modelOf func(sessionID string) (string, bool)) []Observation {
	var out []Observation
	for _, it := range items {
		if it.Source.Kind != queue.SourceSession || modelOf == nil {
			continue
		}
		model, ok := modelOf(it.Source.SessionID)
		if !ok || model == "" {
			continue
		}
		switch {
		case it.Category == queue.CatSessionUsageLimit:
			out = append(out, Observation{Model: model, State: QuotaExhausted, At: it.LastSeen,
				Source: "Work Deck 读到会话 " + it.Source.SessionID + " 卡在额度上（inbox 记录 substate=usage-limit）"})
		case it.Category == queue.CatSessionFinished && it.Source.DoneStatus == "ok":
			out = append(out, Observation{Model: model, State: QuotaAvailable, At: it.LastSeen,
				Source: "Work Deck 读到会话 " + it.Source.SessionID + " 正常完成"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// Live 在每次派单时现取决策队列与绑定表，算出额度。队列或绑定表没接上时一律 unknown。
type Live struct {
	Items   func() []queue.Item
	ModelOf func(sessionID string) (string, bool)
}

// QuotaOf 实现 QuotaSource。
func (l Live) QuotaOf(model string, now time.Time) Quota {
	if l.Items == nil || l.ModelOf == nil {
		return Quota{State: QuotaUnknown, Source: "决策队列或绑定表没接上，Work Deck 没有额度观测"}
	}
	return Observed{Obs: ObservationsFromQueue(l.Items(), l.ModelOf)}.QuotaOf(model, now)
}
