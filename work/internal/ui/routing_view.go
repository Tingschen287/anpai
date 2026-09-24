package ui

import (
	"sort"
	"strings"

	"github.com/Tingschen287/terminal-deck/work/internal/dispatch"
	"github.com/Tingschen287/terminal-deck/work/internal/routing"
)

// selectableFirst 只留路由判为可选的候选，推荐的排第一，其余按路由的顺序。
func selectableFirst(cands []dispatch.Model, dec routing.Decision) []dispatch.Model {
	order := map[string]int{}
	ok := map[string]bool{}
	for i, f := range dec.Candidates {
		order[f.Model] = i
		ok[f.Model] = f.Selectable
	}
	var out []dispatch.Model
	for _, c := range cands {
		if ok[c.ID] {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].ID == dec.Recommended) != (out[j].ID == dec.Recommended) {
			return out[i].ID == dec.Recommended
		}
		return order[out[i].ID] < order[out[j].ID]
	})
	return out
}

func excludedSummary(dec routing.Decision) string {
	var parts []string
	for _, f := range dec.Candidates {
		if f.Excluded != "" && f.MeetsRouteClass {
			parts = append(parts, f.Model+"："+f.Excluded)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "（" + strings.Join(parts, "；") + "）"
}

func factorOf(dec *routing.Decision, model string) (routing.Factor, bool) {
	if dec == nil {
		return routing.Factor{}, false
	}
	for _, f := range dec.Candidates {
		if f.Model == model {
			return f, true
		}
	}
	return routing.Factor{}, false
}

// factorLine 是一个候选的因子，一行：能力、额度、未知项。
func factorLine(f routing.Factor) string {
	keys := make([]string, 0, len(f.Capabilities))
	for k := range f.Capabilities {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var caps []string
	for _, k := range keys {
		caps = append(caps, k+"="+f.Capabilities[k])
	}
	s := "额度 " + f.Quota.State
	if len(caps) > 0 {
		s = "能力 " + strings.Join(caps, " ") + "　" + s
	}
	if len(f.Unknowns) > 0 {
		s += "　未知：" + strings.Join(f.Unknowns, "、")
	}
	return s
}

func capsRequirement(dec *routing.Decision) string {
	if len(dec.RequiredCaps) == 0 {
		return "无（" + dec.RequiredCapsSource + "）"
	}
	return strings.Join(dec.RequiredCaps, "、") + "（" + dec.RequiredCapsSource + "）"
}
