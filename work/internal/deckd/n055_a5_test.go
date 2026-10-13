package deckd

import (
	"context"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// uncertainMain 造出「重启前第一条 brief 正在送」的主 agent，并有一条待转告的汇报。
func uncertainMain(t *testing.T, w *agentWorld) {
	t.Helper()
	spawnedSub(t, w)
	if code, b := w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"做完了"}`); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	w.store.MarkStarting(w.main.Key)
	w.store.MarkBriefSending(w.main.Key)
}

func mainPushes(w *agentWorld) int {
	w.up.mu.Lock()
	defer w.up.mu.Unlock()
	return len(w.up.mainSends)
}

// 评审 3 B1：重启时见到「正在送」、画面上是确认框：不补送，保持启动期，汇报（后台推送与 F8）都不往确认框里送；
// owner 处理完、出现空输入框后才收尾（记为已确认、说明不确定），之后汇报照常转告，brief 始终没有补送。
func TestUncertainResumeKeepsDialogProtected(t *testing.T) {
	w := newAgentWorld(t)
	uncertainMain(t, w)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.srv.WithBackground(ctx)
	w.up.setOutput(claudeTrustScreen)
	before := w.up.sendCount()
	w.srv.ResumeAwaiting()
	waitUntil(t, "后台认出确认框、补记等 owner", func() bool {
		y, _ := w.store.Session(w.main.Key)
		return strings.Contains(y.AwaitingOwner, "目录信任确认") && strings.Contains(y.AwaitingOwner, "不会补送")
	})
	w.srv.deliverReports(context.Background(), newState())
	w.srv.deliverPending(context.Background(), newState(), "alpha", true)
	if y, _ := w.store.Session(w.main.Key); mainPushes(w) != 0 || !y.Starting || !y.BriefSending || y.BriefConfirmed {
		t.Fatalf("确认框还在：不推汇报、保持启动期与「正在送」：pushes=%d %+v", mainPushes(w), y)
	}
	w.up.setOutput(claudeComposerScreen) // owner 选了信任
	waitUntil(t, "出现空输入框后收尾", func() bool { y, _ := w.store.Session(w.main.Key); return !y.StartPending() })
	y, _ := w.store.Session(w.main.Key)
	if !y.BriefConfirmed || y.BriefSending || !strings.Contains(y.Note, "送没送到不确定") {
		t.Fatalf("记为已确认并说明不确定：%+v", y)
	}
	w.srv.deliverReports(context.Background(), newState())
	if w.up.sendCount() != before || mainPushes(w) != 1 { // 汇报经中间窗格的送达门送，记在 mainSends，不算 sendCount
		t.Fatalf("brief 不补送，只送汇报一次：sends=%d pushes=%d", w.up.sendCount()-before, mainPushes(w))
	}
}

// 评审 3 B1：输入框里留着半截消息（重启前没打完）：不算就绪，一直保持启动期、不推汇报，等 owner 处理。
func TestUncertainResumeWaitsOnLeftoverDraft(t *testing.T) {
	w := newAgentWorld(t)
	uncertainMain(t, w)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.srv.WithBackground(ctx)
	draft := strings.Replace(claudeComposerScreen, "❯ \n", "❯ 你是项目「alpha」的主 agent，由 owner 通\n", 1)
	w.up.setOutput(draft)
	w.srv.ResumeAwaiting()
	waitUntil(t, "记下等 owner", func() bool {
		y, _ := w.store.Session(w.main.Key)
		return strings.Contains(y.AwaitingOwner, "半截消息")
	})
	for i := 0; i < 3; i++ {
		w.srv.deliverPending(context.Background(), newState(), "alpha", true)
	}
	if y, _ := w.store.Session(w.main.Key); mainPushes(w) != 0 || !y.StartPending() {
		t.Fatalf("留着半截消息：不推汇报、保持启动期：pushes=%d %+v", mainPushes(w), y)
	}
	cancel()
	w.srv.WaitBackground()
	if y, _ := w.store.Session(w.main.Key); !y.Starting || !y.BriefSending {
		t.Fatalf("deckd 关闭时保留状态，留给下次重启：%+v", y)
	}
}

// 评审 3 B1：子 agent 重启时任务正在送、停在确认上：保持启动期（发话 409），出现空输入框后才解除，任务不补送。
func TestUncertainResumeSubStaysPending(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	w.store.MarkStarting(sub.Key)
	w.store.MarkBriefSending(sub.Key)
	w.store.SetSessionStatus(sub.Key, deckstore.StatusRunning, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.srv.WithBackground(ctx)
	w.up.setOutput(claudeTrustScreen)
	before := w.up.sendCount()
	w.srv.ResumeAwaiting()
	waitUntil(t, "补记等 owner", func() bool { y, _ := w.store.Session(sub.Key); return strings.Contains(y.AwaitingOwner, "F2") })
	if y, _ := w.store.Session(sub.Key); !y.StartPending() {
		t.Fatalf("确认框还在：保持启动期：%+v", y)
	}
	w.up.setOutput(claudeComposerScreen)
	waitUntil(t, "出现空输入框后解除", func() bool { y, _ := w.store.Session(sub.Key); return !y.StartPending() })
	if w.up.sendCount() != before {
		t.Fatal("任务不补送")
	}
}
