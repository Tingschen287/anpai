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

	// ExecutorActor 是 Graph 账本上记的执行者身份。
	//
	// 它是给人看的、写进拒绝信息和流水的标识，**不是**独立性的判据。
	// 判据见 DispatchReviewer 的注释：那件事派工器自己查，不问调用方。
	ExecutorActor string
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
//
// 独立性的判据是**上游会话号**，由派工器自己从绑定存储里查，不接受调用方
// 声明自己是谁。上一版栽在这里：界面拿不到评审者的真实身份，就拼了一个
// `"work-deck-reviewer-" + 模型名` 填进请求，它永远不可能等于执行者的标识，
// 于是这条本该最严的检查永远通过。凡是调用方能自己填的字段，都挡不住
// 调用方填错。
//
// 查不到执行者在哪个会话时，这里**拒绝派**，不是跳过检查：
// 证明不了独立，就不算独立。
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
	case req.ModelID == "":
		return refuse(fmt.Errorf("派评审缺少模型 ID：模型未知时拒绝派工，不猜默认值"))
	case req.ProjectPath == "":
		return refuse(fmt.Errorf("派评审缺少项目路径"))
	}

	// 独立性这一组放在最前面查，它比档位更根本。
	//
	// 先查身份：连执行者是谁都说不上来，谈不上独立。
	// 再查会话：这一条是权威判据，派工器自己去绑定存储里取执行者的会话号。
	executorSession := ""
	if b, ok := d.Store.Get(req.NodeID, req.Attempt); ok {
		executorSession = b.SessionID
	}
	if req.IndependentRequired {
		if req.ExecutorActor == "" {
			return refuse(fmt.Errorf(
				"节点要求独立评审，但不知道执行者是谁：无法判断独立性，拒绝派评审"))
		}
		if executorSession == "" {
			return refuse(fmt.Errorf(
				"节点要求评审独立于执行者 %s，但绑定存储里没有这个节点 attempt %d 的执行会话："+
					"证明不了评审独立，拒绝派评审。"+
					"本次执行若不是 Work Deck 派出的，请先登记执行会话，或改用 CLI 手工派评审",
				req.ExecutorActor, req.Attempt))
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

	// 评审会话不能和执行会话是同一个。
	//
	// 这是独立性的第二道，也是最终那道：前面比的是「有没有执行会话」，
	// 这里比的是上游真的给回来的会话号。真撞上说明上游出了怪事，宁可报错。
	// 注意此时评审会话已经起来了，而本包的白名单不含 stop，收不回来——
	// 拒绝信息里要把会话号给全，让人能手工处理。
	if executorSession != "" && res.SessionID == executorSession {
		return refuse(fmt.Errorf(
			"拒绝：上游把评审派进了执行者正在用的会话 %s。"+
				"该会话已被启动且 Work Deck 不会去停它，请手工确认这个会话的去向",
			res.SessionID))
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
		DispatchedBy: req.DispatchedBy,
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
