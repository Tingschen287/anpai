package deckd

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// n051 评审 2 R2（owner 选方案 A）：发话前就把任务代次加 1；没送成时撤回，撤回前已收到这一代的真正完成就不撤。

func TestReportDuringSendCountsAsNewTask(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"第一件做完"}`)
	w.srv.deliverReports(context.Background(), newState())
	w.up.sendHook = func(context.Context) {
		// 上游还在等提交确认，子 agent 已经做完第二件并报告
		w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"第二件做完"}`)
	}
	if code, b := w.do("main", "POST", "/v1/agents/"+sub.Key+"/send", `{"text":"再做第二件"}`); code != 200 {
		t.Fatalf("发话：%d %s", code, b)
	}
	w.up.sendHook = nil
	w.srv.deliverReports(context.Background(), newState())
	if len(w.up.mainSends) != 2 || !strings.Contains(w.up.mainSends[1], "第二件做完") {
		t.Fatalf("send 返回前到的真正完成算新的一次、照常转告：%v", w.up.mainSends)
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
	if x, _ := w.store.Session(sub.Key); x.Gen() != 1 {
		t.Fatalf("没送成：代次撤回到 1：%d", x.Gen())
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

// n051 评审 2 R3：真正的完成已被 wait 领走并确认，退回等送的提醒删掉，不再送。
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
