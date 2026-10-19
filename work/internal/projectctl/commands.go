package projectctl

import (
	"context"
	"encoding/json"
	"regexp"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// Validate 跑 projectctl validate，确认 graph 与证据是否自洽。
func (c *Client) Validate(ctx context.Context) (ValidateResult, error) {
	raw, err := c.run(ctx, "validate")
	if err != nil {
		return ValidateResult{}, err
	}
	var v ValidateResult
	if err := json.Unmarshal(raw, &v); err != nil {
		return ValidateResult{}, i18n.E("err.projectctl.validate.parse", err)
	}
	return v, nil
}

// ValidateResult 是 validate 的返回。
type ValidateResult struct {
	Valid         bool   `json:"valid"`
	ProjectID     string `json:"project_id"`
	GraphRevision int    `json:"graph_revision"`
	Mode          string `json:"mode"`
}

// ReadyNode 是 next 返回的一个就绪节点。
type ReadyNode struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Risk       string   `json:"risk"`
	RouteClass string   `json:"route_class"`
	OwnerRole  string   `json:"owner_role"`
	DependsOn  []string `json:"depends_on"`
}

// Next 列出依赖已满足、可以开工的节点。
func (c *Client) Next(ctx context.Context) ([]ReadyNode, error) {
	raw, err := c.run(ctx, "next")
	if err != nil {
		return nil, err
	}
	var out []ReadyNode
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, i18n.E("err.projectctl.next.parse", err)
	}
	return out, nil
}

// VerifyChain 体检账本：哈希链是否连得上、证据是否比账本还新。
//
// 这个命令在审计不通过时退出码是 2，但 stdout 仍然是合法结果，run 已经处理了。
func (c *Client) VerifyChain(ctx context.Context) (ChainStatus, error) {
	raw, err := c.run(ctx, "verify-chain")
	if err != nil {
		return ChainStatus{}, err
	}
	var v struct {
		ChainOK     bool     `json:"chain_ok"`
		SpecOK      bool     `json:"spec_ok"`
		Events      int      `json:"events"`
		ErrorCount  int      `json:"error_count"`
		Errors      []string `json:"errors"`
		Warnings    []string `json:"warnings"`
		LastEventAt *string  `json:"last_event_at"`
		Stale       bool     `json:"stale"`
		LagDays     *float64 `json:"lag_days"`
		AuditFailed bool     `json:"audit_failed"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return ChainStatus{}, i18n.E("err.projectctl.verifychain.parse", err)
	}
	out := ChainStatus{
		ChainOK:     v.ChainOK,
		SpecOK:      v.SpecOK,
		Events:      v.Events,
		ErrorCount:  v.ErrorCount,
		Errors:      v.Errors,
		Warnings:    v.Warnings,
		Stale:       v.Stale,
		AuditFailed: v.AuditFailed,
	}
	if v.LastEventAt == nil {
		out.LastEventAt = Unknown[time.Time]("账本没有事件时间")
	} else if t, err := time.Parse(time.RFC3339, *v.LastEventAt); err != nil {
		out.LastEventAt = Unknown[time.Time]("账本事件时间无法解析：" + *v.LastEventAt)
	} else {
		out.LastEventAt = Known(t)
	}
	if v.LagDays == nil {
		out.LagDays = Unknown[float64]("projectctl 未给出滞后天数")
	} else {
		out.LagDays = Known(*v.LagDays)
	}
	return out, nil
}

// briefStateRe 从 brief 的 Markdown 里取当前状态。
// brief 的 JSON 输出是一整段文本，不是结构化字段，所以只能按行取。
var briefStateRe = regexp.MustCompile("(?m)^- Current state: `([a-z]+)`\\s*$")

// BriefState 查单个节点此刻的状态。
//
// 这是唯一一个能按需查到任意节点实时状态的只读命令：状态投影里只有计数和
// 少数几张分类表，拿不到全量明细。
func (c *Client) BriefState(ctx context.Context, nodeID string) (State, error) {
	raw, err := c.run(ctx, "brief", nodeID)
	if err != nil {
		return "", err
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", i18n.E("err.projectctl.briefstate.parse", err)
	}
	m := briefStateRe.FindStringSubmatch(text)
	if m == nil {
		return "", i18n.E("err.projectctl.briefstate.nostateline", nodeID)
	}
	return State(m[1]), nil
}
