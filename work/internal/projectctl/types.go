package projectctl

import "time"

// State 是 Graph 节点的状态，取值与 projectctl 的 NODE_STATES 一一对应。
type State string

const (
	StatePlanned    State = "planned"
	StateReady      State = "ready"
	StateRunning    State = "running"
	StateSubmitted  State = "submitted"
	StateAccepted   State = "accepted"
	StateRework     State = "rework"
	StateBlocked    State = "blocked"
	StateFailed     State = "failed"
	StateSuperseded State = "superseded"
)

// AllStates 是展示顺序：从「要人管的」排到「已经过去的」。
var AllStates = []State{
	StateRunning, StateSubmitted, StateReady, StateRework,
	StateBlocked, StateFailed, StatePlanned, StateAccepted, StateSuperseded,
}

// NeedsOwner 表示这个状态是否需要 owner 出手。
// 提交等评审、被阻塞、失败、打回重做，这四种都停在那里等人。
func (s State) NeedsOwner() bool {
	switch s {
	case StateSubmitted, StateBlocked, StateFailed, StateRework:
		return true
	}
	return false
}

// Node 是一个节点在界面上需要的全部信息。
//
// 静态字段来自 work-graph.yaml，State 来自 projectctl brief，
// Attempt / Actor / Reason / RequiredAction 来自状态投影。
// 来源不同，新鲜度也不同，所以拿不到的一律是 Value 而不是零值。
type Node struct {
	ID         string
	Title      string
	Kind       string
	Risk       string
	RouteClass string
	OwnerRole  string
	DependsOn  []string
	// 这个节点对评审的要求，派评审时照着它来。
	ReviewRequired    bool
	ReviewerRole      string
	ReviewRouteClass  string
	ReviewIndependent bool

	State Value[State]

	// 下面几项只有投影里才有。
	Attempt Value[int]
	Actor   Value[string]
	// 本次 attempt 的开工时间，以及到取数为止走了多久。
	StartedAt      Value[time.Time]
	Elapsed        Value[time.Duration]
	Reason         string
	RequiredAction string
	LatestEvidence string
}

// BudgetLine 是预算表的一行：某个维度用了多少、上限多少。
type BudgetLine struct {
	// 界面上显示的维度名。
	Label string
	// 已用量。projectctl 不导出的维度这里是 Unknown。
	Used Value[float64]
	// 上限。来自 work-graph.yaml 的 policies.project_budget。
	Limit Value[float64]
	// 单位，例如「次」「小时」。
	Unit string
	// UnitKey 是单位的语言无关写法（round、call、hour，没有单位为空），界面按语言翻译；Unit 仍是中文原文。
	UnitKey string
}

// ChainStatus 是账本自身的体检结果，来自 verify-chain。
type ChainStatus struct {
	ChainOK     bool
	SpecOK      bool
	Events      int
	ErrorCount  int
	Errors      []string
	Warnings    []string
	LastEventAt Value[time.Time]
	Stale       bool
	LagDays     Value[float64]
	AuditFailed bool
}

// Freshness 说明状态投影相对账本有多新。
//
// 投影是 projectctl 写命令的副产物，只读的 Work Deck 不会去刷新它，
// 所以它完全可能落后于账本。落后时投影里的数字一律不能当真。
type Freshness struct {
	// 投影文件的生成时间。
	GeneratedAt Value[time.Time]
	// 投影记录的事件游标。
	EventCursor string
	// 投影是否落后于账本。
	Stale bool
	// 落后的说明，直接显示给人看。
	Note string
}

// Snapshot 是一次刷新取到的全部内容。
type Snapshot struct {
	ProjectID     string
	ProjectRoot   string
	GraphRevision Value[int]
	Health        Value[string]

	Nodes   []Node
	Budgets []BudgetLine

	Chain   ChainStatus
	Fresh   Freshness
	TakenAt time.Time
	// 取数过程中出的问题，逐条显示，不吞。
	Problems []string
	// ProblemErrs 和 Problems 一一对应：带码的错误，界面按码翻译；Problems 仍是中文原文。
	ProblemErrs []error
}

// problem 记一条取数问题：文字给旧界面，错误给带码的界面。
func (s *Snapshot) problem(err error) {
	s.Problems = append(s.Problems, err.Error())
	s.ProblemErrs = append(s.ProblemErrs, err)
}

// Counts 按状态统计节点数。状态未知的节点单独计入 unknown 桶。
func (s Snapshot) Counts() (map[State]int, int) {
	counts := make(map[State]int, len(AllStates))
	unknown := 0
	for _, n := range s.Nodes {
		st, ok := n.State.Get()
		if !ok {
			unknown++
			continue
		}
		counts[st]++
	}
	return counts, unknown
}

// WaitingOnOwner 返回需要 owner 出手的节点，按状态的紧要程度排序。
func (s Snapshot) WaitingOnOwner() []Node {
	order := map[State]int{StateBlocked: 0, StateFailed: 1, StateSubmitted: 2, StateRework: 3}
	var out []Node
	for _, n := range s.Nodes {
		if st, ok := n.State.Get(); ok && st.NeedsOwner() {
			out = append(out, n)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			a, _ := out[j].State.Get()
			b, _ := out[j-1].State.Get()
			if order[a] < order[b] {
				out[j], out[j-1] = out[j-1], out[j]
				continue
			}
			break
		}
	}
	return out
}

// Running 返回正在跑的节点。
func (s Snapshot) Running() []Node {
	var out []Node
	for _, n := range s.Nodes {
		if st, ok := n.State.Get(); ok && st == StateRunning {
			out = append(out, n)
		}
	}
	return out
}
