package dispatch

import (
	"context"
	"fmt"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/binding"
)

// ReviewRequest 是派一个评审者去看某次提交。
type ReviewRequest struct {
	NodeID  string
	Attempt int
	// 节点 review_policy 要求的档位。
	RequiredClass RouteClass
	// 要用的模型 ID。同派工一样必填。
	ModelID string

	// 执行者的会话标识，从 Graph 账本取，不是调用方随口给的。
	ExecutorActor string
	// 要派的评审者会话标识。
	ReviewerActor string
	// 节点是否要求评审独立于执行者。
	IndependentRequired bool

	ProjectPath  string
	Title        string
	Group        string
	Message      string
	DispatchedBy string
}

// ReviewerKey 是评审绑定在存储里的键。
//
// 评审与执行共用一个 attempt，但不是同一件事，所以键上加后缀区分，
// 免得评审会话把执行会话的绑定顶掉。
func ReviewerKey(nodeID string) string { return nodeID + "#review" }

// DispatchReviewer 派一个评审者。
//
// 比派工多守一条规矩：节点要求独立评审时，评审者不能是执行者本人。
// 这条不是提醒，是拒绝——自己评自己等于没评，而账本上会留下一条看起来
// 完全合规的验收记录，那比没有评审更糟。
func (d *Dispatcher) DispatchReviewer(ctx context.Context, req ReviewRequest) (binding.Binding, error) {
	out := Outcome{
		At:            d.now(),
		NodeID:        ReviewerKey(req.NodeID),
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
		return refuse(fmt.Errorf("派评审缺少节点号"))
	case req.Attempt <= 0:
		return refuse(fmt.Errorf("派评审的 attempt 必须是正数，拿到的是 %d", req.Attempt))
	case req.ReviewerActor == "":
		return refuse(fmt.Errorf("派评审缺少评审者标识：说不清谁在评，就没法判断独立性"))
	case req.ModelID == "":
		return refuse(fmt.Errorf("派评审缺少模型 ID：模型未知时拒绝派工，不猜默认值"))
	case req.ProjectPath == "":
		return refuse(fmt.Errorf("派评审缺少项目路径"))
	}

	// 独立性这一条放在最前面查，它比档位更根本。
	if req.IndependentRequired {
		if req.ExecutorActor == "" {
			return refuse(fmt.Errorf(
				"节点要求独立评审，但不知道执行者是谁：无法判断独立性，拒绝派评审"))
		}
		if req.ReviewerActor == req.ExecutorActor {
			return refuse(fmt.Errorf(
				"拒绝派评审：节点要求评审独立于执行者，而评审者 %s 就是执行者本人",
				req.ReviewerActor))
		}
	}

	if !req.RequiredClass.Known() {
		return refuse(fmt.Errorf("评审要求的 route class %q 不认识：拒绝派工，不猜", req.RequiredClass))
	}

	model, err := d.Table.Lookup(req.ModelID)
	if err != nil {
		return refuse(err)
	}
	out.ModelClass = string(model.Class)
	out.Tool = ToolOf(model)

	if err := CheckNoDowngrade(model, req.RequiredClass); err != nil {
		return refuse(fmt.Errorf("评审不得降档：%w", err))
	}

	key := ReviewerKey(req.NodeID)
	if existing, ok := d.Store.Get(key, req.Attempt); ok {
		return refuse(fmt.Errorf("%s 的 attempt %d 已经派过评审，绑在会话 %s 上，不重复派",
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
		return refuse(fmt.Errorf("upstream agent-deck 启动评审会话失败：%w", err))
	}

	// 评审会话不能和执行会话是同一个。真到这一步说明上游出了怪事，宁可报错。
	if execBinding, ok := d.Store.Get(req.NodeID, req.Attempt); ok {
		if execBinding.SessionID == res.SessionID {
			return refuse(fmt.Errorf(
				"拒绝：上游把评审派进了执行者正在用的会话 %s", res.SessionID))
		}
	}

	b := binding.Binding{
		NodeID:       key,
		Attempt:      req.Attempt,
		SessionID:    res.SessionID,
		Tool:         ToolOf(model),
		Model:        model.ID,
		RouteClass:   string(model.Class),
		ProjectPath:  req.ProjectPath,
		Profile:      res.Profile,
		DispatchedAt: d.now(),
		DispatchedBy: req.ReviewerActor,
	}
	if err := d.Store.Put(b); err != nil {
		return refuse(fmt.Errorf("评审会话 %s 已启动但绑定没存住：%w", res.SessionID, err))
	}

	out.OK = true
	out.SessionID = res.SessionID
	out.Profile = res.Profile
	_ = d.appendLog(out)
	return b, nil
}
