// Package routing 为一次派单挑 CLI 与模型，并把每个因子写成可审计的记录。
//
// 三条规矩：
//  1. 能力位显式声明。每个模型在配置里逐项写 yes/no，没写就是 unknown，绝不从工具名推断。
//  2. 额度只用 Work Deck 自己观测到的结果，否则标 unknown，绝不取 CLI 自报的用量回执。
//     依据是 n026 的实测（见 QuotaBasis）。
//  3. 未知不填默认值：有任何未知因子的候选不自动推荐；一个都推荐不了就升级给 owner 挑。
//     路由只在满足节点档位的模型里挑，不降档，也不碰评审门（评审另走 DispatchReviewer）。
package routing

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Capabilities 是能力位的全集。配置里出现别的名字报错。
var Capabilities = []string{"hooks", "sandbox", "resume", "structured_output", "mcp"}

// 能力位取值。
const (
	Yes     = "yes"
	No      = "no"
	Unknown = "unknown"
)

// 额度状态。
const (
	QuotaAvailable = "available"
	QuotaExhausted = "exhausted"
	QuotaUnknown   = "unknown"
)

// QuotaBasis 是「额度为什么不取 CLI 自报」的依据，写进每一条审计记录。
const QuotaBasis = "n026 实测（4.eval/results/n026_pa7_usage/report.md §2、§5、§9）：8 个样本只有 4 个给出完整用量，" +
	"缓存语义跨 CLI 差三个数量级；CLI 自报用量不可依赖。所以额度只用 Work Deck 自己观测到的会话结果，观测不到标 unknown"

// rank 与 dispatch 的档位一致。本包不引 dispatch，免得循环依赖；两边由 TestRanksMatchDispatch 对齐。
var rank = map[string]int{"economy": 0, "balanced": 1, "frontier": 2, "human": 3}

// Candidate 是一个可派的模型，连同配置里声明的能力位。
type Candidate struct {
	ID    string
	Tool  string
	Class string
	// Caps 是显式声明的能力位，值只能是 yes 或 no；没写的键就是 unknown。
	Caps map[string]string
}

// Quota 是一个模型的额度判断。
type Quota struct {
	State      string     `json:"state"`
	ObservedAt *time.Time `json:"observed_at"`
	// Source 说明判断从哪来；unknown 时说明为什么不知道。
	Source string `json:"source"`
}

// QuotaSource 给出一个模型的额度判断。
type QuotaSource interface {
	QuotaOf(modelID string, now time.Time) Quota
}

// Request 是一次派单要满足的条件。
type Request struct {
	NodeID        string
	Attempt       int
	Purpose       string // executor 或 reviewer
	RequiredClass string
	RequiredCaps  []string
	// RequiredCapsSource 说明能力要求从哪来（例如配置的 node_capabilities），没声明也要写明。
	RequiredCapsSource string
}

// Factor 是一个候选在这次派单里的全部因子。
type Factor struct {
	Model           string            `json:"model"`
	Tool            string            `json:"tool"`
	Class           string            `json:"route_class"`
	MeetsRouteClass bool              `json:"meets_route_class"`
	Capabilities    map[string]string `json:"capabilities"`
	Quota           Quota             `json:"quota"`
	// Selectable 为真：owner 可以选它（满足档位、能力没有 no、额度不是 exhausted）。
	Selectable bool `json:"selectable"`
	// Recommendable 为真：可以自动推荐它（在 Selectable 之上，所有因子都已知且满足）。
	Recommendable bool     `json:"recommendable"`
	Excluded      string   `json:"excluded,omitempty"`
	Unknowns      []string `json:"unknowns"`
}

// Decision 是一次派单的审计记录。
type Decision struct {
	At                 time.Time `json:"at"`
	NodeID             string    `json:"node_id"`
	Attempt            int       `json:"attempt"`
	Purpose            string    `json:"purpose"`
	RequiredClass      string    `json:"required_route_class"`
	RequiredCaps       []string  `json:"required_capabilities"`
	RequiredCapsSource string    `json:"required_capabilities_source"`
	Candidates         []Factor  `json:"candidates"`
	Recommended        string    `json:"recommended"`
	Reason             string    `json:"reason"`
	// Escalated 为真：没有能自动推荐的候选，交给 owner 挑。
	Escalated bool     `json:"escalated_to_owner"`
	Unknowns  []string `json:"unknowns"`
	Chosen    string   `json:"chosen"`
	// ChosenBy 是做出选择的人：Work Deck 里每一次派单都由 owner 在界面上挑定。
	ChosenBy string `json:"chosen_by"`
	// ChoiceReason 说明为什么选了 Chosen：采纳推荐、没有推荐由 owner 挑、或 owner 改选。
	// Reason 只解释推荐本身，两者分开。
	ChoiceReason string `json:"choice_reason"`
	// DispatchedBy 是派工记账里的身份（例如 work-deck），不是选择者。
	DispatchedBy string `json:"dispatched_by"`
	QuotaBasis   string `json:"quota_basis"`
}

// Decide 算出一次派单的全部因子与推荐。它不挑默认值：推荐不了就升级。
func Decide(req Request, cands []Candidate, q QuotaSource, now time.Time) Decision {
	d := Decision{
		At: now, NodeID: req.NodeID, Attempt: req.Attempt, Purpose: req.Purpose,
		RequiredClass: req.RequiredClass, RequiredCaps: append([]string{}, req.RequiredCaps...),
		RequiredCapsSource: req.RequiredCapsSource, QuotaBasis: QuotaBasis,
		Candidates: []Factor{}, Unknowns: []string{},
	}
	if d.RequiredCaps == nil {
		d.RequiredCaps = []string{}
	}
	sorted := append([]Candidate{}, cands...)
	sort.Slice(sorted, func(i, j int) bool {
		if rank[sorted[i].Class] != rank[sorted[j].Class] {
			return rank[sorted[i].Class] < rank[sorted[j].Class]
		}
		return sorted[i].ID < sorted[j].ID
	})
	reqRank, reqKnown := rank[req.RequiredClass]
	unknownSet := map[string]bool{}
	for _, c := range sorted {
		f := Factor{Model: c.ID, Tool: c.Tool, Class: c.Class, Capabilities: map[string]string{}, Unknowns: []string{}}
		cr, ok := rank[c.Class]
		f.MeetsRouteClass = reqKnown && ok && cr >= reqRank
		for _, cap := range req.RequiredCaps {
			v, ok := c.Caps[cap]
			if !ok || (v != Yes && v != No) {
				v = Unknown
			}
			f.Capabilities[cap] = v
		}
		if q != nil {
			f.Quota = q.QuotaOf(c.ID, now)
		} else {
			f.Quota = Quota{State: QuotaUnknown, Source: "没有额度观测来源"}
		}
		switch {
		case !f.MeetsRouteClass:
			f.Excluded = fmt.Sprintf("档位 %s 低于要求的 %s（不降档）", c.Class, req.RequiredClass)
		case hasValue(f.Capabilities, No):
			f.Excluded = "声明不具备所需能力：" + strings.Join(keysWith(f.Capabilities, No), "、")
		case f.Quota.State == QuotaExhausted:
			f.Excluded = "Work Deck 观测到额度已耗尽：" + f.Quota.Source
		}
		f.Selectable = f.Excluded == ""
		if f.Selectable {
			for _, cap := range keysWith(f.Capabilities, Unknown) {
				f.Unknowns = append(f.Unknowns, "能力 "+cap+" 未声明")
			}
			if f.Quota.State != QuotaAvailable {
				f.Unknowns = append(f.Unknowns, "额度未知")
			}
			f.Recommendable = len(f.Unknowns) == 0
			for _, u := range f.Unknowns {
				unknownSet[c.ID+"："+u] = true
			}
		}
		d.Candidates = append(d.Candidates, f)
	}
	for u := range unknownSet {
		d.Unknowns = append(d.Unknowns, u)
	}
	sort.Strings(d.Unknowns)
	switch {
	case !reqKnown:
		d.Escalated = true
		d.Reason = fmt.Sprintf("节点档位 %q 不认识，不推荐，交给 owner", req.RequiredClass)
	default:
		for _, f := range d.Candidates {
			if f.Recommendable {
				d.Recommended = f.Model
				d.Reason = "所有因子都已知且满足；在满足条件的候选里取档位最低的（够用即可，不先用最贵的）"
				break
			}
		}
		if d.Recommended == "" {
			d.Escalated = true
			if anySelectable(d.Candidates) {
				d.Reason = "每个可选候选都有未知因子（能力未声明或额度未观测到），不用默认值填补，交给 owner 挑"
			} else {
				d.Reason = "没有可选的候选（都被档位、能力或额度排除），交给 owner"
			}
		}
	}
	return d
}

// Choose 记下 owner 的选择。选的必须是可选候选；否则报错，不派。
func (d *Decision) Choose(model, by string) error {
	for _, f := range d.Candidates {
		if f.Model != model {
			continue
		}
		if !f.Selectable {
			return fmt.Errorf("模型 %s 不可选：%s", model, f.Excluded)
		}
		d.Chosen, d.ChosenBy = model, by
		switch {
		case d.Recommended == model:
			d.ChoiceReason = by + " 采纳了推荐"
		case d.Recommended == "":
			d.ChoiceReason = "没有推荐（" + d.Reason + "）；" + by + " 在可选候选里选定 " + model
			if len(f.Unknowns) > 0 {
				d.ChoiceReason += "，它的未知项：" + strings.Join(f.Unknowns, "、")
			}
		default:
			d.ChoiceReason = by + " 没有采纳推荐的 " + d.Recommended + "，改选 " + model +
				"；Work Deck 只记下这是 " + by + " 的决定，界面不收集他的理由"
			if len(f.Unknowns) > 0 {
				d.ChoiceReason += "；所选模型的未知项：" + strings.Join(f.Unknowns, "、")
			}
		}
		return nil
	}
	return fmt.Errorf("模型 %s 不在这次派单的候选里", model)
}

// Append 把一条审计记录追加进文件并 fsync。写不进去就返回错误，调用方据此拒绝派单。
func Append(path string, d Decision) error {
	if path == "" {
		return fmt.Errorf("没有路由审计记录路径")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	line, err := json.Marshal(d)
	if err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ValidateCaps 检查配置里的能力声明：键必须在全集里，值只能是 yes/no。
func ValidateCaps(caps map[string]string) error {
	for k, v := range caps {
		if !contains(Capabilities, k) {
			return fmt.Errorf("能力位 %q 不认识，只认 %s", k, strings.Join(Capabilities, "、"))
		}
		if v != Yes && v != No {
			return fmt.Errorf("能力位 %s 的值 %q 只能是 yes 或 no；不知道就别写，留作 unknown", k, v)
		}
	}
	return nil
}

// ValidateRequired 检查节点能力要求的名字都在全集里。
func ValidateRequired(caps []string) error {
	for _, c := range caps {
		if !contains(Capabilities, c) {
			return fmt.Errorf("能力要求 %q 不认识，只认 %s", c, strings.Join(Capabilities, "、"))
		}
	}
	return nil
}

func hasValue(m map[string]string, v string) bool { return len(keysWith(m, v)) > 0 }

func keysWith(m map[string]string, v string) []string {
	var out []string
	for k, x := range m {
		if x == v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func anySelectable(fs []Factor) bool {
	for _, f := range fs {
		if f.Selectable {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
