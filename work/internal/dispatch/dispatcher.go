package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
}

// Dispatcher 把一个节点派成一个真实会话。
type Dispatcher struct {
	Deck  Launcher
	Store *binding.Store
	Table *RouteTable
	// 派工流水的落盘路径，每次一行 JSON。
	LogPath string
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

	res, err := d.Deck.Launch(ctx, agentdeck.LaunchRequest{
		ProjectPath: req.ProjectPath,
		Title:       req.Title,
		Group:       req.Group,
		Tool:        ToolOf(model),
		Model:       model.ID,
		Message:     req.Message,
		NoWait:      true,
	})
	if err != nil {
		return refuse(fmt.Errorf("upstream agent-deck 启动会话失败：%w", err))
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
