package deckd

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// n051 增量自查的修复。

// 旧主 agent 退出、owner 起了新主 agent：旧主 agent 开的子 agent 与待送汇报归新主 agent，不悄悄丢。
func TestNewMainAdoptsOrphanedSubsAndReports(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"旧主 agent 时完成的"}`)
	old, _ := w.store.Session(w.main.Key)
	w.gone = map[string]bool{old.TmuxSession: true}
	code, b := w.do("owner", "POST", "/v1/projects/alpha/main", `{}`)
	if code != 201 {
		t.Fatalf("起新主 agent：%d %s", code, b)
	}
	nm := decode(t, b)["session"].(map[string]any)["key"].(string)
	if x, _ := w.store.Session(sub.Key); x.Parent != nm {
		t.Fatalf("子 agent 应归新主 agent：%s", x.Parent)
	}
	if reps := w.store.PendingReports(nm); len(reps) != 1 {
		t.Fatalf("待送汇报应归新主 agent：%d", len(reps))
	}
	found := false
	for _, n := range w.store.Notifications(0, "alpha") {
		found = found || strings.Contains(n.Text, "接管了旧主 agent 留下的 1 个子 agent、1 条待送汇报")
	}
	if !found {
		t.Fatal("应通知 owner 接管了多少")
	}
	st := &inferState{wasBusy: map[string]bool{}, quietFrom: map[string]time.Time{}, notified: map[string]int{}}
	w.srv.deliverReports(context.Background(), st)
	if len(w.up.mainSends) != 1 || !strings.Contains(w.up.mainSends[0], "旧主 agent 时完成的") {
		t.Fatalf("接管后推送给新主 agent：%v", w.up.mainSends)
	}
}

// 主 agent 正在 deck agent wait：推送循环不碰它的汇报，由 wait 取走（否则推送把汇报一直占在 sending，wait 取不到）。
func TestPushSkipsMainWhileItWaits(t *testing.T) {
	old := waitPoll
	waitPoll = 20 * time.Millisecond
	t.Cleanup(func() { waitPoll = old })
	w := newAgentWorld(t)
	spawnedSub(t, w)
	var wg sync.WaitGroup
	var body string
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, body = w.do("main", "GET", "/v1/reports/wait?timeout=3", "")
	}()
	for i := 0; i < 100 && !w.srv.waitingFor(w.main.Key); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !w.srv.waitingFor(w.main.Key) {
		t.Fatal("wait 应登记为正在等")
	}
	// 推送循环在汇报入队前后都跑一次：都不应碰它
	st := &inferState{wasBusy: map[string]bool{}, quietFrom: map[string]time.Time{}, notified: map[string]int{}}
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"等的时候完成"}`)
	w.srv.deliverReports(context.Background(), st)
	wg.Wait()
	if len(w.up.mainSends) != 0 {
		t.Fatalf("主 agent 在 wait：推送循环不应送：%v", w.up.mainSends)
	}
	if !strings.Contains(body, "等的时候完成") {
		t.Fatalf("wait 应取到：%s", body)
	}
	if w.srv.waitingFor(w.main.Key) {
		t.Fatal("wait 结束后不再登记")
	}
}

// 绝对路径只替换 brief 模板，不改主 agent 写的任务正文（正文按原文算过 sha256）。
func TestDeckCommandDoesNotRewriteTaskText(t *testing.T) {
	old := DeckCommand
	t.Cleanup(func() { DeckCommand = old })
	DeckCommand = "/opt/50%/deck"
	task := "别自己运行 deck agent spawn；照原样保留这句"
	b := subBrief(deckstore.ProjectDef{Name: "A"}, deckstore.Session{Title: "x"}, task)
	if !strings.Contains(b, task) {
		t.Fatalf("任务正文应原样：\n%s", b)
	}
	if !strings.Contains(b, "/opt/50%/deck agent report --done") {
		t.Fatalf("模板里的命令应换成绝对路径（含 %%）：\n%s", b)
	}
}
