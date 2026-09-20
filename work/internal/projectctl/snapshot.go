package projectctl

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// 状态投影落后账本多久就不能再信。
//
// 投影是写命令的副产物，只读的 Work Deck 不刷新它。只要账本里最新的事件比投影新，
// 投影里的 attempt、actor 和预算数字就可能是上一轮的，一律按未知处理。
const projectionTolerance = 2 * time.Second

// Reader 一次性把界面要的东西全取回来。
type Reader struct {
	client *Client
	root   string
}

// NewReader 按项目根构造。script 是 projectctl.py 的路径。
func NewReader(projectRoot, script string) *Reader {
	return &Reader{client: NewClient(projectRoot, script), root: projectRoot}
}

// Snapshot 取一次完整快照。
//
// 任何一路取数失败都不会让整次刷新失败：拿不到的部分标成未知，原因记进 Problems，
// 其余照常显示。宁可让人看见半张表加一行原因，也不给一个空屏或一组编出来的数。
func (r *Reader) Snapshot(ctx context.Context) Snapshot {
	snap := Snapshot{ProjectRoot: r.root, TakenAt: time.Now()}

	graph, err := LoadGraph(r.root)
	if err != nil {
		snap.Problems = append(snap.Problems, err.Error())
		snap.GraphRevision = Unknown[int]("读不到 work-graph.yaml")
		snap.Health = Unknown[string]("读不到 work-graph.yaml")
		return snap
	}
	snap.ProjectID = graph.ProjectID
	snap.GraphRevision = Known(graph.GraphRevision)

	chain, err := r.client.VerifyChain(ctx)
	if err != nil {
		snap.Problems = append(snap.Problems, "账本体检失败："+err.Error())
		chain.LastEventAt = Unknown[time.Time]("verify-chain 执行失败")
		chain.LagDays = Unknown[float64]("verify-chain 执行失败")
	}
	snap.Chain = chain

	proj, err := LoadProjection(r.root)
	if err != nil {
		snap.Problems = append(snap.Problems, err.Error())
	}
	snap.Health = proj.Health
	snap.Fresh = freshness(proj, chain)

	snap.Nodes = r.nodes(ctx, graph, proj, snap.Fresh, &snap.Problems)
	snap.Budgets = budgets(graph, proj, snap.Fresh)
	return snap
}

// freshness 判断投影还能不能信。
func freshness(p Projection, chain ChainStatus) Freshness {
	f := Freshness{GeneratedAt: p.GeneratedAt, EventCursor: p.EventCursor}

	gen, genOK := p.GeneratedAt.Get()
	if !genOK {
		f.Stale = true
		f.Note = "状态投影没有生成时间，无法判断新旧，其中的数字一律按未知处理"
		return f
	}
	last, lastOK := chain.LastEventAt.Get()
	if !lastOK {
		f.Stale = true
		f.Note = "账本最新事件时间取不到，无法确认状态投影是否落后，其中的数字一律按未知处理"
		return f
	}
	if last.Sub(gen) > projectionTolerance {
		f.Stale = true
		f.Note = fmt.Sprintf("状态投影停在 %s，账本已经走到 %s，投影里的 attempt、actor 和预算都按未知处理",
			gen.Format(time.RFC3339), last.Format(time.RFC3339))
		return f
	}
	f.Note = fmt.Sprintf("状态投影与账本一致，生成于 %s", gen.Format(time.RFC3339))
	return f
}

// nodes 把静态清单、实时状态和投影补充信息合成一张表。
func (r *Reader) nodes(ctx context.Context, g Graph, p Projection, f Freshness, problems *[]string) []Node {
	out := make([]Node, len(g.Nodes))
	now := time.Now()
	var mu sync.Mutex
	var wg sync.WaitGroup
	// 每个节点要单独问一次 projectctl，节点多的时候串行会很慢，这里限并发跑。
	sem := make(chan struct{}, 8)

	for i, gn := range g.Nodes {
		n := Node{
			ID: gn.ID, Title: gn.Title, Kind: gn.Kind, Risk: gn.Risk,
			RouteClass: gn.RouteClass, OwnerRole: gn.OwnerRole, DependsOn: gn.DependsOn,
		}
		// 投影里的补充信息，只有投影还新鲜时才采用。
		if f.Stale {
			n.Attempt = Unknown[int]("状态投影已过期")
			n.Actor = Unknown[string]("状态投影已过期")
			n.StartedAt = Unknown[time.Time]("状态投影已过期")
			n.Elapsed = Unknown[time.Duration]("状态投影已过期")
		} else {
			if a, ok := p.Attempts[gn.ID]; ok {
				n.Attempt = Known(a)
			} else {
				n.Attempt = Unknown[int]("状态投影里没有这个节点的 attempt")
			}
			if a, ok := p.Actors[gn.ID]; ok && a != "" {
				n.Actor = Known(a)
			} else {
				n.Actor = Unknown[string]("状态投影里没有这个节点的执行者")
			}
			n.Reason = p.Reasons[gn.ID]
			n.RequiredAction = p.RequiredActions[gn.ID]
			n.LatestEvidence = p.Evidence[gn.ID]
			// 耗时是「现在减去开工时刻」，是算出来的事实，不是估出来的。
			// 开工时刻只在投影的最近事件表里，查不到就老实说未知。
			if st, ok := p.StartedAt[gn.ID]; ok {
				n.StartedAt = Known(st)
				n.Elapsed = Known(now.Sub(st))
			} else {
				n.StartedAt = Unknown[time.Time]("状态投影的最近事件表里没有这个节点的开工记录")
				n.Elapsed = Unknown[time.Duration]("拿不到开工时刻，无法算耗时")
			}
		}
		out[i] = n

		wg.Add(1)
		go func(idx int, id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			state, err := r.client.BriefState(ctx, id)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				out[idx].State = Unknown[State]("取不到状态：" + err.Error())
				*problems = append(*problems, fmt.Sprintf("节点 %s 状态取不到：%v", id, err))
				return
			}
			out[idx].State = Known(state)
		}(i, gn.ID)
	}
	wg.Wait()
	return out
}

// budgets 组预算表。
//
// worker rounds、frontier calls 和 token 的已用量能从状态投影读到；
// elapsed 这一维度 projectctl 目前不导出任何已用量，所以它只能是未知——
// 从事件时间戳自己算一个出来是推测值，这里不做。
func budgets(g Graph, p Projection, f Freshness) []BudgetLine {
	pb := g.Policies.ProjectBudget

	limit := func(v *float64) Value[float64] {
		if v == nil {
			return Unknown[float64]("work-graph.yaml 没有设这个上限")
		}
		return Known(*v)
	}
	used := func(key string) Value[float64] {
		if f.Stale {
			return Unknown[float64]("状态投影已过期")
		}
		v, ok := p.BudgetUsed[key]
		if !ok {
			return Unknown[float64]("状态投影的预算表里没有这一列")
		}
		return Known(v)
	}

	return []BudgetLine{
		{Label: "worker rounds", Used: used("worker_rounds"), Limit: limit(pb.MaxWorkerRounds), Unit: "轮"},
		{Label: "frontier calls", Used: used("frontier_calls"), Limit: limit(pb.MaxFrontierCalls), Unit: "次"},
		{
			Label: "elapsed",
			Used:  Unknown[float64]("projectctl 不导出 elapsed 已用量，编一个数出来是推测值"),
			Limit: limit(pb.MaxElapsedHours),
			Unit:  "小时",
		},
		{Label: "tokens", Used: used("tokens"), Limit: limit(pb.SoftTokenBudget), Unit: ""},
	}
}
