package queue

import (
	"fmt"
	"strings"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/binding"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

// Bindings 让队列知道某个节点的某次 attempt 跑在哪个会话里。
type Bindings interface {
	Get(nodeID string, attempt int) (binding.Binding, bool)
}

// FromGraph 从 Graph 快照里挑出停在 owner 手上的节点。
//
// 只挑两种：kind 为 human_gate 且在 ready/running 的门，和 blocked 的节点。
// 状态取不到的节点不猜，不进队列（界面的问题栏会单独报它取不到）。
// script 是 Graph 控制器脚本的路径，用来拼给 owner 执行的命令。
func FromGraph(snap projectctl.Snapshot, b Bindings, script string, now time.Time) []Item {
	var out []Item
	for _, n := range snap.Nodes {
		st, ok := n.State.Get()
		if !ok {
			continue
		}
		var cat Category
		switch {
		case n.Kind == "human_gate" && (st == projectctl.StateReady || st == projectctl.StateRunning):
			cat = CatGraphHumanGate
		case st == projectctl.StateBlocked:
			cat = CatGraphBlocked
		default:
			continue
		}
		attempt, _ := n.Attempt.Get()
		it := Item{
			ID:       fmt.Sprintf("graph:%s#%d:%s", n.ID, attempt, st),
			Category: cat,
			Risk:     normalizeRisk(n.Risk),
			Title:    graphTitle(cat, n),
			Detail:   graphDetail(n),
			Source: Source{
				Kind: SourceGraph, NodeID: n.ID, Attempt: attempt, State: string(st), Present: true,
			},
			EvidenceRefs:     graphEvidence(snap, n),
			BlockingSessions: []string{},
			FirstSeen:        now,
			LastSeen:         now,
			Status:           StatusOpen,
		}
		if b != nil && attempt > 0 {
			if bd, ok := b.Get(n.ID, attempt); ok && bd.SessionID != "" {
				it.BlockingSessions = append(it.BlockingSessions, bd.SessionID)
			}
		}
		ctl := "python3 " + script
		switch cat {
		case CatGraphHumanGate:
			it.Options = []Option{
				{Key: "take", Label: "由 owner 接手这道门（human 档开工，之后按 brief 提交与评审）",
					Command: fmt.Sprintf("%s start %s --actor owner --route-class human --reason \"<填写理由>\"", ctl, n.ID)},
				{Key: "later", Label: "暂不处理，只记下已看过"},
			}
		case CatGraphBlocked:
			it.Options = []Option{
				{Key: "resume", Label: "放行（resume）",
					Command: fmt.Sprintf("%s resume %s --actor owner --reason \"<填写理由>\"", ctl, n.ID)},
				{Key: "fail", Label: "放弃（fail）",
					Command: fmt.Sprintf("%s fail %s --actor owner --reason \"<填写理由>\"", ctl, n.ID)},
				{Key: "later", Label: "暂不处理，只记下已看过"},
			}
		}
		it.Recommendation = Recommendation{
			Option: "", Confidence: ConfidenceNone,
			Basis: "Graph 的门由 owner 裁决，Work Deck 不替人推荐",
		}
		out = append(out, it)
	}
	return out
}

func graphTitle(cat Category, n projectctl.Node) string {
	name := n.ID
	if n.Title != "" {
		name = n.ID + " " + n.Title
	}
	if cat == CatGraphHumanGate {
		return "Graph 门等你：" + name
	}
	return "Graph 节点被阻塞：" + name
}

func graphDetail(n projectctl.Node) string {
	switch {
	case n.RequiredAction != "":
		return n.RequiredAction
	case n.Reason != "":
		return n.Reason
	}
	return "状态投影里没有给出原因，按 Enter 看节点详情或跑 projectctl brief"
}

func graphEvidence(snap projectctl.Snapshot, n projectctl.Node) []string {
	refs := []string{"work-graph.yaml#" + n.ID}
	if n.LatestEvidence != "" {
		refs = append(refs, n.LatestEvidence)
	}
	return refs
}

func normalizeRisk(r string) string {
	for _, k := range AllRisks {
		if r == k {
			return r
		}
	}
	// Graph 的 risk 取值不在四档里时按 high 处理：宁可高估，不把一个门排到最后。
	return RiskHigh
}

// FromInbox 把 inbox export 的记录变成条目。
//
// 同一批里指纹相同的只留最后一条（时间最新）。四类记录进队列：
// waiting、error、卡在额度上、finished。别的翻转（例如 running→idle 且不是额度问题）
// 不需要人拍板，不进队列。
func FromInbox(records []agentdeck.InboxRecord, now time.Time) []Item {
	byID := map[string]Item{}
	var order []string
	for _, r := range records {
		cat, ok := categorize(r)
		if !ok {
			continue
		}
		id := "session:" + r.Fingerprint()[:24]
		it := Item{
			ID:               id,
			Category:         cat,
			Risk:             sessionRisk(cat, r),
			Title:            sessionTitle(cat, r),
			Detail:           sessionDetail(r),
			Options:          sessionOptions(cat),
			Recommendation:   sessionRecommendation(cat, r),
			EvidenceRefs:     sessionEvidence(r),
			BlockingSessions: []string{r.ChildSessionID},
			Source: Source{
				Kind: SourceSession, SessionID: r.ChildSessionID, Profile: r.Profile, Present: true,
				DoneStatus: strings.ToLower(strings.TrimSpace(r.DoneStatus)),
			},
			FirstSeen: now,
			LastSeen:  now,
			Status:    StatusOpen,
		}
		if _, seen := byID[id]; !seen {
			order = append(order, id)
		}
		byID[id] = it
	}
	out := make([]Item, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

func categorize(r agentdeck.InboxRecord) (Category, bool) {
	if r.Kind == "finished" {
		return CatSessionFinished, true
	}
	if r.Kind != "" {
		return "", false
	}
	if r.Substate == "usage-limit" {
		return CatSessionUsageLimit, true
	}
	switch r.ToStatus {
	case "waiting":
		return CatSessionWaiting, true
	case "error":
		return CatSessionError, true
	}
	return "", false
}

func sessionRisk(cat Category, r agentdeck.InboxRecord) string {
	switch cat {
	case CatSessionError:
		return RiskHigh
	case CatSessionWaiting, CatSessionUsageLimit:
		return RiskMedium
	case CatSessionFinished:
		if strings.EqualFold(r.DoneStatus, "fail") {
			return RiskMedium
		}
	}
	return RiskLow
}

func sessionTitle(cat Category, r agentdeck.InboxRecord) string {
	who := r.ChildTitle
	if who == "" {
		who = r.ChildSessionID
	}
	switch cat {
	case CatSessionWaiting:
		return "会话在等你输入：" + who
	case CatSessionError:
		return "会话出错：" + who
	case CatSessionUsageLimit:
		return "会话卡在额度上：" + who
	}
	return "会话报告完成：" + who
}

func sessionDetail(r agentdeck.InboxRecord) string {
	var parts []string
	if r.Kind == "finished" {
		parts = append(parts, "完成状态 "+orUnknown(r.DoneStatus))
		if r.DoneSummary != "" {
			parts = append(parts, "摘要："+firstLine(r.DoneSummary, 200))
		}
	} else {
		parts = append(parts, fmt.Sprintf("%s → %s", orUnknown(r.FromStatus), orUnknown(r.ToStatus)))
		if r.Substate != "" {
			parts = append(parts, "细分 "+r.Substate)
		}
	}
	if r.Profile != "" {
		parts = append(parts, "profile "+r.Profile)
	}
	if !r.Timestamp.IsZero() {
		parts = append(parts, "上报于 "+r.Timestamp.Local().Format("01-02 15:04"))
	}
	return strings.Join(parts, "；")
}

func sessionOptions(cat Category) []Option {
	ack := Option{Key: "ack", Label: "已处理，不给会话发消息"}
	switch cat {
	case CatSessionWaiting:
		return []Option{
			{Key: "continue", Label: "让它继续（发送「继续」）", Message: "继续"},
			ack,
		}
	case CatSessionFinished:
		return []Option{
			ack,
			{Key: "continue", Label: "让它接着做（发送「继续」）", Message: "继续"},
		}
	}
	return []Option{ack}
}

func sessionRecommendation(cat Category, r agentdeck.InboxRecord) Recommendation {
	switch cat {
	case CatSessionWaiting:
		return Recommendation{Option: "continue", Confidence: ConfidenceLow,
			Basis: "规则：Work Deck 看不到它具体在问什么，先按 p 预览再决定；「继续」只是最常见的回答"}
	case CatSessionError:
		return Recommendation{Option: "ack", Confidence: ConfidenceHigh,
			Basis: "规则：出错多半是凭据、模型或环境问题，回一句话解决不了，要人去处理"}
	case CatSessionUsageLimit:
		return Recommendation{Option: "ack", Confidence: ConfidenceHigh,
			Basis: "规则：额度卡住时回话没用，等额度恢复或换档"}
	}
	if strings.EqualFold(r.DoneStatus, "fail") {
		return Recommendation{Option: "ack", Confidence: ConfidenceMedium,
			Basis: "规则：报告失败的完成要先看摘要，不宜直接让它继续"}
	}
	return Recommendation{Option: "ack", Confidence: ConfidenceMedium,
		Basis: "规则：完成回报看过结果即可"}
}

func sessionEvidence(r agentdeck.InboxRecord) []string {
	refs := []string{"agent-deck session:" + r.ChildSessionID}
	if r.TurnFingerprint != "" {
		refs = append(refs, "agent-deck inbox turn:"+r.TurnFingerprint)
	}
	return refs
}

func orUnknown(s string) string {
	if s == "" {
		return "（空）"
	}
	return s
}

func firstLine(s string, max int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
