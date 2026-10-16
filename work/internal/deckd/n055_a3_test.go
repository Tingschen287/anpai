package deckd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeListBinary 写一个冒充 agent-deck 的脚本：list --json 输出给定内容。
func fakeListBinary(t *testing.T, output string) string {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "out.txt")
	os.WriteFile(out, []byte(output), 0o644)
	bin := filepath.Join(dir, "agent-deck")
	os.WriteFile(bin, []byte("#!/bin/sh\ncat "+out+"\n"), 0o755)
	return bin
}

// n055（owner 选 A）：侧栏状态默认从 agent-deck list --json 读，不用 headless 服务与 token；一个会话都没有时上游只打一行字。
func TestListActivity(t *testing.T) {
	bin := fakeListBinary(t, `[{"id":"a","tmux_session":"agentdeck_a","status":"running"},{"id":"b","tmux_session":"agentdeck_b","status":"waiting"},{"id":"c","status":"idle"}]`)
	m, err := listActivity(bin, "deck")(context.Background())
	if err != nil || m["agentdeck_a"] != "running" || m["agentdeck_b"] != "waiting" || len(m) != 2 {
		t.Fatalf("按 tmux 会话名记下状态：%v %v", m, err)
	}
	c := newActivityCache(listActivity(bin, "deck"))
	c.refresh(context.Background())
	if c.of("agentdeck_a") != "running" || c.of("agentdeck_x") != "unknown" {
		t.Fatal("缓存")
	}
	empty := fakeListBinary(t, "No sessions found in profile 'deck'.\n")
	m, err = listActivity(empty, "deck")(context.Background())
	if err != nil || len(m) != 0 {
		t.Fatalf("没有会话：空表、不报错：%v %v", m, err)
	}
}

func TestComposerReadyDraftAndCodexStartup(t *testing.T) {
	draft := strings.Replace(claudeComposerScreen, "❯\u00a0", "❯ 先看一下 README", 1)
	if ComposerReady("claude", draft) {
		t.Fatal("输入框里有 owner 的草稿：不算就绪")
	}
	if ComposerReady("codex", "› Waiting for startup\n\n  ? for shortcuts") {
		t.Fatal("Codex 启动中的占位不算就绪")
	}
}

// attempt 1 落盘的旧状态只有 AwaitingOwner、没有 Starting：重启后补上启动期、接着等，处理完照样送。
func TestResumeLegacyAwaiting(t *testing.T) {
	w := newAgentWorld(t)
	addGamma(t, w)
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	w.srv.WithBackground(dead)
	w.up.setOutput(claudeTrustScreen)
	if code, b := w.do("owner", "POST", "/v1/projects/gamma/main", `{}`); code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	w.srv.WaitBackground()
	x := mainOf(t, w, "gamma")
	w.store.FinishStart(x.Key)
	w.store.SetAwaitingOwner(x.Key, "CLI 停在目录信任确认上（旧版本写的）") // 只有 AwaitingOwner
	before := w.up.sendCount()
	w.up.setOutput(claudeComposerScreen)
	w.srv.WithBackground(context.Background())
	w.srv.ResumeAwaiting()
	waitUntil(t, "补上启动期后接着等、送一次", func() bool { return w.up.sendCount() == before+1 })
	waitUntil(t, "记为已确认", func() bool { return mainOf(t, w, "gamma").BriefConfirmed })
}
