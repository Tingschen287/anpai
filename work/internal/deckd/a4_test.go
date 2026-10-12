package deckd

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

// n051 评审 3 B2：账本查询在途时，子 agent 主动报告了第 1 件、主 agent 又发了第 2 件；查询返回第 1 件的提交。
// 这条账本信号属于第 1 件（已报告过）：作重复，不再转告；第 2 件真正的完成照常转告。
func TestGraphSignalDuringQueryAfterActiveReport(t *testing.T) {
	w, _ := graphWorld(t)
	code, b := w.do("main", "POST", "/v1/agents", spawnGraphJSON("balanced", "节点活", "做 n099", "gq1", "/tmp/alpha", "n099_x", "claude", "claude-sonnet-5"))
	if code != 201 {
		t.Fatalf("Graph 通道开子 agent：%d %s", code, b)
	}
	var out struct{ Session deckstore.Session }
	json.Unmarshal([]byte(b), &out)
	key := out.Session.Key
	prev := w.srv.callerFn
	w.srv.callerFn = func(r *http.Request) Caller {
		if r.Header.Get("X-Test-As") == "graphsub" {
			return Caller{Role: RoleSub, Project: "alpha", Session: key}
		}
		return prev(r)
	}
	w.srv.agents.graphState = func(context.Context, deckstore.ProjectDef, string) (projectctl.State, error) {
		// 查询途中：第 1 件主动报告并送达，主 agent 发了第 2 件
		w.do("graphsub", "POST", "/v1/reports", `{"done":true,"text":"第一件做完"}`)
		w.srv.deliverReports(context.Background(), newState())
		if code, b := w.do("main", "POST", "/v1/agents/"+key+"/send", `{"text":"再做第二件"}`); code != 200 {
			t.Errorf("发话：%d %s", code, b)
		}
		return projectctl.StateSubmitted, nil
	}
	w.srv.graphSubmits(context.Background(), newState(), time.Now())
	w.srv.deliverReports(context.Background(), newState())
	if len(w.up.mainSends) != 1 {
		t.Fatalf("第 1 件的账本信号是重复：不再转告：%v", w.up.mainSends)
	}
	if _, b := w.do("graphsub", "POST", "/v1/reports", `{"done":true,"text":"第二件做完"}`); strings.Contains(b, `"duplicate":true`) {
		t.Fatalf("第 2 件的完成不该被当成重复：%s", b)
	}
	w.srv.deliverReports(context.Background(), newState())
	if len(w.up.mainSends) != 2 || !strings.Contains(w.up.mainSends[1], "第二件做完") {
		t.Fatalf("两件各送一次：%v", w.up.mainSends)
	}
}
