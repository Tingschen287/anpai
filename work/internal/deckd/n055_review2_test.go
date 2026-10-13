package deckd

import (
	"context"
	"strings"
	"testing"
)

// 评审 2 R1：deckd 在新主 agent 的启动期、还没认出确认框时重启：第一条消息肯定还没送，恢复后保持启动期接着等，
// 汇报一直不推；后台认出确认框后补记等 owner；owner 处理完送一次 brief，之后汇报照常送。
func TestResumeStartingNotSentKeepsWaiting(t *testing.T) {
	w := newAgentWorld(t)
	spawnedSub(t, w)
	if code, b := w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"做完了"}`); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	old, _ := w.store.Session(w.main.Key)
	w.gone = map[string]bool{old.TmuxSession: true}
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	w.srv.WithBackground(dead) // 重启前：后台等待随进程消失
	w.up.setOutput(" ▐▛███▛█   Claude Code v2.1.296\n\n  Loading…")
	if code, b := w.do("owner", "POST", "/v1/projects/alpha/main", `{}`); code != 201 {
		t.Fatalf("起新主 agent：%d %s", code, b)
	}
	w.srv.WaitBackground()
	var nm string
	for _, x := range w.store.Sessions("alpha") {
		if x.Role == "main" && x.Key != old.Key {
			nm = x.Key
		}
	}
	w.store.SetAwaitingOwner(nm, "") // 还原「重启时正在看画面、还没记下等待」的那一刻
	x, _ := w.store.Session(nm)
	if !x.Starting || x.AwaitingOwner != "" || x.BriefSending {
		t.Fatalf("重启前：只在启动期：%+v", x)
	}
	w.up.setOutput(claudeTrustScreen) // 确认框出现时 deckd 正在重启
	before := w.up.sendCount()
	w.gone = map[string]bool{old.TmuxSession: true}
	w.srv.WithBackground(context.Background())
	w.srv.ResumeAwaiting()
	waitUntil(t, "后台认出确认框、补记等 owner", func() bool {
		y, _ := w.store.Session(nm)
		return strings.Contains(y.AwaitingOwner, "目录信任确认")
	})
	w.srv.deliverReports(context.Background(), newState())
	w.srv.deliverPending(context.Background(), newState(), "alpha", true)
	w.up.mu.Lock()
	pushed := len(w.up.mainSends)
	w.up.mu.Unlock()
	if y, _ := w.store.Session(nm); pushed != 0 || y.BriefConfirmed || w.up.sendCount() != before {
		t.Fatalf("恢复后不记为已确认、不推汇报、不送 brief：pushed=%d %+v", pushed, y)
	}
	w.up.setOutput(claudeComposerScreen) // owner 选了信任
	waitUntil(t, "送一次 brief", func() bool { return w.up.sendCount() == before+1 })
	waitUntil(t, "记为已确认", func() bool { y, _ := w.store.Session(nm); return y.BriefConfirmed && !y.StartPending() })
	w.srv.deliverReports(context.Background(), newState())
	w.up.mu.Lock()
	pushed = len(w.up.mainSends)
	w.up.mu.Unlock()
	if pushed != 1 {
		t.Fatalf("brief 送达后汇报照常送：%d", pushed)
	}
}

// 评审 2 R2：第一条消息往上游送之前先落盘「正在送」；重启时见到它，送没送到不确定，不补送。
func TestBriefSendingPersistedAndNotResent(t *testing.T) {
	w := newAgentWorld(t)
	addGamma(t, w)
	seen := false
	w.up.sendHook = func(context.Context) {
		for _, x := range w.store.Sessions("gamma") {
			if x.Role == "main" {
				seen = seen || x.BriefSending
			}
		}
	}
	if code, b := w.do("owner", "POST", "/v1/projects/gamma/main", `{}`); code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	w.up.sendHook = nil
	if !seen {
		t.Fatal("送之前「正在送」已落盘")
	}
	x := mainOf(t, w, "gamma")
	if x.BriefSending || x.StartPending() {
		t.Fatalf("送达后清掉：%+v", x)
	}
	// 另起一个停在确认框上的主 agent，模拟「后台补送已把 brief 交给上游、确认还没落盘」时 deckd 退出
	w2 := newAgentWorld(t)
	addGamma(t, w2)
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	w2.srv.WithBackground(dead)
	w2.up.setOutput(claudeTrustScreen)
	if code, b := w2.do("owner", "POST", "/v1/projects/gamma/main", `{}`); code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	w2.srv.WaitBackground()
	y := mainOf(t, w2, "gamma")
	w2.store.MarkBriefSending(y.Key)
	w2.up.setOutput(claudeComposerScreen)
	before := w2.up.sendCount()
	w2.srv.WithBackground(context.Background())
	w2.srv.ResumeAwaiting()
	w2.srv.WaitBackground()
	z, _ := w2.store.Session(y.Key)
	if w2.up.sendCount() != before || !strings.Contains(z.Note, "送没送到不确定") || z.StartPending() || !z.BriefConfirmed {
		t.Fatalf("不补送、解除启动期、主 agent 记为已确认并说明：%+v", z)
	}
}

// 评审 2 R3：Codex 输入框里有 owner 没提交的草稿时不算就绪；只有空或占位才算。
func TestComposerReadyCodexDraft(t *testing.T) {
	foot := "\n\n  GPT-5.6-Terra high · ~/proj\n  ? for shortcuts"
	if !ComposerReady("codex", "› Ask Codex to do anything"+foot) || !ComposerReady("codex", "› "+foot) {
		t.Fatal("占位或空：就绪")
	}
	if ComposerReady("codex", "› 先修一下 README"+foot) || ComposerReady("codex", "› Ask Codex to do anything else"+foot) {
		t.Fatal("草稿：不算就绪")
	}
}
