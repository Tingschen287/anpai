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

	// 要求独立评审时，评审 brief 不许随 launch 一起投递。
	//
	// 为什么：会话号要等 launch 返回才知道，所以「评审有没有落进执行者的会话」
	// 这一判断天然在 launch 之后。如果 brief 随 launch 一起送出去，那么万一真撞上，
	// 任务已经进了执行者的会话，后面再报错也撤不回来——独立评审当场就被破坏了。
	// 把 brief 从 launch 里摘掉，撞会话的后果就只剩一个空会话，是可以收拾的。
	//
	// 代价是 brief 得另找路子送。本包的白名单里没有「向已有会话发消息」这一条，
	// 而为这件事扩白名单等于放开 `session` 底下一整片操作面（stop/remove/…），
	// 那个代价比这里的残余风险大。所以这里拒绝，不偷偷把 brief 塞进去。
	if req.IndependentRequired && req.Message != "" {
		return refuse(fmt.Errorf(
			"节点要求独立评审，评审 brief 不能随会话启动一起投递：" +
				"会话号要等启动之后才知道，brief 先送出去就意味着万一落到执行者会话里也收不回来。" +
				"请先派出空会话，brief 另行送达"))
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

	// 要求独立评审时 req.Message 必为空（上面那条守着），所以这里启动的是一个
	// 还没收到任务的空会话；下面核验过会话号之后，它才算数。
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
	// 这是独立性的最后一道：前面比的是「有没有执行会话」，这里比的是
	// 上游真的给回来的会话号。真撞上说明上游出了怪事，宁可报错。
	//
	// 这一步只能在 launch 之后做，会话号早于此不存在。能做的是让撞上的后果
	// 尽量轻：要求独立评审时 brief 不随 launch 投递（见上面那条守卫），
	// 所以此刻撞上也只是多了一个还没收到任务的空会话，绑定不会落，
	// 评审也不会真的在执行者的会话里跑起来。
	//
	// 本包的白名单不含 stop，那个空会话收不回来——拒绝信息里把会话号给全，
	// 让人能手工处理。
	if executorSession != "" && res.SessionID == executorSession {
		return refuse(fmt.Errorf(
			"拒绝：上游把评审派进了执行者正在用的会话 %s。"+
				"该会话是空的（评审 brief 没有随启动投递），但 Work Deck 不会去停它，"+
				"请手工确认这个会话的去向",
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
