// Package queue 把「等 owner 拍板」的事汇成一个队列。
//
// 两个来源进同一个队列：Graph 里停在人手上的节点（human gate、blocked），
// 和运行时会话经 upstream agent-deck `inbox export` 报上来的待决事项。
// 条目结构见 schema.json，TestItemsMatchTheSchema 按它校验。
//
// 本包不调上游、不写 Graph 账本：取数由调用方经 adapter 包做好再交进来，
// 回答 Graph 条目只给出要在命令行执行的 projectctl 命令。
package queue

import "time"

// Category 是条目的类别。
type Category string

const (
	// CatGraphHumanGate：Graph 里 kind 为 human_gate、停在 ready 或 running 的节点。
	CatGraphHumanGate Category = "graph_human_gate"
	// CatGraphBlocked：Graph 里 blocked 的节点，等 owner 决定怎么放行。
	CatGraphBlocked Category = "graph_blocked"
	// CatSessionWaiting：会话从 running 翻到 waiting，在等人输入。
	CatSessionWaiting Category = "session_waiting"
	// CatSessionError：会话翻到 error，例如凭据失效、模型不可用。
	CatSessionError Category = "session_error"
	// CatSessionUsageLimit：会话卡在额度上。
	CatSessionUsageLimit Category = "session_usage_limit"
	// CatSessionFinished：会话报了完成，结果等人看。
	CatSessionFinished Category = "session_finished"
)

// AllCategories 是全部类别，schema 的枚举与它逐一对应。
var AllCategories = []Category{
	CatGraphHumanGate, CatGraphBlocked,
	CatSessionWaiting, CatSessionError, CatSessionUsageLimit, CatSessionFinished,
}

// 风险等级，与 Graph 节点的 risk 取值一致。
const (
	RiskLow      = "low"
	RiskMedium   = "medium"
	RiskHigh     = "high"
	RiskCritical = "critical"
)

// AllRisks 是全部风险等级。
var AllRisks = []string{RiskLow, RiskMedium, RiskHigh, RiskCritical}

// 推荐的置信度。
//
// 用档位不用小数：推荐是按固定规则给的，不是模型算出来的，写成 0.83 会假装有精度。
// none 表示本条不给推荐（例如 Graph 门，Work Deck 不替 owner 裁决）。
const (
	ConfidenceNone   = "none"
	ConfidenceLow    = "low"
	ConfidenceMedium = "medium"
	ConfidenceHigh   = "high"
)

// AllConfidences 是全部置信度档位。
var AllConfidences = []string{ConfidenceNone, ConfidenceLow, ConfidenceMedium, ConfidenceHigh}

// 条目状态。
const (
	// StatusOpen：还没人回答。
	StatusOpen = "open"
	// StatusAnswered：会话条目已回答（投递成功，或选了不发消息的选项）。
	// Graph 条目不会进这个状态：选了选项只是拿到命令，门还在，直到 Graph 状态变了才转 resolved。
	StatusAnswered = "answered"
	// StatusResolved：来源那一头已经不再等了（Graph 节点换了状态）。
	StatusResolved = "resolved"
)

// AllStatuses 是全部条目状态。
var AllStatuses = []string{StatusOpen, StatusAnswered, StatusResolved}

// 来源类别。
const (
	SourceGraph   = "graph"
	SourceSession = "session"
)

// Option 是一个可选的回答。
//
// 会话条目的选项带 Message：选中后把这段文字投递回发起会话；Message 为空表示只记下
// 「已处理」、不给会话发任何东西。Graph 条目的选项带 Command：Work Deck 不写账本，
// 选中后把这条命令交给 owner 自己执行。
type Option struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Message string `json:"message,omitempty"`
	Command string `json:"command,omitempty"`
}

// Recommendation 是推荐的选项与它的置信度。
//
// Option 为空串且 Confidence 为 none 表示不给推荐。Basis 说明推荐从哪条规则来。
type Recommendation struct {
	Option     string `json:"option"`
	Confidence string `json:"confidence"`
	Basis      string `json:"basis"`
}

// Source 记下条目从哪来，回答时靠它找回去。
type Source struct {
	Kind string `json:"kind"`
	// Graph 条目。
	NodeID  string `json:"node_id,omitempty"`
	Attempt int    `json:"attempt,omitempty"`
	State   string `json:"state,omitempty"`
	// 会话条目：发起会话与它所在的 profile。
	SessionID string `json:"session_id,omitempty"`
	Profile   string `json:"profile,omitempty"`
	// DoneStatus 是完成回报里的 ok/fail（小写），只有 session_finished 条目有。n039 的额度观测用它。
	DoneStatus string `json:"done_status,omitempty"`
	// ReportedAt 是上游记录里这件事发生的时间（第一次读到时的值，之后重复上报不改）。
	// n039 的额度观测按它算有效期；用 LastSeen 的话，每一轮轮询都会把旧记录刷成「新鲜」。
	ReportedAt *time.Time `json:"reported_at,omitempty"`
	// 会话条目在最近一次 export 里是否还在。conductor drain 或上游清扫后，记录会消失；
	// 条目保留（owner 可能还没看到），但要标出来。
	Present bool `json:"present"`
}

// Answer 是 owner 的回答与投递结果。
type Answer struct {
	Option     string    `json:"option"`
	AnsweredAt time.Time `json:"answered_at"`
	// Delivered 只对会话条目有意义：文字是否已投递回发起会话。
	Delivered     bool   `json:"delivered"`
	DeliveryError string `json:"delivery_error,omitempty"`
	// Note 是给人看的说明，例如 Graph 条目要 owner 执行的命令。
	Note string `json:"note,omitempty"`
	// InFlight 为真表示投递已开始、结果还没记下。进程若在这时中断，重启后看到的就是它：
	// 结果未知，不自动重投（重投可能让对方收到两遍）。
	InFlight bool `json:"in_flight,omitempty"`
}

// Item 是队列里的一条待决事项。
type Item struct {
	ID               string         `json:"id"`
	Category         Category       `json:"category"`
	Risk             string         `json:"risk"`
	Title            string         `json:"title"`
	Detail           string         `json:"detail"`
	Options          []Option       `json:"options"`
	Recommendation   Recommendation `json:"recommendation"`
	EvidenceRefs     []string       `json:"evidence_refs"`
	BlockingSessions []string       `json:"blocking_sessions"`
	// Deadline 为空表示来源没有给截止时间。Graph 与上游 inbox 目前都不提供，所以现在总是空。
	Deadline  *time.Time `json:"deadline"`
	Source    Source     `json:"source"`
	FirstSeen time.Time  `json:"first_seen"`
	LastSeen  time.Time  `json:"last_seen"`
	Status    string     `json:"status"`
	Answer    *Answer    `json:"answer"`
}

// OptionByKey 找一个选项。
func (it Item) OptionByKey(key string) (Option, bool) {
	for _, o := range it.Options {
		if o.Key == key {
			return o, true
		}
	}
	return Option{}, false
}
