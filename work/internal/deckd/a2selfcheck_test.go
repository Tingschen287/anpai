package deckd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// n051 attempt 2 自查：任务代次去重不能吞掉真正的完成。

// 推断（误报）送过以后，子 agent 又干起活来、随后主动报告：算新的一次，送到；子 agent 标 done。
func TestFalseInferenceDoesNotSwallowLaterRealCompletion(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	now := time.Now()
	w.srv.activity = &activityCache{byTmx: map[string]string{sub.TmuxSession: "running"}, at: now, now: func() time.Time { return now }}
	st := newInferState()
	w.srv.inferCompletions(st, now)
	w.srv.activity.byTmx[sub.TmuxSession] = "idle"
	w.srv.inferCompletions(st, now.Add(time.Second))
	w.srv.inferCompletions(st, now.Add(62*time.Second))
	w.srv.deliverReports(context.Background(), st)
	if len(w.up.mainSends) != 1 || !strings.Contains(w.up.mainSends[0], "推断") {
		t.Fatalf("先按推断送一次：%v", w.up.mainSends)
	}
	// 子 agent 又干起活来（例如 owner 回答了它的问题）
	w.srv.activity.byTmx[sub.TmuxSession] = "running"
	w.srv.inferCompletions(st, now.Add(70*time.Second))
	if x, _ := w.store.Session(sub.Key); x.Gen() != 2 {
		t.Fatalf("推断过又干活：开新一代：%d", x.Gen())
	}
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"真正做完了"}`)
	w.srv.deliverReports(context.Background(), st)
	if len(w.up.mainSends) != 2 || !strings.Contains(w.up.mainSends[1], "真正做完了") {
		t.Fatalf("真正的完成应送到：%v", w.up.mainSends)
	}
	if x, _ := w.store.Session(sub.Key); x.Status != deckstore.StatusDone {
		t.Fatalf("主动报告后标 done：%s", x.Status)
	}
}

// waiting（权限提示等）不算推断；推断后没有再干活就主动报告：同一次完成只送一次，但子 agent 照样标 done、释放名额。
func TestWaitingIsNotInferredAndDuplicateStillMarksDone(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	now := time.Now()
	w.srv.activity = &activityCache{byTmx: map[string]string{sub.TmuxSession: "running"}, at: now, now: func() time.Time { return now }}
	st := newInferState()
	w.srv.inferCompletions(st, now)
	w.srv.activity.byTmx[sub.TmuxSession] = "waiting"
	w.srv.inferCompletions(st, now.Add(time.Second))
	w.srv.inferCompletions(st, now.Add(120*time.Second))
	if n := len(w.store.PendingReports(w.main.Key)); n != 0 {
		t.Fatalf("waiting 不推断：%d", n)
	}
	w.srv.activity.byTmx[sub.TmuxSession] = "idle"
	w.srv.inferCompletions(st, now.Add(121*time.Second))
	w.srv.inferCompletions(st, now.Add(190*time.Second))
	w.srv.deliverReports(context.Background(), st)
	before := w.store.Slots("alpha")
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"做完了"}`)
	w.srv.deliverReports(context.Background(), st)
	if len(w.up.mainSends) != 1 {
		t.Fatalf("同一次完成只送一次：%v", w.up.mainSends)
	}
	if x, _ := w.store.Session(sub.Key); x.Status != deckstore.StatusDone {
		t.Fatalf("主动报告与已送的推断撞上也要标 done：%s", x.Status)
	}
	if u := w.store.Slots("alpha"); u.Subagents != before.Subagents-1 {
		t.Fatalf("应释放名额：%+v → %+v", before, u)
	}
}

// 发话上游没证实（Codex 总是这样）也开新一代：之后的完成照常送到。
func TestUnconfirmedSendStillStartsNewGeneration(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"第一件"}`)
	w.srv.deliverReports(context.Background(), newInferState())
	w.up.sendErr = &agentdeck.ErrDeliveryUnconfirmed{SessionID: sub.AgentDeckID, Detail: "delivery=unverified"}
	if code, b := w.do("main", "POST", "/v1/agents/"+sub.Key+"/send", `{"text":"第二件"}`); code != 202 {
		t.Fatalf("没证实：202：%d %s", code, b)
	}
	w.up.sendErr = nil
	if x, _ := w.store.Session(sub.Key); x.Gen() != 2 {
		t.Fatalf("没证实也开新一代：%d", x.Gen())
	}
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"第二件做完"}`)
	w.srv.deliverReports(context.Background(), newInferState())
	if len(w.up.mainSends) != 2 || !strings.Contains(w.up.mainSends[1], "第二件做完") {
		t.Fatalf("之后的完成应送到：%v", w.up.mainSends)
	}
}

// 额度未知且包络也超了：同一条决定写明两个原因（这条批准随后也会被当作超额例外用掉）。
func TestQuotaGateDecisionNamesEnvelopeToo(t *testing.T) {
	w := newAgentWorld(t) // 默认包络 frontier 0 个
	code, b := w.do("main", "POST", "/v1/agents", spawnJSON("frontier", "难题", "t", "qe1", "/tmp/alpha", "", "claude", "claude-opus-5-5"))
	if code != 202 || !strings.Contains(b, "额度未知") || !strings.Contains(b, "超出预算包络") {
		t.Fatalf("决定应写明两个原因：%d %s", code, b)
	}
}
