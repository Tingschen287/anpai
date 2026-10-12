package deckd

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/lease"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

// n051 attempt 2 设计评审（owner 选 A）：推断降级为「提醒」，不占完成。
// agent-deck 里做完一轮的会话没人看过就一直是 waiting，所以 waiting 也要提醒；真正的完成只认主动报告与账本提交。

// quiet 让子 agent 先在跑、再停在 v 超过 inferAfter。
func quiet(w *agentWorld, st *inferState, tmux, v string, t0 time.Time) {
	if w.srv.activity == nil {
		w.srv.activity = &activityCache{byTmx: map[string]string{}, at: t0, now: func() time.Time { return t0 }}
	}
	w.srv.activity.byTmx[tmux] = "running"
	w.srv.inferCompletions(st, t0)
	w.srv.activity.byTmx[tmux] = v
	w.srv.inferCompletions(st, t0.Add(time.Second))
	w.srv.inferCompletions(st, t0.Add(62*time.Second))
}

func TestWaitingTriggersHint(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	quiet(w, newState(), sub.TmuxSession, "waiting", time.Now())
	reps := w.store.PendingReports(w.main.Key)
	if len(reps) != 1 || reps[0].Source != deckstore.ReportInferred || !strings.Contains(reps[0].Text, "waiting") {
		t.Fatalf("停在 waiting 也要提醒：%+v", reps)
	}
	w.srv.deliverReports(context.Background(), newState())
	if len(w.up.mainSends) != 1 || !strings.Contains(w.up.mainSends[0], "推断提醒") || !strings.Contains(w.up.mainSends[0], "不算完成") {
		t.Fatalf("转告标明推断提醒、不算完成：%v", w.up.mainSends)
	}
	if x, _ := w.store.Session(sub.Key); x.Status != deckstore.StatusRunning {
		t.Fatalf("提醒不把子 agent 标 done：%s", x.Status)
	}
}

// 提醒送出后，子 agent 很快就主动报告（中间没看到它在跑、deckd 也可能重启过）：照常转告。
func TestQuickReportAfterHintDelivered(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	quiet(w, newState(), sub.TmuxSession, "idle", time.Now())
	w.srv.deliverReports(context.Background(), newState())
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"做完了"}`)
	w.srv.deliverReports(context.Background(), newState())
	if len(w.up.mainSends) != 2 || !strings.Contains(w.up.mainSends[1], "做完了") {
		t.Fatalf("真正的完成不被提醒吞掉：%v", w.up.mainSends)
	}
}

// 这一代已经报告过就不提醒；主 agent 发了新的话、它干完又停下却没报告（已标 done 的也算），再提醒。
func TestHintOnlyWithoutCompletion(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	st := newState()
	t0 := time.Now()
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"第一件做完"}`)
	quiet(w, st, sub.TmuxSession, "waiting", t0)
	if n := len(w.store.PendingReports(w.main.Key)); n != 1 {
		t.Fatalf("已报告过：只有那条主动报告，不再提醒：%d", n)
	}
	w.srv.deliverReports(context.Background(), st)
	if code, b := w.do("main", "POST", "/v1/agents/"+sub.Key+"/send", `{"text":"再做第二件"}`); code != 200 {
		t.Fatalf("发话：%d %s", code, b)
	}
	quiet(w, st, sub.TmuxSession, "waiting", t0.Add(5*time.Minute))
	reps := w.store.PendingReports(w.main.Key)
	if len(reps) != 1 || reps[0].Source != deckstore.ReportInferred {
		t.Fatalf("新的一代停下没报告：提醒：%+v", reps)
	}
}

// 同一子 agent 同时只留一条等送的提醒；真正的完成到时提醒还没送，就换成真正的完成，只送一次。
func TestOneOutstandingHintThenReplaced(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	st := newState()
	t0 := time.Now()
	quiet(w, st, sub.TmuxSession, "idle", t0)
	quiet(w, st, sub.TmuxSession, "waiting", t0.Add(3*time.Minute))
	if n := len(w.store.PendingReports(w.main.Key)); n != 1 {
		t.Fatalf("同时只留一条等送的提醒：%d", n)
	}
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"真正做完"}`)
	w.srv.deliverReports(context.Background(), st)
	if len(w.up.mainSends) != 1 || !strings.Contains(w.up.mainSends[0], "主动报告") || !strings.Contains(w.up.mainSends[0], "真正做完") {
		t.Fatalf("换成真正的完成、只送一次：%v", w.up.mainSends)
	}
}

// 账本提交按 节点#attempt 去重：主 agent 之后发了新的话、子 agent 又被看成在跑，同一轮提交也不重报；新的一轮才报。
func TestGraphSubmitOncePerAttempt(t *testing.T) {
	w, _ := graphWorld(t)
	code, b := w.do("main", "POST", "/v1/agents", spawnGraphJSON("balanced", "节点活", "做 n099", "ga1", "/tmp/alpha", "n099_x", "claude", "claude-sonnet-5"))
	if code != 201 {
		t.Fatalf("Graph 通道开子 agent：%d %s", code, b)
	}
	var out struct{ Session deckstore.Session }
	json.Unmarshal([]byte(b), &out)
	key := out.Session.Key
	w.srv.agents.graphState = func(context.Context, deckstore.ProjectDef, string) (projectctl.State, error) {
		return projectctl.StateSubmitted, nil
	}
	st := newState()
	t0 := time.Now()
	w.srv.graphSubmits(context.Background(), st, t0)
	w.srv.deliverReports(context.Background(), st)
	if len(w.up.mainSends) != 1 {
		t.Fatalf("账本提交送一次：%v", w.up.mainSends)
	}
	w.store.BumpTaskGen(key)
	w.store.SetSessionStatus(key, deckstore.StatusRunning, "又在跑")
	w.srv.graphSubmits(context.Background(), st, t0.Add(graphEvery+time.Second))
	w.srv.deliverReports(context.Background(), st)
	if len(w.up.mainSends) != 1 {
		t.Fatalf("同一轮提交不重报：%v", w.up.mainSends)
	}
	w.store.SetGraphAttempt(key, "n099_x", 4)
	w.srv.graphSubmits(context.Background(), st, t0.Add(2*graphEvery+2*time.Second))
	w.srv.deliverReports(context.Background(), st)
	if len(w.up.mainSends) != 2 || !strings.Contains(w.up.mainSends[1], "第 4 轮") {
		t.Fatalf("新的一轮提交照常转告：%v", w.up.mainSends)
	}
}

// 同一次完成的重复主动报告（第一次已送过）：不再转告，但子 agent 照样标 done、释放名额。
func TestDuplicateActiveReportStillMarksDone(t *testing.T) {
	w, _ := graphWorld(t)
	code, b := w.do("main", "POST", "/v1/agents", spawnGraphJSON("balanced", "节点活", "做 n099", "gd1", "/tmp/alpha", "n099_x", "claude", "claude-sonnet-5"))
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
	w.do("graphsub", "POST", "/v1/reports", `{"done":true,"text":"做完了"}`)
	w.srv.deliverReports(context.Background(), newState())
	w.store.SetSessionStatus(key, deckstore.StatusRunning, "owner 又让它跑")
	before := w.store.Slots("alpha")
	if code, b := w.do("graphsub", "POST", "/v1/reports", `{"done":true,"text":"做完了"}`); code != 200 || !strings.Contains(b, `"duplicate":true`) {
		t.Fatalf("同一次完成：重复：%d %s", code, b)
	}
	w.srv.deliverReports(context.Background(), newState())
	if len(w.up.mainSends) != 1 {
		t.Fatalf("重复的不再转告：%v", w.up.mainSends)
	}
	if x, _ := w.store.Session(key); x.Status != deckstore.StatusDone {
		t.Fatalf("重复的主动报告也标 done：%s", x.Status)
	}
	if u := w.store.Slots("alpha"); u.Subagents != before.Subagents-1 {
		t.Fatalf("应释放名额：%+v → %+v", before, u)
	}
}

// busyOnceUp：第一次送主 agent 时先调 hook（模拟子 agent 在 --defer-if-busy 等待期间报告），再回「主 agent 正忙」。
type busyOnceUp struct {
	*fakeUp
	mu   sync.Mutex
	hook func()
}

func (h *busyOnceUp) SendToMain(ctx context.Context, p lease.Permit, id, msg string) error {
	h.mu.Lock()
	hook := h.hook
	h.hook = nil
	h.mu.Unlock()
	if hook != nil {
		hook()
		return agentdeck.ErrTargetBusy
	}
	return h.fakeUp.SendToMain(ctx, p, id, msg)
}

// 自查：主 agent 正忙，提醒在 sending 里等；这时真正的完成到了。提醒退回等送时并入真正的完成，只送一次。
func TestHintBackFromBusyAbsorbsCompletion(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	st := newState()
	quiet(w, st, sub.TmuxSession, "waiting", time.Now())
	h := &busyOnceUp{fakeUp: w.up}
	h.hook = func() { w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"真正做完"}`) }
	w.srv.agents.upstream = h
	w.srv.deliverReports(context.Background(), st)
	if n := len(w.store.PendingReports(w.main.Key)); n != 1 {
		t.Fatalf("退回后只剩一条：%d", n)
	}
	w.srv.deliverReports(context.Background(), st)
	if len(w.up.mainSends) != 1 || !strings.Contains(w.up.mainSends[0], "主动报告") || !strings.Contains(w.up.mainSends[0], "真正做完") {
		t.Fatalf("只送真正的完成：%v", w.up.mainSends)
	}
}

// 自查：wait 领走提醒、回应没写出去前真正的完成到了；退回时并入，只剩一条。
func TestReleasedHintAbsorbsCompletion(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	quiet(w, newState(), sub.TmuxSession, "idle", time.Now())
	got, _ := w.store.TakeReports(w.main.Key)
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"真正做完"}`)
	w.store.ReleaseClaims(w.main.Key, []string{got[0].ID})
	reps := w.store.PendingReports(w.main.Key)
	if len(reps) != 1 || reps[0].Source != deckstore.ReportActive || reps[0].ID != got[0].ID {
		t.Fatalf("退回时并入真正的完成：%+v", reps)
	}
}

// 自查：旧版本按代次编号（gen:N）留在盘上的推断行不占完成：已送的不吞主动报告；等送的被账本提交换掉。
func TestOldInferredRowsDoNotHoldCompletion(t *testing.T) {
	t.Run("已送的旧推断", func(t *testing.T) {
		w := newAgentWorld(t)
		sub := spawnedSub(t, w)
		w.store.EnqueueReport(deckstore.Report{Sub: sub.Key, Source: deckstore.ReportInferred, CompletionID: deckstore.CompletionIDFor(1), Text: "旧推断"})
		w.srv.deliverReports(context.Background(), newState())
		w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"做完了"}`)
		w.srv.deliverReports(context.Background(), newState())
		if len(w.up.mainSends) != 2 || !strings.Contains(w.up.mainSends[1], "做完了") {
			t.Fatalf("真正的完成照常转告：%v", w.up.mainSends)
		}
	})
	t.Run("等送的旧推断", func(t *testing.T) {
		w, _ := graphWorld(t)
		code, b := w.do("main", "POST", "/v1/agents", spawnGraphJSON("balanced", "节点活", "做 n099", "go1", "/tmp/alpha", "n099_x", "claude", "claude-sonnet-5"))
		if code != 201 {
			t.Fatalf("开子 agent：%d %s", code, b)
		}
		var out struct{ Session deckstore.Session }
		json.Unmarshal([]byte(b), &out)
		w.store.EnqueueReport(deckstore.Report{Sub: out.Session.Key, Source: deckstore.ReportInferred, CompletionID: deckstore.CompletionIDFor(1), Text: "旧推断"})
		w.srv.agents.graphState = func(context.Context, deckstore.ProjectDef, string) (projectctl.State, error) {
			return projectctl.StateSubmitted, nil
		}
		w.srv.graphSubmits(context.Background(), newState(), time.Now())
		reps := w.store.PendingReports(w.main.Key)
		if x, _ := w.store.Session(out.Session.Key); len(reps) != 1 || reps[0].Source != deckstore.ReportGraph || x.Status != deckstore.StatusDone {
			t.Fatalf("账本提交换掉旧推断、子 agent 标 done：%+v %s", reps, x.Status)
		}
	})
}

// 自查：deckd 重启后，重启前就开的子 agent 第一次被看到就停着（等权限确认），从那一刻计时照常提醒。
func TestHintAfterRestartWithoutSeeingRunning(t *testing.T) {
	w := newAgentWorld(t)
	old := spawnedSub(t, w)
	t0 := time.Now().Add(time.Second)
	w.srv.activity = &activityCache{byTmx: map[string]string{old.TmuxSession: "waiting"}, at: t0, now: func() time.Time { return t0 }}
	st := newState() // 新进程
	w.srv.inferCompletions(st, t0)
	w.srv.inferCompletions(st, t0.Add(30*time.Second))
	if n := len(w.store.PendingReports(w.main.Key)); n != 0 {
		t.Fatalf("不到 60 秒不提醒：%d", n)
	}
	w.srv.inferCompletions(st, t0.Add(62*time.Second))
	reps := w.store.PendingReports(w.main.Key)
	if len(reps) != 1 || reps[0].Source != deckstore.ReportInferred {
		t.Fatalf("重启前就开的子 agent 停着：照常提醒：%+v", reps)
	}
}
