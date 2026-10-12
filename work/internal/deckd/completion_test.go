package deckd

import (
	"context"
	"strings"
	"testing"
	"time"
)

// n051 评审 1 B4：「同一次完成只送一次」按子 agent 的任务代次，而不是汇报正文。

func newState() *inferState {
	return &inferState{wasBusy: map[string]bool{}, quietFrom: map[string]time.Time{}, notified: map[string]int{}}
}

func TestRewordedReportSameCompletionSentOnce(t *testing.T) {
	w := newAgentWorld(t)
	spawnedSub(t, w)
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"做完了，测试通过"}`)
	_, b := w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"做完了，测试通过。证据在 foo_test.go"}`)
	if !strings.Contains(b, `"duplicate":true`) {
		t.Fatalf("改写正文仍是同一次完成：%s", b)
	}
	w.srv.deliverReports(context.Background(), newState())
	if len(w.up.mainSends) != 1 {
		t.Fatalf("同一次完成只转告一次：%v", w.up.mainSends)
	}
	found := false
	for _, n := range w.store.Notifications(0, "alpha") {
		found = found || strings.Contains(n.Text, "补充") && strings.Contains(n.Text, "foo_test.go")
	}
	if !found {
		t.Fatal("补充说明应记进通知栏，不悄悄丢")
	}
}

func TestInferredThenActiveSameCompletion(t *testing.T) {
	infer := func(w *agentWorld, tmux string) {
		now := time.Now()
		w.srv.activity = &activityCache{byTmx: map[string]string{tmux: "running"}, at: now, now: func() time.Time { return now }}
		st := newState()
		w.srv.inferCompletions(st, now)
		w.srv.activity.byTmx[tmux] = "idle"
		w.srv.inferCompletions(st, now.Add(time.Second))
		w.srv.inferCompletions(st, now.Add(62*time.Second))
	}
	t.Run("推断还没送", func(t *testing.T) {
		w := newAgentWorld(t)
		sub := spawnedSub(t, w)
		infer(w, sub.TmuxSession)
		w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"主动报告的正文"}`)
		w.srv.deliverReports(context.Background(), newState())
		if len(w.up.mainSends) != 1 || !strings.Contains(w.up.mainSends[0], "主动报告的正文") || !strings.Contains(w.up.mainSends[0], "主动报告") {
			t.Fatalf("只送一次，送主动报告的那条：%v", w.up.mainSends)
		}
	})
	t.Run("推断已送过", func(t *testing.T) {
		w := newAgentWorld(t)
		sub := spawnedSub(t, w)
		infer(w, sub.TmuxSession)
		w.srv.deliverReports(context.Background(), newState())
		w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"迟到的主动报告"}`)
		w.srv.deliverReports(context.Background(), newState())
		if len(w.up.mainSends) != 1 || !strings.Contains(w.up.mainSends[0], "推断") {
			t.Fatalf("同一次完成已按推断送过，不再送：%v", w.up.mainSends)
		}
	})
}

func TestNewInstructionStartsNewCompletion(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"第一件做完"}`)
	w.srv.deliverReports(context.Background(), newState())
	if code, b := w.do("main", "POST", "/v1/agents/"+sub.Key+"/send", `{"text":"再做第二件"}`); code != 200 {
		t.Fatalf("发话：%d %s", code, b)
	}
	if x, _ := w.store.Session(sub.Key); x.Gen() != 2 {
		t.Fatalf("发话后任务代次应为 2：%d", x.Gen())
	}
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"第二件做完"}`)
	w.srv.deliverReports(context.Background(), newState())
	if len(w.up.mainSends) != 2 || !strings.Contains(w.up.mainSends[1], "第二件做完") {
		t.Fatalf("新的任务完成应再转告一次：%v", w.up.mainSends)
	}
}
