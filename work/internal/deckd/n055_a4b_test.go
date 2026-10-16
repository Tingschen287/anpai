package deckd

import (
	"context"
	"strings"
	"testing"
)

// attempt 4 自查：后台送第一条 brief 时 deckd 正在关闭（例如 owner 关了布局），发送被打断：不记成失败，
// 保留启动期与「正在送」，重启时按「送没送到不确定」处理。
func TestShutdownDuringFirstBriefIsUncertain(t *testing.T) {
	w := newAgentWorld(t)
	addGamma(t, w)
	ctx, cancel := context.WithCancel(context.Background())
	w.srv.WithBackground(ctx)
	w.up.setOutput(claudeTrustScreen)
	if code, b := w.do("owner", "POST", "/v1/projects/gamma/main", `{}`); code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	w.up.mu.Lock()
	w.up.sendHook = func(context.Context) { cancel() } // 发送途中关闭
	w.up.mu.Unlock()
	w.up.setOutput(claudeComposerScreen)
	w.srv.WaitBackground()
	x := mainOf(t, w, "gamma")
	if !x.Starting || !x.BriefSending || x.BriefConfirmed || strings.Contains(x.Note, "失败") {
		t.Fatalf("不记失败、保留启动期与正在送：%+v", x)
	}
}

// attempt 4 自查：Codex 历史里 owner 发过的消息也以 › 开头；只看最底下的输入框行。
func TestComposerReadyCodexHistory(t *testing.T) {
	screen := "› 帮我看看 README\n\n• 好的。\n\n› Ask Codex to do anything\n\n  ? for shortcuts"
	if !ComposerReady("codex", screen) {
		t.Fatal("历史里的 › 消息不算输入框；最底下是占位：就绪")
	}
}

// attempt 4 自查：重启后接着等的主 agent 立即对 owner 放开输入（与正常启动的「还没就绪」一致）。
func TestResumeWaitingMainOpensOwnerInput(t *testing.T) {
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
	w.store.SetAwaitingOwner(x.Key, "") // 重启时正在看画面、还没记下等待
	w.srv.WithBackground(context.Background())
	w.srv.ResumeAwaiting()
	if y := mainOf(t, w, "gamma"); !strings.Contains(y.AwaitingOwner, "还在启动") || !y.Starting {
		t.Fatalf("记下等待（中间对 owner 可写）、保持启动期：%+v", y)
	}
}
