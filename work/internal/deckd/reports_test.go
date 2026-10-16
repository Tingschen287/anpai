package deckd

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/viewstate"
)

// n051：子 agent 完成后自动转告主 agent（§14.4）。

// spawnedSub 由主 agent 开一个子 agent，并让后续 "sub" 调用者就是它。
func spawnedSub(t *testing.T, w *agentWorld) deckstore.Session {
	t.Helper()
	code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "写测试", "t", "rs1", "/tmp/alpha", "", "claude", "claude-sonnet-5-5"))
	if code != 201 {
		t.Fatalf("开子 agent：%d %s", code, b)
	}
	var out struct{ Session deckstore.Session }
	json.Unmarshal([]byte(b), &out)
	prev := w.srv.callerFn
	w.srv.callerFn = func(r *http.Request) Caller {
		if r.Header.Get("X-Test-As") == "sub" {
			return Caller{Role: RoleSub, Project: "alpha", Session: out.Session.Key}
		}
		return prev(r)
	}
	return out.Session
}

func pendingCount(w *agentWorld) int { return len(w.store.PendingReports(w.main.Key)) }

func TestActiveReportQueuedOnceAndDelivered(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	for _, as := range []string{"main", "owner"} {
		if code, _ := w.do(as, "POST", "/v1/reports", `{"done":true,"text":"x"}`); code != 403 {
			t.Fatalf("%s 不能发汇报：%d", as, code)
		}
	}
	code, b := w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"写完了，测试在 foo_test.go"}`)
	if code != 200 || !strings.Contains(b, `"duplicate":false`) {
		t.Fatalf("汇报应入队：%d %s", code, b)
	}
	if _, b := w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"写完了，测试在 foo_test.go"}`); !strings.Contains(b, `"duplicate":true`) {
		t.Fatalf("同一次完成只收一次：%s", b)
	}
	if x, _ := w.store.Session(sub.Key); x.Status != deckstore.StatusDone {
		t.Fatalf("报告完成后会话应为 done（释放名额）：%s", x.Status)
	}
	if pendingCount(w) != 1 {
		t.Fatalf("应有一条等送：%d", pendingCount(w))
	}
	st := &inferState{wasBusy: map[string]bool{}, quietFrom: map[string]time.Time{}, notified: map[string]int{}}
	w.srv.deliverReports(context.Background(), st)
	if len(w.up.mainSends) != 1 || !strings.Contains(w.up.mainSends[0], "主动报告") || !strings.Contains(w.up.mainSends[0], "写测试") {
		t.Fatalf("应转告主 agent 并标明主动报告与来源：%v", w.up.mainSends)
	}
	if pendingCount(w) != 0 {
		t.Fatal("送达后不再等送")
	}
	w.srv.deliverReports(context.Background(), st)
	if len(w.up.mainSends) != 1 {
		t.Fatal("同一条不送两次")
	}
	// 不带 --done 只记通知栏
	if code, b := w.do("sub", "POST", "/v1/reports", `{"done":false,"text":"进度：一半"}`); code != 200 || !strings.Contains(b, `"queued":false`) {
		t.Fatalf("进度汇报只发通知：%d %s", code, b)
	}
}

func TestReportBusyRetriesUnconfirmedNotResent(t *testing.T) {
	w := newAgentWorld(t)
	spawnedSub(t, w)
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"完成 A"}`)
	st := &inferState{wasBusy: map[string]bool{}, quietFrom: map[string]time.Time{}, notified: map[string]int{}}
	w.up.mainErr = agentdeck.ErrTargetBusy
	w.srv.deliverReports(context.Background(), st)
	if pendingCount(w) != 1 {
		t.Fatal("主 agent 正在回答：没送，仍等送")
	}
	w.up.mainErr = &agentdeck.ErrDeliveryUnconfirmed{SessionID: "x", Detail: "typed"}
	w.srv.deliverReports(context.Background(), st)
	if pendingCount(w) != 0 {
		t.Fatal("送达未确认：不再等送")
	}
	w.up.mainErr = nil
	w.srv.deliverReports(context.Background(), st)
	if len(w.up.mainSends) != 0 {
		t.Fatalf("送达未确认的不自动重发：%v", w.up.mainSends)
	}
	ns := w.store.Notifications(0, "alpha")
	if !strings.Contains(ns[len(ns)-1].Text, "送达未确认") {
		t.Fatalf("应通知送达未确认：%v", ns[len(ns)-1])
	}
}

// 最小的中间窗格：主 agent 显示在中间、owner 刚打过字。
type typingCenter struct {
	ro    bool
	shown string
	last  time.Time
}

func (c *typingCenter) SetReadOnly(ro bool) error             { c.ro = ro; return nil }
func (c *typingCenter) ReadOnly() (bool, error)               { return c.ro, nil }
func (c *typingCenter) Switch(s string) error                 { c.shown = s; return nil }
func (c *typingCenter) LastInput() (time.Time, error)         { return c.last, nil }
func (c *typingCenter) Capture(string, int) ([]string, error) { return []string{"x"}, nil }
func (c *typingCenter) ClientID() (int, error)                { return 1, nil }
func (c *typingCenter) Bind(int)                              {}

func TestReportQueuedWhileOwnerTyping(t *testing.T) {
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
	m := viewstate.New(c, w.leases, lookup, func(string) error { return nil }, nil)
	if err := m.Open(w.main.AgentDeckID); err != nil {
		t.Fatal(err)
	}
	w.srv.view = m
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"完成 B"}`)
	st := &inferState{wasBusy: map[string]bool{}, quietFrom: map[string]time.Time{}, notified: map[string]int{}}
	w.srv.deliverReports(context.Background(), st)
	w.srv.deliverReports(context.Background(), st)
	if pendingCount(w) != 1 || len(w.up.mainSends) != 0 {
		t.Fatal("owner 正在主 agent 那里打字：先排队不送")
	}
	n := 0
	for _, x := range w.store.Notifications(0, "alpha") {
		if strings.Contains(x.Text, "条汇报等着转告主 agent") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("排队只通知一次：%d", n)
	}
	c.last = time.Now().Add(-time.Minute) // owner 停手了
	w.srv.deliverReports(context.Background(), st)
	if len(w.up.mainSends) != 1 || c.ro {
		t.Fatalf("停手后应送达，并按 brief 状态恢复可写：sends=%v ro=%v", w.up.mainSends, c.ro)
	}
}

func TestInferredCompletionAfterQuietPeriod(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	w.srv.activity = &activityCache{byTmx: map[string]string{sub.TmuxSession: "running"}, at: time.Now(), now: time.Now}
	st := &inferState{wasBusy: map[string]bool{}, quietFrom: map[string]time.Time{}, notified: map[string]int{}}
	t0 := time.Now()
	w.srv.inferCompletions(st, t0)
	set := func(v string) {
		w.srv.activity.mu.Lock()
		w.srv.activity.byTmx[sub.TmuxSession], w.srv.activity.at = v, time.Now()
		w.srv.activity.mu.Unlock()
	}
	set("idle")
	w.srv.inferCompletions(st, t0.Add(time.Second))
	w.srv.inferCompletions(st, t0.Add(30*time.Second))
	if pendingCount(w) != 0 {
		t.Fatal("安静不到 60 秒不推断")
	}
	w.srv.inferCompletions(st, t0.Add(62*time.Second))
	reps := w.store.PendingReports(w.main.Key)
	if len(reps) != 1 || reps[0].Source != deckstore.ReportInferred || !strings.Contains(reps[0].Text, "推断") {
		t.Fatalf("应按推断入队：%+v", reps)
	}
	w.srv.inferCompletions(st, t0.Add(200*time.Second))
	if pendingCount(w) != 1 {
		t.Fatal("同一段安静只推断一次")
	}
	// 一直空闲、从没在跑过的子 agent 不推断
	w.srv.deliverReports(context.Background(), st)
	if !strings.Contains(w.up.mainSends[0], "推断") {
		t.Fatalf("转告应标明推断：%v", w.up.mainSends)
	}
}
