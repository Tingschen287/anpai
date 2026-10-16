package deckd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

// n051 attempt 3（owner 选方案 A'）：只给停下来的子 agent 发新的话；代次在存储锁里取；发话途中到的完成按上一代有没有完成来归。

// 自查 S1：子 agent 还在做第 1 件时主 agent 追加发话——不发、代次不动；第 1 件、第 2 件的完成各送一次。
func TestSendToRunningSubRefused(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	now := time.Now()
	w.srv.activity = &activityCache{byTmx: map[string]string{sub.TmuxSession: "running"}, at: now, now: func() time.Time { return now }}
	before := len(w.up.sends) // 开子 agent 时送的 brief
	code, b := w.do("main", "POST", "/v1/agents/"+sub.Key+"/send", `{"text":"顺便再做第二件"}`)
	if code != 409 || !strings.Contains(b, "正在做上一件") {
		t.Fatalf("还在跑：不发：%d %s", code, b)
	}
	if x, _ := w.store.Session(sub.Key); x.Gen() != 1 || x.SendInFlight || len(w.up.sends) != before {
		t.Fatalf("代次不动、什么都没送：%d %v %v", x.Gen(), x.SendInFlight, w.up.sends)
	}
}

// 缓存过时、以为它停着；上游在打字前一刻发现它还在跑（ErrTargetBusy）。这期间它做完第 1 件并报告：算第 1 件，照常送；代次撤回。
func TestBusyAtUpstreamReportCountsForPreviousTask(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	w.up.subBusy = true
	w.up.sendHook = func(context.Context) { w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"第一件做完"}`) }
	if code, _ := w.do("main", "POST", "/v1/agents/"+sub.Key+"/send", `{"text":"再做第二件"}`); code != 409 {
		t.Fatalf("上游判定还在跑：409：%d", code)
	}
	w.up.subBusy, w.up.sendHook = false, nil
	if x, _ := w.store.Session(sub.Key); x.Gen() != 1 || x.SendInFlight {
		t.Fatalf("代次撤回到 1：%d %v", x.Gen(), x.SendInFlight)
	}
	if code, _ := w.do("main", "POST", "/v1/agents/"+sub.Key+"/send", `{"text":"再做第二件"}`); code != 200 {
		t.Fatalf("停下后再发：%d", code)
	}
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"第二件做完"}`)
	w.srv.deliverReports(context.Background(), newState())
	if len(w.up.mainSends) != 2 || !strings.Contains(w.up.mainSends[0], "第一件做完") || !strings.Contains(w.up.mainSends[1], "第二件做完") {
		t.Fatalf("两件各送一次：%v", w.up.mainSends)
	}
}

// 上游判定停着并打进新的话，返回之前子 agent 就做完第 1 件的收尾报告（缓存过时的窗口）：算第 1 件；第 2 件之后照常送。
func TestReportInFlightCreditsUnfinishedPreviousTask(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	w.up.sendHook = func(context.Context) { w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"第一件做完"}`) }
	if code, _ := w.do("main", "POST", "/v1/agents/"+sub.Key+"/send", `{"text":"再做第二件"}`); code != 200 {
		t.Fatalf("发话：%d", code)
	}
	w.up.sendHook = nil
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"第二件做完"}`)
	w.srv.deliverReports(context.Background(), newState())
	if len(w.up.mainSends) != 2 || !strings.Contains(w.up.mainSends[1], "第二件做完") {
		t.Fatalf("两件各送一次：%v", w.up.mainSends)
	}
}

// 评审 2 R2：上一件已完成；新任务在 send 返回前就做完并报告（Codex 证实提交要十几秒）：算新任务，照常送。
func TestReportDuringSendCountsAsNewTask(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"第一件做完"}`)
	w.srv.deliverReports(context.Background(), newState())
	w.up.sendHook = func(context.Context) { w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"第二件做完"}`) }
	if code, b := w.do("main", "POST", "/v1/agents/"+sub.Key+"/send", `{"text":"再做第二件"}`); code != 200 {
		t.Fatalf("发话：%d %s", code, b)
	}
	w.up.sendHook = nil
	w.srv.deliverReports(context.Background(), newState())
	if len(w.up.mainSends) != 2 || !strings.Contains(w.up.mainSends[1], "第二件做完") {
		t.Fatalf("send 返回前到的真正完成算新的一次、照常转告：%v", w.up.mainSends)
	}
	if x, _ := w.store.Session(sub.Key); x.SendInFlight {
		t.Fatal("发话返回后不再算在途")
	}
}

func TestFailedSendRevertsGeneration(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"第一件做完"}`)
	w.up.sendErr = errors.New("上游投递失败")
	if code, _ := w.do("main", "POST", "/v1/agents/"+sub.Key+"/send", `{"text":"再做第二件"}`); code != 502 {
		t.Fatalf("发话失败：502：%d", code)
	}
	w.up.sendErr = nil
	if x, _ := w.store.Session(sub.Key); x.Gen() != 1 || x.SendInFlight {
		t.Fatalf("没送成：代次撤回到 1：%d %v", x.Gen(), x.SendInFlight)
	}
	// 子 agent 重报第一件：仍是同一次完成，不重复转告
	if _, b := w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"第一件做完"}`); !strings.Contains(b, `"duplicate":true`) {
		t.Fatalf("撤回后重报第一件仍算重复：%s", b)
	}
}

func TestFailedSendKeepsGenerationIfCompletionArrived(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"第一件做完"}`)
	w.srv.deliverReports(context.Background(), newState())
	w.up.sendHook = func(context.Context) { w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"第二件做完"}`) }
	w.up.sendErr = errors.New("上游投递失败（其实送到了）")
	w.do("main", "POST", "/v1/agents/"+sub.Key+"/send", `{"text":"再做第二件"}`)
	w.up.sendHook, w.up.sendErr = nil, nil
	if x, _ := w.store.Session(sub.Key); x.Gen() != 2 {
		t.Fatalf("这一代已收到真正的完成：不撤回：%d", x.Gen())
	}
	w.srv.deliverReports(context.Background(), newState())
	if len(w.up.mainSends) != 2 || !strings.Contains(w.up.mainSends[1], "第二件做完") {
		t.Fatalf("照常转告：%v", w.up.mainSends)
	}
}

// 自查：安静中主 agent 发了新的话，提醒按安静开始时的代次编号并重新计时：发话前那段安静不会被当成新任务的「停下没报告」。
func TestHintQuietRestartsOnNewGeneration(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	st := newState()
	t0 := time.Now()
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"第一件做完"}`)
	w.srv.deliverReports(context.Background(), st)
	w.srv.activity = &activityCache{byTmx: map[string]string{sub.TmuxSession: "running"}, at: t0, now: func() time.Time { return t0 }}
	w.srv.inferCompletions(st, t0)
	w.srv.activity.byTmx[sub.TmuxSession] = "idle"
	w.srv.inferCompletions(st, t0.Add(time.Second))
	if code, _ := w.do("main", "POST", "/v1/agents/"+sub.Key+"/send", `{"text":"再做第二件"}`); code != 200 {
		t.Fatalf("发话：%d", code)
	}
	w.srv.inferCompletions(st, t0.Add(30*time.Second)) // 代次变了：重新计时
	w.srv.inferCompletions(st, t0.Add(62*time.Second))
	if n := len(w.store.PendingReports(w.main.Key)); n != 0 {
		t.Fatalf("发话前那段安静不提醒：%d", n)
	}
	w.srv.inferCompletions(st, t0.Add(92*time.Second))
	reps := w.store.PendingReports(w.main.Key)
	if len(reps) != 1 || !strings.HasPrefix(reps[0].CompletionID, "hint:2:") {
		t.Fatalf("新任务之后停了 60 秒：按第 2 代提醒：%+v", reps)
	}
}

// 评审 2 R3：真正的完成已被 wait 领走并确认，退回等送的提醒删掉，不再送。
func TestStaleHintDroppedAfterCompletionDelivered(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	st := newState()
	quiet(w, st, sub.TmuxSession, "waiting", time.Now())
	h := &busyOnceUp{fakeUp: w.up}
	h.hook = func() {
		w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"真正做完"}`)
		got, _ := w.store.TakeReports(w.main.Key)
		ids := []string{}
		for _, r := range got {
			ids = append(ids, r.ID)
		}
		w.store.AckReports(w.main.Key, ids)
	}
	w.srv.agents.upstream = h
	w.srv.deliverReports(context.Background(), st)
	if n := len(w.store.PendingReports(w.main.Key)); n != 0 {
		t.Fatalf("过时的提醒删掉：%d", n)
	}
	w.srv.deliverReports(context.Background(), st)
	if len(w.up.mainSends) != 0 {
		t.Fatalf("完成已经由 wait 收到：不再推送提醒：%v", w.up.mainSends)
	}
	for _, r := range w.store.Reports("alpha") {
		if r.Source == deckstore.ReportInferred {
			t.Fatalf("提醒应已删除：%+v", r)
		}
	}
}

// 自查：提醒在发送中，主 agent 又发了话、新一代的完成已由 wait 领走确认；提醒退回时同样删掉（之后已有更新的完成）。
func TestStaleHintDroppedAfterNewerGenerationCompletion(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	st := newState()
	quiet(w, st, sub.TmuxSession, "idle", time.Now())
	h := &busyOnceUp{fakeUp: w.up}
	h.hook = func() {
		w.store.BumpTaskGen(sub.Key)
		w.store.EndSend(sub.Key)
		w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"第二件做完"}`)
		got, _ := w.store.TakeReports(w.main.Key)
		ids := []string{}
		for _, r := range got {
			ids = append(ids, r.ID)
		}
		w.store.AckReports(w.main.Key, ids)
	}
	w.srv.agents.upstream = h
	w.srv.deliverReports(context.Background(), st)
	w.srv.deliverReports(context.Background(), st)
	if len(w.up.mainSends) != 0 || len(w.store.PendingReports(w.main.Key)) != 0 {
		t.Fatalf("更新的完成已收到：过时提醒不再送：%v", w.up.mainSends)
	}
}

// attempt 3 第二轮自查（blocker）：账本信号查得晚（30 秒一次），在主 agent 发了第 2 件之后才到。
// attempt 4 起它只是账本提醒、不占完成（owner 选 B2-A）：提醒照常送，第 2 件真正的完成照常转告。
func TestLateGraphSignalCreditsEarlierTask(t *testing.T) {
	w, _ := graphWorld(t)
	code, b := w.do("main", "POST", "/v1/agents", spawnGraphJSON("balanced", "节点活", "做 n099", "lg1", "/tmp/alpha", "n099_x", "claude", "claude-sonnet-5-5"))
	if code != 201 {
		t.Fatalf("Graph 通道开子 agent：%d %s", code, b)
	}
	var out struct{ Session deckstore.Session }
	json.Unmarshal([]byte(b), &out)
	key := out.Session.Key
	// 子 agent 在账本里提交了第 1 件，没运行 report；账本信号还没查到，主 agent 就发了第 2 件
	if code, b := w.do("main", "POST", "/v1/agents/"+key+"/send", `{"text":"再做第二件"}`); code != 200 {
		t.Fatalf("发话：%d %s", code, b)
	}
	w.srv.agents.graphState = func(context.Context, deckstore.ProjectDef, string) (projectctl.State, error) {
		return projectctl.StateSubmitted, nil
	}
	w.srv.graphSubmits(context.Background(), newState(), time.Now())
	reps := w.store.PendingReports(w.main.Key)
	if len(reps) != 1 || reps[0].Source != deckstore.ReportGraph || reps[0].Ref != "n099_x#3" {
		t.Fatalf("迟到的账本信号按 节点#attempt 作账本提醒：%+v", reps)
	}
	w.srv.deliverReports(context.Background(), newState())
	prev := w.srv.callerFn
	w.srv.callerFn = func(r *http.Request) Caller {
		if r.Header.Get("X-Test-As") == "graphsub" {
			return Caller{Role: RoleSub, Project: "alpha", Session: key}
		}
		return prev(r)
	}
	if _, b := w.do("graphsub", "POST", "/v1/reports", `{"done":true,"text":"第二件做完"}`); strings.Contains(b, `"duplicate":true`) {
		t.Fatalf("第 2 件的完成不该被当成重复：%s", b)
	}
	w.srv.deliverReports(context.Background(), newState())
	if len(w.up.mainSends) != 2 || !strings.Contains(w.up.mainSends[1], "第二件做完") {
		t.Fatalf("两件各送一次：%v", w.up.mainSends)
	}
}
