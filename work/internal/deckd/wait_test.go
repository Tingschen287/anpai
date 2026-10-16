package deckd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// n051 Codex 端到端实测：Codex 开完子 agent 不结束这一轮、用 sleep 反复等，推送循环送不进去。主 agent 可以自己等汇报。

func TestMainWaitTakesReportOnceAndPushSkipsIt(t *testing.T) {
	old := waitPoll
	waitPoll = 20 * time.Millisecond
	t.Cleanup(func() { waitPoll = old })
	w := newAgentWorld(t)
	spawnedSub(t, w)
	go func() {
		time.Sleep(150 * time.Millisecond)
		w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"pong 42"}`)
	}()
	code, b := w.do("main", "GET", "/v1/reports/wait?timeout=5", "")
	if code != 200 || !strings.Contains(b, "主动报告") || !strings.Contains(b, "pong 42") || strings.Contains(b, `"timeout":true`) {
		t.Fatalf("主 agent 等到汇报（标明主动报告）：%d %s", code, b)
	}
	if n := pendingCount(w); n != 0 {
		t.Fatalf("取走的不再等送：%d", n)
	}
	st := &inferState{wasBusy: map[string]bool{}, quietFrom: map[string]time.Time{}, notified: map[string]int{}}
	w.srv.deliverReports(context.Background(), st)
	if len(w.up.mainSends) != 0 {
		t.Fatalf("主 agent 自己取走的，推送循环不再送：%v", w.up.mainSends)
	}
	if code, b := w.do("main", "GET", "/v1/reports/wait?timeout=0", ""); code != 200 || !strings.Contains(b, `"timeout":true`) {
		t.Fatalf("没有汇报：到时返回：%d %s", code, b)
	}
}

func TestMainWaitRoleAndScope(t *testing.T) {
	w := newAgentWorld(t)
	spawnedSub(t, w)
	for _, as := range []string{"sub", "owner", "unknown"} {
		if code, _ := w.do(as, "GET", "/v1/reports/wait?timeout=0", ""); code != 403 {
			t.Fatalf("%s 不能等主 agent 的汇报：%d", as, code)
		}
	}
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"给 alpha 的"}`)
	if _, b := w.do("betamain", "GET", "/v1/reports/wait?timeout=0", ""); strings.Contains(b, "给 alpha 的") {
		t.Fatalf("别的项目的主 agent 取不到：%s", b)
	}
	// 推送循环正在送（sending）的不取
	reps := w.store.PendingReports(w.main.Key)
	if len(reps) != 1 {
		t.Fatalf("应有一条等送：%d", len(reps))
	}
	if err := w.store.MarkReport(reps[0].ID, deckstore.ReportSending); err != nil {
		t.Fatal(err)
	}
	if _, b := w.do("main", "GET", "/v1/reports/wait?timeout=0", ""); strings.Contains(b, "给 alpha 的") {
		t.Fatalf("正在推送的不取：%s", b)
	}
}

func TestMainBriefSaysEndTurnAndWait(t *testing.T) {
	old := DeckCommand // 同包的 daemon 测试会把它设成测试二进制的路径
	DeckCommand = "deck"
	t.Cleanup(func() { DeckCommand = old })
	b := MainBrief(deckstore.ProjectDef{ID: "a", Name: "A", Path: "/tmp/a", Envelope: deckstore.DefaultEnvelope("a")})
	for _, want := range []string{"结束这一轮", "不要用 sleep", "deck agent wait"} {
		if !strings.Contains(b, want) {
			t.Fatalf("主 agent 的 brief 应写明 %q：\n%s", want, b)
		}
	}
}
