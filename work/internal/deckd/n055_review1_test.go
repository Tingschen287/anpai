package deckd

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// n055 评审 1 B2：新主 agent 登记成 running、接管旧主 agent 的待送汇报之后，看完画面之前，推送与 F8 立即送都不能往它打字。
func TestReportsHeldBeforeStartupScreenIsRead(t *testing.T) {
	w := newAgentWorld(t)
	spawnedSub(t, w)
	if code, b := w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"做完了"}`); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	old, _ := w.store.Session(w.main.Key)
	w.gone = map[string]bool{old.TmuxSession: true} // 旧主 agent 已退出，汇报留在 pending
	w.up.setOutput(claudeTrustScreen)
	var once sync.Once
	duringWindow := -1
	w.up.mu.Lock()
	w.up.paneHook = func() {
		once.Do(func() {
			// 卡在「新主 agent 已登记、汇报已接管、第一次取画面」这一刻：后台推送与 F8 立即送各跑一次
			st := newState()
			w.srv.deliverReports(context.Background(), st)
			w.srv.deliverPending(context.Background(), st, "alpha", true)
			w.up.mu.Lock()
			duringWindow = len(w.up.mainSends)
			w.up.mu.Unlock()
		})
	}
	w.up.mu.Unlock()
	code, b := w.do("owner", "POST", "/v1/projects/alpha/main", `{}`)
	if code != 201 {
		t.Fatalf("起新主 agent：%d %s", code, b)
	}
	if duringWindow != 0 {
		t.Fatalf("登记后、看完画面前不该推送汇报：送了 %d 条", duringWindow)
	}
	w.up.mu.Lock()
	w.up.paneHook = nil
	w.up.mu.Unlock()
	newMain := mainOf(t, w, "alpha")
	for _, x := range w.store.Sessions("alpha") {
		if x.Role == "main" && x.Key != old.Key {
			newMain = x
		}
	}
	if len(w.store.PendingReports(newMain.Key)) != 1 {
		t.Fatalf("汇报留给新主 agent：%+v", w.store.PendingReports(newMain.Key))
	}
	w.up.setOutput(claudeComposerScreen) // owner 选了信任
	waitUntil(t, "brief 送达", func() bool {
		for _, x := range w.store.Sessions("alpha") {
			if x.Key == newMain.Key {
				return x.BriefConfirmed
			}
		}
		return false
	})
	w.srv.deliverReports(context.Background(), newState())
	w.up.mu.Lock()
	n := len(w.up.mainSends)
	w.up.mu.Unlock()
	if n != 1 {
		t.Fatalf("brief 送达后汇报照常送一次：%d", n)
	}
}

// n055 评审 1 B3：稳定的欢迎或加载画面不等于就绪；之后才出的信任框不能收到 brief；出现真正的输入框才送。
func TestStableSplashIsNotReady(t *testing.T) {
	w := newAgentWorld(t)
	addGamma(t, w)
	splash := " ▐▛███▛█   Claude Code v2.1.296\n▝▜██████▀  Opus 5.5 · Claude Max\n\n  Loading…"
	w.up.setOutput(splash)
	before := w.up.sendCount()
	code, b := w.do("owner", "POST", "/v1/projects/gamma/main", `{}`)
	if code != 201 || decode(t, b)["brief_delivered"] != false {
		t.Fatalf("稳定的启动画面不算就绪：%d %s", code, b)
	}
	time.Sleep(20 * time.Millisecond)
	w.up.setOutput(claudeTrustScreen) // 迟到的信任框
	time.Sleep(30 * time.Millisecond)
	if w.up.sendCount() != before {
		t.Fatal("启动画面与迟到的信任框都不该收到 brief")
	}
	if x := mainOf(t, w, "gamma"); x.BriefConfirmed || !x.StartPending() {
		t.Fatalf("仍在启动期：%+v", x)
	}
	w.up.setOutput(claudeComposerScreen)
	waitUntil(t, "出现输入框后送一次", func() bool { return w.up.sendCount() == before+1 })
	waitUntil(t, "记为已确认", func() bool { return mainOf(t, w, "gamma").BriefConfirmed })
}

func TestComposerReady(t *testing.T) {
	codexReady := "• 明白。\n\n› Ask Codex to do anything\n\n  GPT-5.6-Terra high · ~/proj · Context 3% used\n  ? for shortcuts"
	codexTrust := "> You are in /tmp/x\n\n  Do you trust the contents of this directory?\n\n› 1. Yes, continue\n  2. No, quit\n\n  Press enter to continue"
	cases := []struct {
		tool, screen string
		ready        bool
	}{
		{"claude", claudeComposerScreen, true},
		{"claude", claudeTrustScreen, false},
		{"claude", " ▐▛███▛█   Claude Code v2.1.296\n\n  Loading…", false},
		{"claude", "> 我的问题\n\n● 回答\n", false}, // 没有输入框的横线
		{"codex", codexReady, true},
		{"codex", codexTrust, false},
		{"codex", "› Ask Codex to do anything", false}, // 没有底部提示
		{"gemini", "anything stable", true},           // 其他工具没有可靠特征，按稳定判断
	}
	for i, c := range cases {
		if got := ComposerReady(c.tool, c.screen); got != c.ready {
			t.Errorf("第 %d 个（%s）：%v，应为 %v\n%s", i, c.tool, got, c.ready, c.screen)
		}
	}
}

// 重启时会话还在启动期、没停在确认上：第一条消息送没送到不确定，不补送，解除限制并请 owner 看一眼。
func TestResumeStartingWithoutDialog(t *testing.T) {
	w := newAgentWorld(t)
	addGamma(t, w)
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	w.srv.WithBackground(dead)
	w.up.setOutput(" ▐▛███▛█   Claude Code v2.1.296\n\n  Loading…")
	if code, b := w.do("owner", "POST", "/v1/projects/gamma/main", `{}`); code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	w.srv.WaitBackground()
	x := mainOf(t, w, "gamma")
	w.store.SetAwaitingOwner(x.Key, "") // 模拟重启前只记了启动期
	before := w.up.sendCount()
	w.srv.WithBackground(context.Background())
	w.srv.ResumeAwaiting()
	y := mainOf(t, w, "gamma")
	if y.StartPending() || !strings.Contains(y.Note, "不确定") || w.up.sendCount() != before {
		t.Fatalf("不补送、解除限制并说明：%+v", y)
	}
}

// 评审 1 B2 的第二道：遍历时拿的是快照，真正发送前在写入门里按最新状态再查一次——快照说可以送、存储里已在启动期就不送。
func TestDeliverOneRechecksLatestState(t *testing.T) {
	w := newAgentWorld(t)
	spawnedSub(t, w)
	if code, b := w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"做完了"}`); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	snapshot, _ := w.store.Session(w.main.Key) // 遍历时的快照：不在启动期
	w.store.MarkStarting(w.main.Key)           // 之后状态变了
	rep := w.store.PendingReports(w.main.Key)[0]
	if err := w.srv.deliverOne(context.Background(), snapshot, rep, true); err == nil {
		t.Fatal("写入门里按最新状态应拒绝")
	}
	if len(w.up.mainSends) != 0 || pendingCount(w) != 1 {
		t.Fatalf("不送、留在 pending：%v %d", w.up.mainSends, pendingCount(w))
	}
}
