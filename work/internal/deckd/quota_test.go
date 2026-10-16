package deckd

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/dashquota"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// n051 评审 1 B2：额度未知时启动 frontier 子 agent，两条通道一律上报，批准绑定本次请求。

func frontierAllowed(t *testing.T, w *agentWorld) {
	t.Helper()
	set := w.store.Settings()
	for i := range set.Projects {
		set.Projects[i].Envelope.MaxFrontier = 1
		set.Projects[i].Envelope.Version++
		set.Projects[i].GraphRoot = "/tmp/" + set.Projects[i].ID + "-graph"
	}
	if err := w.store.ReplaceSettings(set); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownQuotaFrontierNeedsApproval(t *testing.T) {
	w := newAgentWorld(t)
	frontierAllowed(t, w) // 包络允许 1 个 frontier：只剩额度这道门
	body := spawnJSON("frontier", "难题", "t", "q1", "/tmp/alpha", "", "claude", "claude-opus-5-5")
	code, b := w.do("main", "POST", "/v1/agents", body)
	if code != 202 || !strings.Contains(b, `"pending":"cost"`) || !strings.Contains(b, "额度未知") || len(w.up.launches) != 0 {
		t.Fatalf("额度未知时 frontier 应上报、不启动：%d %s", code, b)
	}
	dec := decode(t, b)["decision"].(map[string]any)
	id := dec["id"].(string)
	w.do("owner", "POST", "/v1/decisions/"+id+"/answer", fmt.Sprintf(`{"option":"approve","version":%v}`, dec["version"]))
	if code, b := w.do("main", "POST", "/v1/agents", spawnJSON("frontier", "难题", "t", "q1", "/tmp/alpha", id, "claude", "claude-opus-5-5")); code != 201 {
		t.Fatalf("批准后带同一请求应开成：%d %s", code, b)
	}
	if d, _ := w.store.Decision(id); d.State != deckstore.StateApplied || !strings.Contains(d.Result, "额度未知") {
		t.Fatalf("例外应标为已用：%s %q", d.State, d.Result)
	}
	// 换一个请求不能沿用
	if code, _ := w.do("main", "POST", "/v1/agents", spawnJSON("frontier", "别的难题", "t", "q2", "/tmp/alpha", id, "claude", "claude-opus-5-5")); code != 409 {
		t.Fatalf("批准只绑定那一个请求：%d", code)
	}
}

func TestKnownQuotaFrontierWithinEnvelopeStarts(t *testing.T) {
	w := newAgentWorld(t)
	frontierAllowed(t, w)
	w.srv.agents.quotaOf = func(context.Context, string) string { return dashquota.Available }
	if code, b := w.do("main", "POST", "/v1/agents", spawnJSON("frontier", "难题", "t", "q3", "/tmp/alpha", "", "claude", "claude-opus-5-5")); code != 201 {
		t.Fatalf("额度已知且在包络内：直接开：%d %s", code, b)
	}
	w.srv.agents.quotaOf = func(context.Context, string) string { return dashquota.Exhausted }
	if code, _ := w.do("main", "POST", "/v1/agents", spawnJSON("frontier", "又一个", "t", "q4", "/tmp/alpha", "", "claude", "claude-opus-5-5")); code != 202 {
		t.Fatalf("额度用尽：上报：%d", code)
	}
	// 非 frontier 不受影响
	w.srv.agents.quotaOf = nil
	if code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "普通", "t", "q5", "/tmp/alpha", "", "claude", "claude-sonnet-5-5")); code != 201 {
		t.Fatalf("balanced 不受额度门影响：%d %s", code, b)
	}
}

func TestUnknownQuotaGateAppliesToGraphChannel(t *testing.T) {
	w, g := graphWorld(t)
	frontierAllowed(t, w)
	code, b := w.do("main", "POST", "/v1/agents", spawnGraphJSON("frontier", "节点难题", "t", "q6", "/tmp/alpha", "n099_x", "claude", "claude-opus-5-5"))
	if code != 202 || !strings.Contains(b, "额度未知") || len(g.calls) != 0 || len(w.up.launches) != 0 {
		t.Fatalf("Graph 通道同样上报、不开一轮、不启动：%d %s", code, b)
	}
}
