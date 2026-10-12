package agentdeck

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// n051 attempt 3 第二轮自查的修复。

func TestSendIfIdleArgsAndBusy(t *testing.T) {
	c, argv := fakeSendUpstream(t, `{"success":false,"error":"defer-if-busy: target still busy (running) after 1s"}`, 1)
	if err := c.SendIfIdle(t.Context(), permit(t, "s1"), "s1", "再做一件"); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("还在跑：ErrTargetBusy（什么都没送）：%v", err)
	}
	got, _ := os.ReadFile(argv)
	want := "--defer-if-busy\n--defer-timeout\n" + SubIdleCheck + "\n--timeout\n" + SubReadyTimeout
	if !strings.Contains(string(got), want) {
		t.Fatalf("参数不对：%s", got)
	}
}

// 上游没在时限内返回、被结束：可能已经打进去了，按送达未确认（调用方不撤回代次）。
func TestSendIfIdleKilledIsUnconfirmed(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "agent-deck")
	os.WriteFile(bin, []byte("#!/bin/sh\ncat > /dev/null\nsleep 5\n"), 0o755)
	c, err := NewClient(bin, "_t", "", "")
	if err != nil {
		t.Fatal(err)
	}
	c.Timeout = 300 * time.Millisecond
	if err := c.SendIfIdle(t.Context(), permit(t, "s1"), "s1", "x"); !IsDeliveryUnconfirmed(err) {
		t.Fatalf("被结束：送达未确认：%v", err)
	}
}

func TestAllowlistSelfCheckFixes(t *testing.T) {
	claude := LaunchArgs{Tool: "claude", Binary: "claude"}
	if err := CheckCLIArgs([]string{"--resume", "--dangerously-skip-permissions"}, claude); err == nil {
		t.Error("值的位置上放参数：拒绝")
	}
	if err := CheckCLIArgs([]string{"--session-id", "u", "--chrome", "--teammate-mode", "tmux", "--channels", "plugin:x"}, claude); err != nil {
		t.Errorf("上游按 use_chrome 等加的参数不误拒：%v", err)
	}
	if _, ok := cliArgs([]string{"/home/u/.local/bin/claude", "daemon", "run", "--origin", "transient"}, claude); ok {
		t.Error("Claude Code 自己的后台进程不是这次启动的会话")
	}
	for _, g := range [][]string{
		{"--allowedTools", "Bash(aws s3 ls --profile=dev:*)"},
		{"--append-system-prompt", "Use profile=dev for AWS"},
		{"-c", `shell_environment_policy.set.XDG_STATE_HOME="/x"`},
	} {
		if err := CheckExtraArgs([][]string{g}); err != nil {
			t.Errorf("自由文本里的词不误拒 %q：%v", g, err)
		}
	}
	for _, g := range [][]string{
		{"-c", "approval_policy=never"},
		{"--permission-mode", "bypassPermissions"},
		{"--append-system-prompt", "x \\u006e y"},
		{"--yolo"},
	} {
		if CheckExtraArgs([][]string{g}) == nil {
			t.Errorf("该拒绝 %q", g)
		}
	}
}
