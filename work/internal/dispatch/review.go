package dispatch

import (
	"context"
	"fmt"
	"strings"

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
	// ChosenBy 是挑模型的人，ChoiceReason 是他写的理由；没采纳推荐时理由必填（见 routing.Choose）。
	ChosenBy     string
	ChoiceReason string
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
	// 只有空白也算没有 brief（n039 attempt 4 评审）：和执行者入口同一个判法。
	case strings.TrimSpace(req.Message) == "":
		return refuse(fmt.Errorf(
			"派评审缺少 brief：说不清要评审者做什么，派出去的就只是一个空会话。" +
				"上一版正是这样——界面从不设置 Message，评审会话起来了却从没收到任务"))
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
	// 不是 agent 的工具当不了评审（n043 复审新问题 1）：它会把 brief 当命令执行。
	if err := agentdeck.CheckTakesTaskText(ToolOf(model)); err != nil {
		return refuse(err)
	}

	key := ReviewerKey(req.NodeID)
	if existing, ok := d.Store.Get(key, req.Attempt); ok {
		return refuse(fmt.Errorf("%s 的 attempt %d 已经派过评审，绑在会话 %s 上，不重复派",
			req.NodeID, req.Attempt, existing.SessionID))
	}

	// 起会话之前先算路由并写审计记录：说不清为什么派给这个模型，就不派。
	routedAt, err := d.routeAndRecord(req.NodeID, req.Attempt, "reviewer", req.RequiredClass, req.ModelID, req.DispatchedBy, req.ChosenBy, req.ChoiceReason)
	if err != nil {
		return refuse(err)
	}
	out.RoutingAt = routedAt

	// 第一步：起一个空会话。brief **不**随它出去。
	//
	// 会话号要等这一步返回才存在，而「这个会话是不是执行者正在用的那个」
	// 必须拿真实会话号来判。brief 若随 launch 一起送，万一真撞上，
	// 评审任务就已经进了执行者的会话，撤不回来——独立评审当场被破坏。
	// 所以先起空的，判完再送。
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
		return refuse(fmt.Errorf("upstream agent-deck 启动评审会话失败：%w", err))
	}
	out.SessionID = res.SessionID
	out.Profile = res.Profile

	// 评审会话不能和执行会话是同一个。
	//
	// 这是独立性的最后一道：前面比的是「有没有执行会话」，这里比的是
	// 上游真的给回来的会话号。真撞上说明上游出了怪事，宁可报错。
	//
	// 第二步：核验会话号。这一步只能在 launch 之后做，会话号早于此不存在。
	//
	// 此刻那个会话还是空的，brief 没送。所以撞上的后果只是多了一个没收到任务的
	// 会话，绑定不会落，评审也不会真的在执行者的会话里跑起来。
	// 撞上的是执行者正在用的会话，派工器不停它（停了等于替执行者动手）——拒绝信息里把会话号给全，
	// 让人能手工处理。
	if executorSession != "" && res.SessionID == executorSession {
		return refuse(fmt.Errorf(
			"拒绝：上游把评审派进了执行者正在用的会话 %s。"+
				"该会话是空的（brief 还没投递），但 Work Deck 不会去停它，"+
				"请手工确认这个会话的去向",
			res.SessionID))
	}

	// 上游把命令起成了 shell：它不是 agent，会把 brief 当命令执行。这是评审自己刚起的会话，停掉（n044）。
	if err := agentdeck.CheckLaunchedTakesTaskText(res); err != nil {
		return refuse(d.abandon(ctx, &out, res.SessionID, err))
	}

	// 第三步：会话号核验过了，这才把 brief 送进去。
	//
	// 送不进去就不落绑定：绑定意味着「这次 attempt 的评审已经派出」，
	// 而一个没收到任务的会话不算派出。那个会话是评审自己刚起的空会话，停掉它（n044），
	// 不要账本上多一条其实什么都没发生的「已派评审」，也不留一个没人认领的会话。
	// 「送达未确认」照样落绑定并记 unconfirmed（n042，理由见 Dispatch）：n040 实测评审会话其实收到并执行了 brief。
	delivery := binding.DeliveryConfirmed
	if err := d.Deck.SendMessage(ctx, res.SessionID, req.Message); agentdeck.IsDeliveryUnconfirmed(err) {
		delivery = binding.DeliveryUnconfirmed
	} else if err != nil {
		return refuse(d.abandon(ctx, &out, res.SessionID,
			fmt.Errorf("评审 brief 没送进会话 %s：%w。这次派评审不记，可以重派", res.SessionID, err)))
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
		Delivery:     delivery,
	}
	if err := d.Store.Put(b); err != nil {
		return refuse(fmt.Errorf("评审会话 %s 已启动但绑定没存住：%w", res.SessionID, err))
	}

	out.OK = true
	out.Delivery = delivery
	_ = d.appendLog(out)
	return b, nil
}
