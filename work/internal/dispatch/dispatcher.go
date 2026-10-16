package dispatch

import (
	"github.com/Tingschen287/terminal-deck/work/internal/routing"

	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/binding"
)

// Request 是派一次工要说清楚的全部事情。
type Request struct {
	NodeID  string
	Attempt int
	// 节点要求的档位，来自 Graph，不是调用方随口给的。
	RequiredClass RouteClass
	// 要用的模型 ID。必填。
	ModelID string

	ProjectPath string
	Title       string
	Group       string
	Message     string
	// 谁派的工，记进账。
	DispatchedBy string
	// ChosenBy 是挑模型的人，ChoiceReason 是他写的理由；没采纳推荐时理由必填（见 routing.Choose）。
	ChosenBy     string
	ChoiceReason string

	// Start 不为 nil：节点还没在 Graph 开工（ready / rework）。派工器先起会话、核过它是 agent，
	// 才调 Start 经 projectctl 开工，拿账本给的 attempt（n044，n043 复审新问题 2）。这时 Attempt 必须留 0。
	// 这样 launch 失败时 Graph 一点没动，不会留下 running 却没有会话的 attempt。
	// 为 nil：节点已经 running（补派），Attempt 是账本已有的号。
	//
	// Start 返回的错误若没有用 NotStarted 标明「确定没开工」，派工器一律当作结果不明、可能已经开工（n044 attempt 3）。
	Start func(ctx context.Context) (Started, error)
	// MessageFor 不为 nil 时，任务文本按最终的 attempt 生成、代替 Message：开工派单要等 start 之后才知道号，
	// 而执行 brief 第一行就写着第几次 attempt（n044）。
	MessageFor func(attempt int) string
}

// Started 是 Graph 开工的回执：账本给的 attempt，与开工事件号（绑定拿它当账本身份；对账确认的开工没有事件号）。
type Started struct {
	Attempt int
	EventID string
}

// notStarted 标明一次开工确定没写进账本。
type notStarted struct{ err error }

func (e *notStarted) Error() string { return e.err.Error() }
func (e *notStarted) Unwrap() error { return e.err }

// NotStarted 包一个开工错误，说明账本确定没开工：刚起的会话停掉就行，节点状态不变。
func NotStarted(err error) error { return &notStarted{err} }

// IsNotStarted 判断一个开工错误是不是标了「确定没开工」。
func IsNotStarted(err error) bool {
	var ns *notStarted
	return errors.As(err, &ns)
}

// StartUncertainError 是开工结果不明：控制器可能已经把开工写进账本，只是回执没到（n044 attempt 2 评审 blocker 2）。
// 派工器照样停掉没拿到任务的会话，但不说「没开工」，而是请 owner 刷新核对。
type StartUncertainError struct{ Cause error }

func (e *StartUncertainError) Error() string {
	return fmt.Sprintf("Graph 开工结果不明（%v），可能已经开工：按 r 刷新，节点若是 running，选中它按 d 补派同一个 attempt", e.Cause)
}
func (e *StartUncertainError) Unwrap() error { return e.Cause }

// IsStartUncertain 判断一个派工错误是不是开工结果不明。
func IsStartUncertain(err error) bool {
	var u *StartUncertainError
	return errors.As(err, &u)
}

// messageFor 是 attempt 号为 a 时要送出去的任务文本。
func (r Request) messageFor(a int) string {
	if r.MessageFor != nil {
		return r.MessageFor(a)
	}
	return r.Message
}

// Outcome 是一次派工尝试的结果，无论成没成都记一条。
type Outcome struct {
	At            time.Time `json:"at"`
	NodeID        string    `json:"node_id"`
	Attempt       int       `json:"attempt"`
	RequiredClass string    `json:"required_route_class"`
	ModelID       string    `json:"model_id"`
	ModelClass    string    `json:"model_route_class,omitempty"`
	Tool          string    `json:"tool,omitempty"`
	Profile       string    `json:"profile,omitempty"`
	SessionID     string    `json:"session_id,omitempty"`
	DispatchedBy  string    `json:"dispatched_by"`
	OK            bool      `json:"ok"`
	// Delivery 是任务文本的送达情况：confirmed 或 unconfirmed（n042）。
	Delivery string `json:"delivery,omitempty"`
	// 被拒绝时写明原因。拒绝也要留痕，否则事后看不出守没守规矩。
	Refusal string `json:"refusal,omitempty"`
	// RoutingAt 是这次派单那条路由审计记录的时间（n044）：要开工的派单写路由记录时 attempt 还没分配，
	// 两份记录靠它对上。
	RoutingAt time.Time `json:"routing_at,omitempty"`
	// SessionStopped 为真：会话起来了却没拿到任务说明，派工器已把它停掉（n044）。
	SessionStopped bool `json:"session_stopped,omitempty"`
	// ReplacedSession 是挪进留档的旧绑定会话号（n044 attempt 3）：账本刚分配的 attempt 在绑定存储里已有一条，
	// 那只能是别的账本历史（例如夹具重建前）留下的。
	ReplacedSession string `json:"replaced_session,omitempty"`
}

// Launcher 是派工器用到的那一点点上游能力。
//
// 收窄成接口有两个好处：拒绝逻辑可以脱离真实上游来测，
// 而且一眼看得出派工器只会启动会话，不会去动上游的别的东西。
type Launcher interface {
	Launch(ctx context.Context, req agentdeck.LaunchRequest) (agentdeck.LaunchResult, error)
	// SendMessage 把任务文本送进一个已经在跑的会话。
	//
	// 派评审要用它：会话号得等 Launch 返回才存在，而「这个会话是不是执行者
	// 正在用的那个」必须拿真实会话号来判。所以 brief 不能随 Launch 出去，
	// 得等判完了再送。
	SendMessage(ctx context.Context, sessionID, message string) error
	// Abandon 停掉本次刚起、却没拿到任务说明的会话（n044）。派工器只对自己刚起的会话调它，
	// 不对执行者的会话调；有人连着时实现方不停。
	Abandon(ctx context.Context, sessionID string) error
}

// Dispatcher 把一个节点派成一个真实会话。
type Dispatcher struct {
	Deck  Launcher
	Store *binding.Store
	Table *RouteTable
	// 派工流水的落盘路径，每次一行 JSON。
	LogPath string

	// 能力路由（n039）。每次派单前按这些算出全部因子，写进 RoutingLog；写不进去就不派。
	//
	// NodeCaps 是节点 → 所需能力，来自配置；没声明的节点没有能力要求，记录里照实写。
	NodeCaps map[string][]string
	// Quota 是 Work Deck 自己的额度观测；为 nil 时每个候选的额度都是 unknown。
	Quota routing.QuotaSource
	// RoutingLog 是路由审计记录的落盘路径，每次一行 JSON。必填。
	RoutingLog string
	// 取当前时间，测试里可以换掉。
	Now func() time.Time

	mu sync.Mutex
}

func (d *Dispatcher) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Dispatch 派一次工。
//
// 任何一步不对就拒绝，并把拒绝理由记进流水。宁可不派，也不派一个说不清楚的。
func (d *Dispatcher) Dispatch(ctx context.Context, req Request) (binding.Binding, error) {
	out := Outcome{
		At:            d.now(),
		NodeID:        req.NodeID,
		Attempt:       req.Attempt,
		RequiredClass: string(req.RequiredClass),
		ModelID:       req.ModelID,
		DispatchedBy:  req.DispatchedBy,
	}
	refuse := func(err error) (binding.Binding, error) {
		out.OK = false
		out.Refusal = err.Error()
		_ = d.appendLog(out)
		return binding.Binding{}, err
	}

	switch {
	case req.NodeID == "":
		return refuse(fmt.Errorf("派工缺少节点号"))
	case req.Start == nil && req.Attempt <= 0:
		return refuse(fmt.Errorf("派工的 attempt 必须是正数，拿到的是 %d", req.Attempt))
	case req.Start != nil && req.Attempt != 0:
		return refuse(fmt.Errorf("要开工的派工不许预填 attempt（拿到 %d）：attempt 由 projectctl start 在会话起来之后给", req.Attempt))
	case req.ModelID == "":
		return refuse(fmt.Errorf("派工缺少模型 ID：模型未知时拒绝派工，不猜默认值"))
	case req.ProjectPath == "":
		return refuse(fmt.Errorf("派工缺少项目路径"))
	}

	if !req.RequiredClass.Known() {
		return refuse(fmt.Errorf("节点的 route class %q 不认识：拒绝派工，不猜", req.RequiredClass))
	}

	model, err := d.Table.Lookup(req.ModelID)
	if err != nil {
		return refuse(err)
	}
	out.ModelClass = string(model.Class)
	out.Tool = ToolOf(model)

	if err := CheckNoDowngrade(model, req.RequiredClass); err != nil {
		return refuse(err)
	}
	// 不是 agent 的工具不接任务说明（n043 复审新问题 1）：launch 之前按配置核一次，launch 之后按回执再核一次。
	if err := agentdeck.CheckTakesTaskText(ToolOf(model), model.DeclaredAgent); err != nil {
		return refuse(err)
	}

	// 同一次 attempt 已经派过就不再派。重复派工会让一个 attempt 对上两个会话，
	// 之后没人说得清哪个才算数。要开工的派单此刻还没有 attempt，等开工拿到号再查。
	if req.Start == nil {
		if existing, ok := d.Store.Get(req.NodeID, req.Attempt); ok {
			return refuse(fmt.Errorf("%s 的 attempt %d 已经绑在会话 %s 上，不重复派工",
				req.NodeID, req.Attempt, existing.SessionID))
		}
	}

	// 任务文本放在模型与档位检查之后查：那几条拒绝理由更根本，不该被它盖住。
	if strings.TrimSpace(req.messageFor(req.Attempt)) == "" {
		return refuse(fmt.Errorf("派工缺少任务文本：说不清要执行者做什么，派出去的就只是一个空会话，不派"))
	}

	routedAt, err := d.routeAndRecord(req.NodeID, req.Attempt, "executor", req.RequiredClass, req.ModelID, req.DispatchedBy, req.ChosenBy, req.ChoiceReason)
	if err != nil {
		return refuse(err)
	}
	out.RoutingAt = routedAt

	lm, na := launchModel(model)
	res, err := d.Deck.Launch(ctx, agentdeck.LaunchRequest{
		ProjectPath:        req.ProjectPath,
		Title:              req.Title,
		Group:              req.Group,
		Tool:               ToolOf(model),
		Model:              lm,
		ModelNotApplicable: na,
		NoWait:             true,
	})
	if err != nil {
		if req.Start != nil {
			return refuse(fmt.Errorf("upstream agent-deck 启动会话失败：%w。Graph 没有开工，节点状态不变", err))
		}
		return refuse(fmt.Errorf("upstream agent-deck 启动会话失败：%w", err))
	}
	out.SessionID = res.SessionID
	out.Profile = res.Profile
	abandon := func(cause error) (binding.Binding, error) {
		return refuse(d.abandon(ctx, &out, res.SessionID, cause))
	}

	// 上游把命令起成了 shell（比如配置写的是 bash），任务说明会被当命令执行：停掉，Graph 不开工。
	if err := agentdeck.CheckLaunchedTakesTaskText(res); err != nil {
		if req.Start != nil {
			err = fmt.Errorf("%w。Graph 没有开工，节点状态不变", err)
		}
		return abandon(err)
	}

	// 会话起来了、是 agent，这才开工（n044）：开工失败就停掉这个空会话，Graph 那边本来就没动。
	attempt, startEvent := req.Attempt, ""
	if req.Start != nil {
		st, err := req.Start(ctx)
		switch {
		case IsNotStarted(err):
			return abandon(fmt.Errorf("Graph 开工失败（确定没开工，节点状态不变）：%w", err))
		case err != nil:
			return abandon(&StartUncertainError{Cause: err})
		case st.Attempt <= 0:
			return abandon(&StartUncertainError{Cause: fmt.Errorf("开工回执的 attempt 是 %d", st.Attempt)})
		}
		attempt, startEvent = st.Attempt, st.EventID
		out.Attempt = attempt
		// 账本刚为这个 attempt 开了工，绑定存储里却已有一条：它属于别的账本历史（例如夹具重建前）。同一本账里一个号只分配一次
		// （节点已 running 时 start 会失败），而写绑定的进程至多一个（写入权记账打开时取排他文件锁，拿不到锁的进程派不了工）。
		// 旧会话可能还在上游跑，所以不覆盖、不删：挪进留档、点名告诉 owner（n044 attempt 2 评审 blocker 1）。
		// 挪在送任务之前：之后送任务失败时这个键是空的，节点 running、没有绑定，d 补派走得通。
		if old, moved, err := d.Store.Supersede(req.NodeID, attempt, res.SessionID,
			fmt.Sprintf("账本刚为 attempt %d 开工（%s），这条绑定属于别的账本历史", attempt, startEvent), d.now()); err != nil {
			return abandon(fmt.Errorf("Graph 已开工 attempt %d，绑定存储里有一条旧绑定，挪不出去（%v）：新会话不绑。"+
				"节点现在是 running：修好绑定存储后按 d 补派", attempt, err))
		} else if moved {
			out.ReplacedSession = old.SessionID
		}
	}

	// 任务文本不随 launch 出去（LaunchRequest 没有这个字段）：launch 返回说明写入权已经登记，
	// 这时才经受仲裁的投递口送。
	// 「送达未确认」（n042）：正文可能到了画面、上游没确认提交。会话可能已经在跑，丢掉它就成了没人认领的会话，
	// 所以照样落绑定，记成 unconfirmed，由界面提示 owner 去看。
	// 其余投递失败：会话没拿到任务，停掉它，不落绑定（n044；此前留着一个没人认领的半成品会话）。
	delivery := binding.DeliveryConfirmed
	if err := d.Deck.SendMessage(ctx, res.SessionID, req.messageFor(attempt)); agentdeck.IsDeliveryUnconfirmed(err) {
		delivery = binding.DeliveryUnconfirmed
	} else if err != nil {
		cause := fmt.Errorf("任务文本没送进会话 %s：%w", res.SessionID, err)
		if req.Start != nil {
			cause = fmt.Errorf("Graph 已开工 attempt %d，但%w。节点现在是 running、没有绑定会话：修好后选中它按 d 补派同一个 attempt", attempt, cause)
		}
		return abandon(cause)
	}

	b := binding.Binding{
		NodeID:       req.NodeID,
		Attempt:      attempt,
		SessionID:    res.SessionID,
		Tool:         ToolOf(model),
		Model:        model.ID,
		RouteClass:   string(model.Class),
		ProjectPath:  req.ProjectPath,
		Profile:      res.Profile,
		DispatchedAt: d.now(),
		DispatchedBy: req.DispatchedBy,
		Delivery:     delivery,
		StartEvent:   startEvent,
		Supersedes:   out.ReplacedSession,
	}
	if err := d.Store.Put(b); err != nil {
		// 会话已经起来了、任务也送了，但绑定没存住。它可能已经在干活，不能停；这种半成品必须喊出来，
		// 否则界面上会出现一个没人认领的会话。
		return refuse(fmt.Errorf("会话 %s 已启动、任务文本已送，但绑定没存住：%w。请手工登记或停掉它", res.SessionID, err))
	}

	out.OK = true
	out.Delivery = delivery
	_ = d.appendLog(out)
	return b, nil
}

// abandonTimeout 是停一个会话最多等多久（探测有没有人连着 + 上游 session stop）。
const abandonTimeout = 30 * time.Second

// abandon 停掉一个刚起、没拿到任务说明的会话，把结果并进拒绝理由（n044）。
// 停不掉（有人连着、探测失败、上游报错）就照实说，请人手工处理。
//
// 停会话另起一个 context：调用方的 context 可能正是因为超时才让 start 失败，拿它去停，
// 探测和 session stop 一步都跑不了（n044 attempt 1 评审 blocker 2）。
func (d *Dispatcher) abandon(ctx context.Context, out *Outcome, sessionID string, cause error) error {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), abandonTimeout)
	defer cancel()
	if err := d.Deck.Abandon(sctx, sessionID); err != nil {
		return fmt.Errorf("%w。刚起的会话 %s 没拿到任务说明，也没停掉（%v）：请手工确认它的去向", cause, sessionID, err)
	}
	out.SessionStopped = true
	return fmt.Errorf("%w。刚起的会话 %s 没拿到任务说明，已停掉", cause, sessionID)
}

// appendLog 往派工流水里追加一行。
func (d *Dispatcher) appendLog(o Outcome) error {
	if d.LogPath == "" {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(d.LogPath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(d.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(o)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

// Candidates 列出能满足某个档位要求的模型。
//
// 界面拿它给人挑。排序把刚好够用的放前面，免得一上来就用最贵的。
func (d *Dispatcher) Candidates(required RouteClass) []Model {
	return d.Table.CandidatesFor(required)
}

// Route 算出一次派单的全部因子与推荐，不落盘。界面用它把因子摆给 owner 看。
func (d *Dispatcher) Route(nodeID string, attempt int, purpose string, required RouteClass) routing.Decision {
	caps, source := d.NodeCaps[nodeID], "配置 node_capabilities 为该节点声明的能力要求"
	if _, ok := d.NodeCaps[nodeID]; !ok {
		source = "配置 node_capabilities 没有为该节点声明能力要求：本次不按能力筛选"
	}
	var cands []routing.Candidate
	for _, m := range d.Table.All() {
		cands = append(cands, routing.Candidate{ID: m.ID, Tool: m.Tool, Class: string(m.Class), Caps: m.Caps})
	}
	return routing.Decide(routing.Request{
		NodeID: nodeID, Attempt: attempt, Purpose: purpose, RequiredClass: string(required),
		RequiredCaps: caps, RequiredCapsSource: source,
	}, cands, d.Quota, d.now())
}

// routeAndRecord 在真正起会话之前算路由、核 owner 选的模型是否可选、写审计记录。
// 任何一步不成就返回错误，调用方据此拒绝派单：说不清为什么派给它，就不派。
// attempt 为 0 表示要开工的派单，号还没分配（n044）：记录里标 attempt_pending，靠返回的时间和派工流水对上。
func (d *Dispatcher) routeAndRecord(nodeID string, attempt int, purpose string, required RouteClass, modelID, by, chosenBy, reason string) (time.Time, error) {
	if d.RoutingLog == "" {
		return time.Time{}, fmt.Errorf("没有配置路由审计记录，不派单")
	}
	dec := d.Route(nodeID, attempt, purpose, required)
	dec.AttemptPending = attempt == 0
	// 选择者由选择入口提供（界面填 owner），by 是派工记账身份，两者分开记。
	dec.DispatchedBy = by
	if err := dec.Choose(modelID, chosenBy, reason); err != nil {
		return time.Time{}, fmt.Errorf("路由拒绝：%w", err)
	}
	if err := routing.Append(d.RoutingLog, dec); err != nil {
		return time.Time{}, fmt.Errorf("路由审计记录写不进去，不派单：%w", err)
	}
	return dec.At, nil
}
