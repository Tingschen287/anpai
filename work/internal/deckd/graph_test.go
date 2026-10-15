package deckd

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

// n051 自查：Graph 通道与普通通道共用准入；会话起来并核过之后才开一轮，开不成就停掉刚起的会话。

type fakeGraph struct {
	mu       sync.Mutex
	calls    []string // node|actor|model|tier
	launched []int    // 每次调用时上游已启动的次数
	attempt  int
	err      error
}

func graphWorld(t *testing.T) (*agentWorld, *fakeGraph) {
	t.Helper()
	w := newAgentWorld(t)
	set := w.store.Settings()
	for i := range set.Projects {
		set.Projects[i].GraphRoot = "/tmp/" + set.Projects[i].ID + "-graph"
	}
	if err := w.store.ReplaceSettings(set); err != nil {
		t.Fatal(err)
	}
	g := &fakeGraph{attempt: 3}
	w.srv.agents.graphStart = func(_ context.Context, p deckstore.ProjectDef, node, actor, model string, route deckstore.Tier) (int, error) {
		g.mu.Lock()
		defer g.mu.Unlock()
		w.up.mu.Lock()
		g.launched = append(g.launched, len(w.up.launches))
		w.up.mu.Unlock()
		g.calls = append(g.calls, strings.Join([]string{node, actor, model, string(route)}, "|"))
		return g.attempt, g.err
	}
	return w, g
}

func spawnGraphJSON(tier, title, task, req, dir, node, agentID, model string) string {
	b := spawnBody{Tier: tier, Title: title, Task: task, RequestID: req, Dir: dir, GraphNode: node, TaskSHA256: TaskSHA256(task)}
	b.ArgsSHA256 = SpawnArgsSHA256(agentID, tier, model, title, dir, node)
	out, _ := json.Marshal(b)
	return string(out)
}

func TestGraphChannelStartsAfterLaunchAndBindsAttempt(t *testing.T) {
	w, g := graphWorld(t)
	code, b := w.do("main", "POST", "/v1/agents", spawnGraphJSON("balanced", "节点活", "做 n099", "g1", "/tmp/alpha", "n099_x", "claude", "claude-sonnet-5-5"))
	if code != 201 {
		t.Fatalf("Graph 通道应能开：%d %s", code, b)
	}
	if len(g.calls) != 1 || !strings.HasPrefix(g.calls[0], "n099_x|deck:") || !strings.HasSuffix(g.calls[0], "|claude-sonnet-5-5|balanced") {
		t.Fatalf("应以 deck:<会话> 为 actor、按解析出的模型与档位开一轮：%v", g.calls)
	}
	if g.launched[0] != 1 {
		t.Fatalf("应在会话起来之后才开一轮（开一轮时已启动 %d 次）", g.launched[0])
	}
	var out struct{ Session deckstore.Session }
	json.Unmarshal([]byte(b), &out)
	if out.Session.Graph == nil || out.Session.Graph.Node != "n099_x" || out.Session.Graph.Attempt != 3 {
		t.Fatalf("会话应绑定账本给的 attempt：%+v", out.Session.Graph)
	}
	if len(w.up.sends) != 1 {
		t.Fatalf("开成一轮后才送 brief：%v", w.up.sends)
	}
	// 普通通道不碰账本
	if code, _ := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "普通活", "t", "g2", "/tmp/alpha", "", "claude", "claude-sonnet-5-5")); code != 201 || len(g.calls) != 1 {
		t.Fatalf("普通通道不应开一轮：%d calls=%v", code, g.calls)
	}
}

func TestGraphChannelSharesAdmission(t *testing.T) {
	w, g := graphWorld(t)
	// bypass 硬拒绝
	w.cfgBypass = map[string]error{"claude": errors.New("上游会加 --dangerously-skip-permissions")}
	if code, _ := w.do("main", "POST", "/v1/agents", spawnGraphJSON("balanced", "a", "t", "g3", "/tmp/alpha", "n099_x", "claude", "claude-sonnet-5-5")); code != 400 {
		t.Fatalf("Graph 通道同样硬拒绝绕过：%d", code)
	}
	w.cfgBypass = nil
	// 预算包络：frontier 名额是 0，超出生成 cost 决定，不开一轮
	code, b := w.do("main", "POST", "/v1/agents", spawnGraphJSON("frontier", "贵活", "t", "g4", "/tmp/alpha", "n099_x", "claude", "claude-opus-5-5"))
	if code != 202 || !strings.Contains(b, `"pending":"cost"`) || !strings.Contains(b, "n099_x#0") {
		t.Fatalf("Graph 通道同样受包络约束，决定范围绑定节点：%d %s", code, b)
	}
	// 子 agent 不能开
	if code, _ := w.do("sub", "POST", "/v1/agents", spawnGraphJSON("balanced", "a", "t", "g5", "/tmp/alpha", "n099_x", "claude", "claude-sonnet-5-5")); code != 403 {
		t.Fatalf("角色检查同样适用：%d", code)
	}
	if len(g.calls) != 0 || len(w.up.launches) != 0 {
		t.Fatalf("准入没过就不启动、不开一轮：graph=%v launches=%d", g.calls, len(w.up.launches))
	}
}

func TestGraphStartFailureStopsLaunchedSession(t *testing.T) {
	for _, c := range []struct {
		name    string
		err     error
		holding bool
	}{
		{"账本明确没开", &projectctl.ErrStartNotStarted{Cause: errors.New("node not ready")}, false},
		{"账本结果不明", errors.New("projectctl 超时"), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, g := graphWorld(t)
			g.err = c.err
			before := w.store.Slots("alpha")
			code, b := w.do("main", "POST", "/v1/agents", spawnGraphJSON("balanced", "开不成", "t", "g6", "/tmp/alpha", "n099_x", "claude", "claude-sonnet-5-5"))
			if code != 409 || !strings.Contains(b, "已停掉") {
				t.Fatalf("开不成一轮：停掉刚起的会话并报错：%d %s", code, b)
			}
			if len(w.up.stops) != 1 || len(w.up.sends) != 0 {
				t.Fatalf("应停掉会话、不送 brief：stops=%v sends=%v", w.up.stops, w.up.sends)
			}
			u := w.store.Slots("alpha")
			if c.holding && u.Subagents != before.Subagents+1 || !c.holding && u != before {
				t.Fatalf("%s：名额 %+v → %+v", c.name, before, u)
			}
			ns := w.store.Notifications(0, "alpha")
			if !strings.Contains(ns[len(ns)-1].Text, "n099_x") {
				t.Fatalf("应通知 owner 哪个节点：%v", ns[len(ns)-1])
			}
		})
	}
}
