package deckd

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// n051 自查的两处小修。

// 主 agent 不能自己写开子 agent / Codex trust 的批准范围：owner 在面板上只看到说明，批准的却是藏在范围里的启动。
func TestMainCannotForgeSpawnScopeDecision(t *testing.T) {
	w := newAgentWorld(t)
	for _, a := range []string{"spawn", "codex_trust"} {
		code, b := w.do("main", "POST", "/v1/decisions", `{"kind":"cost","summary":"买 20 元额度","scope":{"action":"`+a+`","tier":"frontier"}}`)
		if code != 400 || !strings.Contains(b, "只能由 deck") {
			t.Fatalf("action=%s 的范围应拒绝：%d %s", a, code, b)
		}
	}
	if code, b := w.do("main", "POST", "/v1/decisions", `{"kind":"cost","summary":"买 20 元额度","scope":{"action":"buy_credit","amount":"20"}}`); code != 201 {
		t.Fatalf("其他范围照常上报：%d %s", code, b)
	}
}

// 批准例外之后名额又够了：例外没被消耗，不能一直停在 executing；收尾后也不能再用。
func TestUnneededExceptionIsClosedNotLeftExecuting(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w) // 夹具已有 1 个，加上它 2 个
	if code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "第三个", "t", "x3", "/tmp/alpha", "", "claude", "claude-sonnet-5")); code != 201 {
		t.Fatalf("第三个：%d %s", code, b)
	}
	code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "第四个", "t", "x4", "/tmp/alpha", "", "claude", "claude-sonnet-5"))
	if code != 202 {
		t.Fatalf("包络满了应生成 cost 决定：%d %s", code, b)
	}
	dec := decode(t, b)["decision"].(map[string]any)
	id := dec["id"].(string)
	// 一个子 agent 报告完成，名额空出来；owner 这时才批准
	if code, _ := w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"做完了"}`); code != 200 {
		t.Fatalf("汇报：%d", code)
	}
	if x, _ := w.store.Session(sub.Key); x.Status != deckstore.StatusDone {
		t.Fatalf("应已完成：%s", x.Status)
	}
	w.do("owner", "POST", "/v1/decisions/"+id+"/answer", fmt.Sprintf(`{"option":"approve","version":%v}`, dec["version"]))
	if code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "第四个", "t", "x4", "/tmp/alpha", id, "claude", "claude-sonnet-5")); code != 201 {
		t.Fatalf("带例外重试应开成：%d %s", code, b)
	}
	d, _ := w.store.Decision(id)
	if d.State != deckstore.StateApplied || !strings.Contains(d.Result, "没有消耗") {
		t.Fatalf("没用上的例外应收尾、说明没消耗：%s %q", d.State, d.Result)
	}
}
