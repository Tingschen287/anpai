package projectctl

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// 状态投影落后账本多久就不能再信。
//
// 投影是写命令的副产物，只读的 Work Deck 不刷新它。只要账本里最新的事件比投影新，
// 投影里的 attempt、actor 和预算数字就可能是上一轮的，一律按未知处理。
const projectionTolerance = 2 * time.Second

// Reader 一次性把界面要的东西全取回来。
//
// 它记着已经查到的终态。真实项目里一次 brief 要两三秒，而控制器对项目目录加了锁，
// 实测并发 1、4、8、16 跑同样 12 个节点都是 33 到 35 秒——并发一点用都没有，
// 唯一能省时间的办法是少问几次。accepted、failed、superseded 不会再变回去，
// 问过一次就记住。
type Reader struct {
	client *Client
	root   string

	mu    sync.Mutex
	final map[string]State
	// 终态缓存的落盘位置。空串表示只在内存里记，不持久化。
	cachePath string
}

// NewReader 按项目根构造。script 是 projectctl.py 的路径。
func NewReader(projectRoot, script string) *Reader {
	return &Reader{
		client: NewClient(projectRoot, script),
		root:   projectRoot,
		final:  map[string]State{},
	}
}

// WithCache 让终态缓存落盘。
//
// accepted / failed / superseded 是终态，不会再变回去。只记在内存里的话，
// 每次重开都要把几十个节点重新问一遍，真实项目上那是近两分钟的等待。
// 存到盘上，第二次打开就是秒出。
//
// 缓存坏了或读不出来不算错误：大不了重新问一遍，不能因为缓存而打不开。
func (r *Reader) WithCache(path string) *Reader {
	r.cachePath = path
	return r
}

type cacheFile struct {
	Project string           `json:"project"`
	States  map[string]State `json:"states"`
	// Events 是写缓存时账本里的事件条数。
	//
	// 缓存只在账本没往前走时才可信。账本一旦有新事件，任何一个节点都可能
	// 已经改了状态，此时宁可全部重查，也不能拿旧值糊弄人。
	Events int `json:"events"`
}

// loadCache 读缓存。events 是账本当前的事件条数，用来判断缓存还新不新。
func (r *Reader) loadCache(events int) {
	if r.cachePath == "" {
		return
	}
	data, err := os.ReadFile(r.cachePath)
	if err != nil {
		return
	}
	var f cacheFile
	if err := json.Unmarshal(data, &f); err != nil {
		return
	}
	// 缓存是按项目存的，换了项目就不能用。
	if f.Project != r.root {
		return
	}
	// 账本往前走过就整份作废。哪怕只多了一条事件，也可能是某个节点
	// 被 resume 或 superseded 了，逐个判断不如全部重查来得诚实。
	if events <= 0 || f.Events != events {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, st := range f.States {
		if isFinal(st) {
			r.final[id] = st
		}
	}
}

// dropStale 把与权威状态不符的缓存踢掉。
//
// 投影或控制器说某个节点不是终态了，缓存里那条就是错的，立刻扔掉，
// 不要等到下次重启。
func (r *Reader) dropStale(id string, actual State) {
	if isFinal(actual) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.final, id)
}

// saveCache 把终态写盘。调用方不得持有锁。
func (r *Reader) saveCache(events int) {
	if r.cachePath == "" {
		return
	}
	r.mu.Lock()
	f := cacheFile{Project: r.root, Events: events, States: make(map[string]State, len(r.final))}
	for id, st := range r.final {
		f.States[id] = st
	}
	r.mu.Unlock()

	data, err := json.Marshal(f)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.cachePath), 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(r.cachePath), ".states-*.tmp")
	if err != nil {
		return
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return
	}
	if err := os.Rename(name, r.cachePath); err != nil {
		os.Remove(name)
	}
}

// isFinal 说明一个状态到没到头。到头的状态不会自己变回去，可以记住。
//
// failed **不在此列**。控制器的状态机允许 node_resumed 把 failed 转回 ready，
// 也允许它转成 superseded（projectctl.py 的 TRANSITIONS）。把它当终态缓存，
// 节点恢复之后界面会一直显示旧的 failed——缓存说谎比慢更糟。
//
// accepted 与 superseded 在该状态机里没有任何出边，是真正到头的。
func isFinal(s State) bool {
	switch s {
	case StateAccepted, StateSuperseded:
		return true
	}
	return false
}

func (r *Reader) cached(id string) (State, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.final[id]
	return st, ok
}

func (r *Reader) remember(id string, st State) {
	if !isFinal(st) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.final[id] = st
}

// FastSnapshot 只用状态投影和账本体检取数，一次控制器查询都不为单个节点发。
//
// 投影的三张分类表已经写明了 ready、running、submitted、blocked、failed 这五类
// 节点的状态，等 owner 的条目、运行中的 attempt 和预算也全在里面。界面先拿这一份
// 立刻画出来，剩下那些只能靠逐个查的节点标成「查询中」，再由 Snapshot 补。
func (r *Reader) FastSnapshot(ctx context.Context) Snapshot {
	return r.snapshot(ctx, false)
}

// Snapshot 取一次完整快照，会为投影覆盖不到的节点逐个问控制器。
//
// 任何一路取数失败都不会让整次刷新失败：拿不到的部分标成未知，原因记进 Problems，
// 其余照常显示。宁可让人看见半张表加一行原因，也不给一个空屏或一组编出来的数。
func (r *Reader) Snapshot(ctx context.Context) Snapshot {
	return r.snapshot(ctx, true)
}

func (r *Reader) snapshot(ctx context.Context, probe bool) Snapshot {
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

	// 账本条数决定缓存还能不能信，所以体检之后才读缓存。
	r.loadCache(chain.Events)

	proj, err := LoadProjection(r.root)
	if err != nil {
		snap.Problems = append(snap.Problems, err.Error())
	}
	snap.Health = proj.Health
	snap.Fresh = freshness(proj, chain)

	snap.Nodes = r.nodes(ctx, graph, proj, snap.Fresh, probe, &snap.Problems)
	snap.Budgets = budgets(graph, proj, snap.Fresh)
	if probe {
		// 问过一轮就把终态记到盘上，下次开就不用再问了。
		r.saveCache(chain.Events)
	}
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
//
// 状态按三步取，一步比一步贵：投影里写明的直接用，记住的终态直接用，
// 剩下的才去问控制器。probe 为 false 时最后一步不做，那些节点标成「查询中」。
func (r *Reader) nodes(ctx context.Context, g Graph, p Projection, f Freshness, probe bool, problems *[]string) []Node {
	out := make([]Node, len(g.Nodes))
	now := time.Now()
	var mu sync.Mutex
	var wg sync.WaitGroup
	// 控制器对项目目录加锁，并发提高并不会更快，这里只留一点重叠。
	sem := make(chan struct{}, 4)

	for i, gn := range g.Nodes {
		n := Node{
			ID: gn.ID, Title: gn.Title, Kind: gn.Kind, Risk: gn.Risk,
			RouteClass: gn.RouteClass, OwnerRole: gn.OwnerRole, DependsOn: gn.DependsOn,
			ReviewRequired:    gn.ReviewPolicy.Required,
			ReviewerRole:      gn.ReviewPolicy.ReviewerRole,
			ReviewRouteClass:  gn.ReviewPolicy.RouteClass,
			ReviewIndependent: gn.ReviewPolicy.IndependentFromExec,
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

		// 第一步：投影新鲜且写明了状态，直接用，省一次查询。
		if st, ok := p.States[gn.ID]; ok && !f.Stale {
			n.State = Known(st)
			r.remember(gn.ID, st)
			// 权威说它不是终态，缓存里若还留着旧的终态，当场扔掉。
			r.dropStale(gn.ID, st)
			out[i] = n
			continue
		}
		// 第二步：之前问到过的终态，不会再变。
		if st, ok := r.cached(gn.ID); ok {
			n.State = Known(st)
			out[i] = n
			continue
		}
		// 第三步：只能去问控制器了。
		if !probe {
			n.State = Unknown[State]("查询中")
			out[i] = n
			continue
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
			r.remember(id, state)
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
