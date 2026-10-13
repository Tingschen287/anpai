package deckd

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// n055 自查 B1：子 agent 的 CLI 停在启动确认上时，主 agent 发话一律拒绝（打进去加回车就替 owner 选了）。
func TestSendRefusedWhileSubAwaitsOwner(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	w.store.SetAwaitingOwner(sub.Key, "CLI 停在目录信任确认上")
	before := w.up.sendCount()
	code, b := w.do("main", "POST", "/v1/agents/"+sub.Key+"/send", `{"text":"1"}`)
	if code != 409 || !strings.Contains(b, "启动确认") || !strings.Contains(b, "不要重发") {
		t.Fatalf("应拒绝并说明：%d %s", code, b)
	}
	if w.up.sendCount() != before {
		t.Fatal("一个字都不该送")
	}
	if x, _ := w.store.Session(sub.Key); x.TaskGen > 1 {
		t.Fatalf("拒绝时任务代次不动：%d", x.TaskGen)
	}
	// 对照：确认处理完（标记清掉）照常发
	w.store.SetAwaitingOwner(sub.Key, "")
	if code, b := w.do("main", "POST", "/v1/agents/"+sub.Key+"/send", `{"text":"再跑一遍"}`); code != 200 {
		t.Fatalf("对照：%d %s", code, b)
	}
}

// n055 自查 B2：主 agent 的 CLI 停在启动确认上时，汇报不推送（留在 pending），brief 送达后照常送。
func TestReportsHeldWhileMainAwaitsOwner(t *testing.T) {
	w := newAgentWorld(t)
	spawnedSub(t, w)
	if code, b := w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"做完了"}`); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	w.store.SetAwaitingOwner(w.main.Key, "CLI 停在目录信任确认上")
	st := newState()
	w.srv.deliverReports(context.Background(), st)
	if len(w.up.mainSends) != 0 || pendingCount(w) != 1 {
		t.Fatalf("等 owner 处理确认时不推：%v %d", w.up.mainSends, pendingCount(w))
	}
	w.store.ConfirmBrief(w.main.Key) // 确认处理完、第一条 brief 送达
	w.srv.deliverReports(context.Background(), st)
	if len(w.up.mainSends) != 1 || pendingCount(w) != 0 {
		t.Fatalf("之后照常送一次：%v", w.up.mainSends)
	}
}

func TestStartupDialogTailOnly(t *testing.T) {
	old := claudeTrustScreen + "\n" + strings.Repeat("对话内容\n", 50) + claudeComposerScreen
	if d := StartupDialog(old); d != "" {
		t.Fatalf("历史里已消失的确认框不算：%q", d)
	}
	echo := "> 帮我做两件事：\n> 1. 写 README\n> 2. 跑测试\n\n" + claudeComposerScreen
	if d := StartupDialog(echo); d != "" {
		t.Fatalf("回显的带编号用户消息不算菜单：%q", d)
	}
	apiKey := "Detected a custom API key in your environment\n\nDo you want to use this API key?\n\n  1. Yes\n❯ 2. No (recommended)"
	if d := StartupDialog(apiKey); d == "" {
		t.Fatal("光标停在 2 上的确认也要认出")
	}
	if d := StartupDialog("Some menu\n  1. A\n› 2. B\n"); d == "" {
		t.Fatal("Codex 风格光标停在 2 上")
	}
}

// 取不到画面（或一直不稳定）：不直接送，按「还没就绪」等；画面好了、没有确认再送一次。
func TestBriefWaitsWhenScreenUnavailable(t *testing.T) {
	w := newAgentWorld(t)
	addGamma(t, w)
	w.up.mu.Lock()
	w.up.paneErr = errors.New("session output 暂时失败")
	w.up.mu.Unlock()
	before := w.up.sendCount()
	code, b := w.do("owner", "POST", "/v1/projects/gamma/main", `{}`)
	if code != 201 || decode(t, b)["brief_delivered"] != false || !strings.Contains(decode(t, b)["note"].(string), "还没就绪") {
		t.Fatalf("不直接送：%d %s", code, b)
	}
	if w.up.sendCount() != before {
		t.Fatal("取不到画面时不送")
	}
	w.up.mu.Lock()
	w.up.paneErr, w.up.output = nil, claudeComposerScreen
	w.up.mu.Unlock()
	waitUntil(t, "画面好了再送", func() bool { return w.up.sendCount() == before+1 })
	waitUntil(t, "brief 记为已确认", func() bool { return mainOf(t, w, "gamma").BriefConfirmed })
}

// deckd 重启：重启前停在确认上的主 agent 接着等（brief 按项目设置重新生成）；子 agent 的任务没保存，改说明请 owner 重派。
func TestResumeAwaitingAfterRestart(t *testing.T) {
	w := newAgentWorld(t)
	addGamma(t, w)
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	w.srv.WithBackground(dead) // 模拟重启前的后台等待随进程消失
	w.up.setOutput(claudeTrustScreen)
	if code, b := w.do("owner", "POST", "/v1/projects/gamma/main", `{}`); code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "写测试", "给 foo 写测试", "n055-rs", "/tmp/alpha/sub", "", "claude", "claude-sonnet-5"))
	if code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	subKey := decode(t, b)["session"].(map[string]any)["key"].(string)
	w.srv.WaitBackground()
	before := w.up.sendCount()
	w.up.setOutput(claudeComposerScreen) // owner 在 deckd 停着时处理了确认
	w.srv.WithBackground(context.Background())
	w.srv.ResumeAwaiting()
	waitUntil(t, "主 agent 的 brief 补送", func() bool { return mainOf(t, w, "gamma").BriefConfirmed })
	time.Sleep(20 * time.Millisecond)
	if w.up.sendCount() != before+1 {
		t.Fatalf("只补送主 agent 的一条：%d", w.up.sendCount()-before)
	}
	x, _ := w.store.Session(subKey)
	if !strings.Contains(x.AwaitingOwner, "重新派") || x.BriefConfirmed {
		t.Fatalf("子 agent 改说明、不送：%+v", x)
	}
}
