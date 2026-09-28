package dispatch

import (
	"github.com/Tingschen287/terminal-deck/work/internal/routing"

	"context"
	"encoding/json"
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
	// 被拒绝时写明原因。拒绝也要留痕，否则事后看不出守没守规矩。
	Refusal string `json:"refusal,omitempty"`
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
	case req.Attempt <= 0:
		return refuse(fmt.Errorf("派工的 attempt 必须是正数，拿到的是 %d", req.Attempt))
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

	// 同一次 attempt 已经派过就不再派。重复派工会让一个 attempt 对上两个会话，
	// 之后没人说得清哪个才算数。
	if existing, ok := d.Store.Get(req.NodeID, req.Attempt); ok {
		return refuse(fmt.Errorf("%s 的 attempt %d 已经绑在会话 %s 上，不重复派工",
			req.NodeID, req.Attempt, existing.SessionID))
	}

	// 任务文本放在模型与档位检查之后查：那几条拒绝理由更根本，不该被它盖住。
	if strings.TrimSpace(req.Message) == "" {
		return refuse(fmt.Errorf("派工缺少任务文本：说不清要执行者做什么，派出去的就只是一个空会话，不派"))
	}

	if err := d.routeAndRecord(req.NodeID, req.Attempt, "executor", req.RequiredClass, req.ModelID, req.DispatchedBy, req.ChosenBy, req.ChoiceReason); err != nil {
		return refuse(err)
	}

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
		return refuse(fmt.Errorf("upstream agent-deck 启动会话失败：%w", err))
	}

	// 任务文本不随 launch 出去（LaunchRequest 没有这个字段）：launch 返回说明写入权已经登记，
	// 这时才经受仲裁的投递口送。送不进去就不落绑定，和派评审同一个规矩。
	if err := d.Deck.SendMessage(ctx, res.SessionID, req.Message); err != nil {
		return refuse(fmt.Errorf(
			"会话 %s 已经起来了，但任务文本没送进去：%w。"+
				"这是个半成品会话，Work Deck 不会去停它，也不记这次派工——请手工确认该会话的去向，再重派",
			res.SessionID, err))
	}

	b := binding.Binding{
		NodeID:       req.NodeID,
		Attempt:      req.Attempt,
		SessionID:    res.SessionID,
		Tool:         ToolOf(model),
		Model:        model.ID,
		RouteClass:   string(model.Class),
		ProjectPath:  req.ProjectPath,
		Profile:      res.Profile,
		DispatchedAt: d.now(),
		DispatchedBy: req.DispatchedBy,
	}
	if err := d.Store.Put(b); err != nil {
		// 会话已经起来了，但绑定没存住。这种半成品必须喊出来，
		// 否则界面上会出现一个没人认领的会话。
		return refuse(fmt.Errorf("会话 %s 已启动但绑定没存住：%w", res.SessionID, err))
	}

	out.OK = true
	out.SessionID = res.SessionID
	out.Profile = res.Profile
	_ = d.appendLog(out)
	return b, nil
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
func (d *Dispatcher) routeAndRecord(nodeID string, attempt int, purpose string, required RouteClass, modelID, by, chosenBy, reason string) error {
	if d.RoutingLog == "" {
		return fmt.Errorf("没有配置路由审计记录，不派单")
	}
	dec := d.Route(nodeID, attempt, purpose, required)
	// 选择者由选择入口提供（界面填 owner），by 是派工记账身份，两者分开记。
	dec.DispatchedBy = by
	if err := dec.Choose(modelID, chosenBy, reason); err != nil {
		return fmt.Errorf("路由拒绝：%w", err)
	}
	if err := routing.Append(d.RoutingLog, dec); err != nil {
		return fmt.Errorf("路由审计记录写不进去，不派单：%w", err)
	}
	return nil
}
