// Package dispatch 把一个 ready 节点变成一个真实会话。
//
// 这里守两条规矩，都不留活口：
//
//  1. 说不清用的是哪个模型，就不派。不替调用方挑默认值——事后核对派工纪律时，
//     一个猜出来的模型名和没有模型名一样没用。
//  2. 不降档。节点要 frontier，就不许拿 balanced 的模型去顶。
package dispatch

import (
	"fmt"
	"sort"
	"strings"
)

// RouteClass 是节点与模型的档位。
type RouteClass string

const (
	Economy  RouteClass = "economy"
	Balanced RouteClass = "balanced"
	Frontier RouteClass = "frontier"
	Human    RouteClass = "human"
)

// rank 决定档位高低。数字大的能顶替数字小的，反过来不行。
var rank = map[RouteClass]int{Economy: 0, Balanced: 1, Frontier: 2, Human: 3}

// Known 说明这个档位是不是认识的。
func (r RouteClass) Known() bool { _, ok := rank[r]; return ok }

// Meets 表示本档位能不能满足 required 的要求。
func (r RouteClass) Meets(required RouteClass) bool {
	a, ok1 := rank[r]
	b, ok2 := rank[required]
	if !ok1 || !ok2 {
		return false
	}
	return a >= b
}

// Model 是一个可派的模型。
type Model struct {
	// 模型 ID，原样传给上游的 --model。
	ID string
	// 跑它的工具，对应上游的 -c，例如 claude、codex。
	Tool string
	// 这个模型算哪一档。
	Class RouteClass
	// Caps 是配置里显式声明的能力位（yes/no），没写的就是 unknown。绝不从 Tool 推断。
	Caps map[string]string
	// NoModel 为真：配置显式声明这个工具没有模型可选，launch 不传 --model（n042）。
	NoModel bool
}

// RouteTable 是档位表：哪个模型算哪一档。
//
// 它是配置，不是常量。供应商随时换模型，把表写死在代码里，改一次就要发一次版，
// 而且真实档位只能从实测得来，不该由程序猜。
type RouteTable struct {
	models map[string]Model
}

// NewRouteTable 按给定模型建表。同一个模型 ID 重复出现会报错，
// 因为那意味着两处对它的档位说法不一致，必须先说清楚。
func NewRouteTable(models []Model) (*RouteTable, error) {
	t := &RouteTable{models: map[string]Model{}}
	for _, m := range models {
		switch {
		case m.ID == "":
			return nil, fmt.Errorf("档位表里有模型没写 ID")
		case m.Tool == "":
			return nil, fmt.Errorf("模型 %s 没写用哪个工具运行", m.ID)
		case !m.Class.Known():
			return nil, fmt.Errorf("模型 %s 的档位 %q 不认识", m.ID, m.Class)
		}
		if _, dup := t.models[m.ID]; dup {
			return nil, fmt.Errorf("模型 %s 在档位表里出现了两次，档位说法可能冲突", m.ID)
		}
		t.models[m.ID] = m
	}
	return t, nil
}

// Lookup 查一个模型。查不到就是查不到，不返回一个兜底的。
func (t *RouteTable) Lookup(id string) (Model, error) {
	if t == nil || len(t.models) == 0 {
		return Model{}, fmt.Errorf("没有档位表，无法确认模型 %q 的档位，拒绝派工", id)
	}
	m, ok := t.models[id]
	if !ok {
		return Model{}, fmt.Errorf("档位表里没有模型 %q：模型未知时拒绝派工，不猜默认值", id)
	}
	return m, nil
}

// All 列出档位表里的全部模型，按 ID 排序。路由要看全部候选，才记得下「谁因为什么被排除」。
func (t *RouteTable) All() []Model {
	if t == nil {
		return nil
	}
	out := make([]Model, 0, len(t.models))
	for _, m := range t.models {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// CandidatesFor 列出能满足某个档位要求的模型，按档位从低到高排，
// 让人优先挑够用的那个，而不是一上来就用最贵的。
func (t *RouteTable) CandidatesFor(required RouteClass) []Model {
	if t == nil {
		return nil
	}
	var out []Model
	for _, m := range t.models {
		if m.Class.Meets(required) {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if rank[out[i].Class] != rank[out[j].Class] {
			return rank[out[i].Class] < rank[out[j].Class]
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// CheckNoDowngrade 确认拿这个模型去做要求 required 档的活，算不算降档。
func CheckNoDowngrade(m Model, required RouteClass) error {
	if !required.Known() {
		return fmt.Errorf("节点的 route class %q 不认识：拒绝派工，不猜", required)
	}
	if !m.Class.Meets(required) {
		return fmt.Errorf("拒绝降档派工：节点要求 %s，而模型 %s 只有 %s 档",
			required, m.ID, m.Class)
	}
	return nil
}

// launchModel 是传给上游 launch 的模型参数：声明了没有模型的，不传 --model。
func launchModel(m Model) (model string, notApplicable bool) {
	if m.NoModel {
		return "", true
	}
	return m.ID, false
}

// ToolOf 返回模型对应的工具名。
func ToolOf(m Model) string { return strings.TrimSpace(m.Tool) }
