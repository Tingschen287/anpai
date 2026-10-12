package agentdeck

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/lease"
)

// n051：转告主 agent（--defer-if-busy）与绕过参数的硬拒绝。

// fakeSendUpstream 写一个假 agent-deck：记下参数，按 reply 回执与退出码回应。
func fakeSendUpstream(t *testing.T, reply string, code int) (*Client, string) {
	t.Helper()
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv")
	bin := filepath.Join(dir, "agent-deck")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argv + "'\ncat > /dev/null\nprintf '%s' '" + reply + "'\nexit " + string(rune('0'+code)) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(bin, "_t", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return c, argv
}

func permit(t *testing.T, session string) lease.Permit {
	t.Helper()
	l, err := lease.Open(filepath.Join(t.TempDir(), "leases.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	if err := l.Grant(session, "测试"); err != nil {
		t.Fatal(err)
	}
	p, err := l.Automatic(session)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSendToMainDefersAndReportsBusy(t *testing.T) {
	c, argv := fakeSendUpstream(t, `{"success":false,"error":"defer-if-busy: target still busy (running) after 10s"}`, 1)
	err := c.SendToMain(t.Context(), permit(t, "m1"), "m1", "子 agent 完成了")
	if !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("对方还忙应报 ErrTargetBusy（什么都没送，稍后再试）：%v", err)
	}
	got, _ := os.ReadFile(argv)
	args := strings.Split(strings.TrimSpace(string(got)), "\n")
	want := []string{"session", "send", "m1", "--message-file", "-", "--json", "--defer-if-busy", "--defer-timeout", MainDeferTimeout}
	if strings.Join(args[len(args)-len(want):], " ") != strings.Join(want, " ") {
		t.Fatalf("参数不对：%v", args)
	}
}

func TestSendToMainDelivered(t *testing.T) {
	c, _ := fakeSendUpstream(t, `{"success":true,"submitted":true}`, 0)
	if err := c.SendToMain(t.Context(), permit(t, "m1"), "m1", "x"); err != nil {
		t.Fatalf("送达应成功：%v", err)
	}
	c, _ = fakeSendUpstream(t, `{"success":false,"delivery":"typed","error":"no evidence"}`, 1)
	if err := c.SendToMain(t.Context(), permit(t, "m1"), "m1", "x"); !IsDeliveryUnconfirmed(err) {
		t.Fatalf("typed 仍是送达未确认：%v", err)
	}
}

// 普通投递不带 --defer-if-busy：忙的报错不当成「没送」。
func TestSendMessageDoesNotTreatBusyAsDeferred(t *testing.T) {
	c, argv := fakeSendUpstream(t, `{"success":false,"error":"defer-if-busy: target still busy"}`, 1)
	if err := c.SendMessage(t.Context(), permit(t, "s1"), "s1", "x"); errors.Is(err, ErrTargetBusy) || err == nil {
		t.Fatalf("普通投递失败应如实报失败：%v", err)
	}
	got, _ := os.ReadFile(argv)
	if strings.Contains(string(got), "--defer-if-busy") {
		t.Fatal("普通投递不应带 --defer-if-busy")
	}
}

func TestSendToMainRefusesZeroPermit(t *testing.T) {
	c, argv := fakeSendUpstream(t, `{"success":true,"submitted":true}`, 0)
	if err := c.SendToMain(t.Context(), lease.Permit{}, "m1", "x"); err == nil {
		t.Fatal("零值许可必须被拒")
	}
	if _, err := os.Stat(argv); !os.IsNotExist(err) {
		t.Fatal("零值许可不应调上游")
	}
}

func TestCheckNoBypass(t *testing.T) {
	for _, bad := range []string{
		"claude --dangerously-skip-permissions", "--DANGEROUSLY-SKIP-PERMISSIONS", "codex --dangerously-bypass-approvals-and-sandbox",
		"gemini --yolo", "--yolo=true", "claude --permission-mode bypassPermissions", "--permission-mode=bypassPermissions",
		"codex -a never", "codex --ask-for-approval=never", "codex --sandbox danger-full-access", "gemini --approval-mode yolo",
		// n051 评审 1 B1：带空格、带引号的 codex -c 完整赋值
		`codex -c "approval_policy = \"never\""`, `approval_policy = "never"`, `sandbox_mode = 'danger-full-access'`, "-c APPROVAL_POLICY=NEVER",
	} {
		if err := CheckNoBypass("claude", bad); err == nil {
			t.Errorf("应拒绝 %q", bad)
		}
	}
	for _, ok := range []string{"claude", "claude-opus-5-5", "codex -a on-request", "--permission-mode plan", "--sandbox workspace-write", "主 agent：整理 yolo 文档",
		`codex -c "approval_policy = \"on-request\""`, `sandbox_mode = "workspace-write"`} {
		if err := CheckNoBypass(ok); err != nil {
			t.Errorf("不该拒绝 %q：%v", ok, err)
		}
	}
}
