package deckd

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
	"github.com/Tingschen287/terminal-deck/work/internal/viewstate"
)

// n051 评审 1 B6：§14.4 的账本信号（attempt 4 起为账本提醒，B2-A）与 owner 一键立即送达。

func TestGraphSubmitSignalReportsOnce(t *testing.T) {
	w, _ := graphWorld(t)
	code, b := w.do("main", "POST", "/v1/agents", spawnGraphJSON("balanced", "节点活", "做 n099", "gs1", "/tmp/alpha", "n099_x", "claude", "claude-sonnet-5-5"))
	if code != 201 {
		t.Fatalf("Graph 通道开子 agent：%d %s", code, b)
	}
	var out struct{ Session deckstore.Session }
	json.Unmarshal([]byte(b), &out)
	plain := spawnedSub(t, w) // 普通通道的子 agent：不查账本
	state := projectctl.StateRunning
	asked := map[string]int{}
	w.srv.agents.graphState = func(_ context.Context, p deckstore.ProjectDef, node string) (projectctl.State, error) {
		asked[node]++
		return state, nil
	}
	st := newState()
	t0 := time.Now()
	w.srv.graphSubmits(context.Background(), st, t0)
	if n := len(w.store.PendingReports(w.main.Key)); n != 0 {
		t.Fatalf("节点还在跑：不报：%d", n)
	}
	state = projectctl.StateSubmitted
	w.srv.graphSubmits(context.Background(), st, t0.Add(time.Second)) // 没到间隔：不查
	if asked["n099_x"] != 1 {
		t.Fatalf("没到间隔不该再查账本：%d", asked["n099_x"])
	}
	w.srv.graphSubmits(context.Background(), st, t0.Add(graphEvery+time.Second))
	reps := w.store.PendingReports(w.main.Key)
	if len(reps) != 1 || reps[0].Source != deckstore.ReportGraph || !strings.Contains(reps[0].Text, "第 3 轮已提交") {
		t.Fatalf("账本提醒应入队一条：%+v", reps)
	}
	if x, _ := w.store.Session(out.Session.Key); x.Status != deckstore.StatusDone {
		t.Fatalf("账本显示已提交：子 agent 标为 done：%s", x.Status)
	}
	if x, _ := w.store.Session(plain.Key); x.Status != deckstore.StatusRunning {
		t.Fatal("普通通道的子 agent 不受影响")
	}
	w.srv.deliverReports(context.Background(), st)
	if len(w.up.mainSends) != 1 || !strings.Contains(w.up.mainSends[0], "账本提醒") || !strings.Contains(w.up.mainSends[0], "不算完成") {
		t.Fatalf("转告标明账本提醒、不算完成：%v", w.up.mainSends)
	}
	// 随后的主动报告才是真正的完成：照常转告（attempt 4 设计复审，owner 选 B2-A）
	prev := w.srv.callerFn
	w.srv.callerFn = func(r *http.Request) Caller {
		if r.Header.Get("X-Test-As") == "graphsub" {
			return Caller{Role: RoleSub, Project: "alpha", Session: out.Session.Key}
		}
		return prev(r)
	}
	w.do("graphsub", "POST", "/v1/reports", `{"done":true,"text":"节点活做完了"}`)
	w.do("graphsub", "POST", "/v1/reports", `{"done":true,"text":"节点活做完了"}`) // 重报：同一次完成只送一次
	w.srv.deliverReports(context.Background(), st)
	if len(w.up.mainSends) != 2 || !strings.Contains(w.up.mainSends[1], "主动报告") {
		t.Fatalf("主动报告照常送、只送一次：%v", w.up.mainSends)
	}
}

func TestOwnerDeliverNowSkipsTypingGate(t *testing.T) {
	w := newAgentWorld(t)
	spawnedSub(t, w)
	c := &typingCenter{ro: true, shown: viewstate.Idle, last: time.Now()}
	lookup := func(id string) (viewstate.Target, bool) {
		x, ok := w.store.Session(id)
		if !ok || x.AgentDeckID == "" {
			return viewstate.Target{}, false
		}
		return viewstate.Target{Session: x.AgentDeckID, Tmux: x.TmuxSession, Role: x.Role, BriefConfirmed: true, Title: x.Title}, true
	}
	m := viewstate.New(c, w.leases, lookup, func(*i18n.Error) error { return nil }, nil)
	if err := m.Open(w.main.AgentDeckID); err != nil {
		t.Fatal(err)
	}
	w.srv.view = m
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"急件"}`)
	w.srv.deliverReports(context.Background(), newState())
	if len(w.up.mainSends) != 0 {
		t.Fatal("owner 正在打字：先排队")
	}
	found := false
	for _, n := range w.store.Notifications(0, "alpha") {
		found = found || strings.Contains(n.Text, "按 F8 立刻送")
	}
	if !found {
		t.Fatal("排队通知应写明按 F8 立刻送")
	}
	for _, as := range []string{"main", "sub"} {
		if code, _ := w.do(as, "POST", "/v1/reports/deliver-now", `{}`); code != 403 {
			t.Fatalf("%s 不能要求立刻送：%d", as, code)
		}
	}
	code, b := w.do("owner", "POST", "/v1/reports/deliver-now", `{"project":"alpha"}`)
	if code != 200 || !strings.Contains(b, `"sent":1`) || !strings.Contains(b, `"pending":0`) {
		t.Fatalf("owner 立刻送：%d %s", code, b)
	}
	if len(w.up.mainSends) != 1 || !strings.Contains(w.up.mainSends[0], "急件") {
		t.Fatalf("应已送到主 agent：%v", w.up.mainSends)
	}
	if c.ro {
		t.Fatal("送完按 brief 状态放开输入")
	}
}
