package agentdeck

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeLaunch 起一个假 agent-deck：把收到的参数逐行记下，回一段成功的 launch JSON。
func fakeLaunch(t *testing.T) (*Client, func() []string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "argv")
	bin := filepath.Join(dir, "agent-deck")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> " + log + "; done\n" +
		`printf '{"success":true,"session_id":"ad-1","tool":"x"}'` + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(bin, "deck", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return c, func() []string {
		b, _ := os.ReadFile(log)
		return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	}
}

// n055：owner 声明的额外参数随启动传上去（n051 只当白名单用、从没传过）：claude 走 --extra-arg，每个元素一个；
// codex 走 -c "codex 参数…"（逐个 shell 转义）。
func TestLaunchPassesDeclaredExtraArgs(t *testing.T) {
	c, argv := fakeLaunch(t)
	_, err := c.Launch(context.Background(), LaunchRequest{ProjectPath: "/tmp/p", Tool: "claude", Model: "claude-opus-5-5", NoWait: true,
		Extra: [][]string{{"--allowedTools", "Bash(deck agent:*),Bash(/x/deck agent:*)"}, {"--append-system-prompt=简短"}}})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(argv(), "|")
	want := "launch|/tmp/p|-c|claude|--json|--extra-arg|--allowedTools|--extra-arg|Bash(deck agent:*),Bash(/x/deck agent:*)|--extra-arg|--append-system-prompt=简短|--model|claude-opus-5-5|--no-wait"
	if got != want {
		t.Fatalf("claude：\n%s\n%s", got, want)
	}

	c, argv = fakeLaunch(t)
	_, err = c.Launch(context.Background(), LaunchRequest{ProjectPath: "/tmp/p", Tool: "codex", Model: "gpt-6.1-sol",
		Extra: [][]string{{"-c", "sandbox_workspace_write.network_access=true"}, {"-c", `shell_environment_policy.set.XDG_STATE_HOME="/tmp/it's"`}}})
	if err != nil {
		t.Fatal(err)
	}
	got = strings.Join(argv(), "|")
	want = `launch|/tmp/p|-c|codex '-c' 'sandbox_workspace_write.network_access=true' '-c' 'shell_environment_policy.set.XDG_STATE_HOME="/tmp/it'\''s"'|--json|--model|gpt-6.1-sol`
	if got != want {
		t.Fatalf("codex：\n%s\n%s", got, want)
	}

	// 没有声明：与 n051 完全一样
	c, argv = fakeLaunch(t)
	if _, err := c.Launch(context.Background(), LaunchRequest{ProjectPath: "/tmp/p", Tool: "codex", Model: "gpt-6.1-sol"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(argv(), "|"); got != "launch|/tmp/p|-c|codex|--json|--model|gpt-6.1-sol" {
		t.Fatalf("没有声明：%s", got)
	}
}

// 白名单外的声明在启动请求里也拒（纵深防御：deckd 开会话前已查过一次）；其他工具不许声明；
// claude 的「参数=取值」带空白时上游拒收，deck 先拦下并说明。
func TestLaunchRefusesUndeclarableExtra(t *testing.T) {
	cases := []LaunchRequest{
		{ProjectPath: "/p", Tool: "claude", Model: "m", Extra: [][]string{{"--permission-mode", "auto"}}},
		{ProjectPath: "/p", Tool: "codex", Model: "m", Extra: [][]string{{"-c", "approval_policy=never"}}},
		{ProjectPath: "/p", Tool: "gemini", Model: "m", Extra: [][]string{{"--model", "x"}}},
		{ProjectPath: "/p", Tool: "claude", Model: "m", Extra: [][]string{{"--allowedTools=Bash(deck agent:*)"}}},
		{ProjectPath: "/p", Tool: "codex", Model: "m", Extra: [][]string{{"-c", "shell_environment_policy.set.X={command}"}}},
	}
	for i, r := range cases {
		if err := r.Validate(); err == nil {
			t.Errorf("第 %d 个应拒绝：%+v", i, r.Extra)
		}
	}
	if err := (LaunchRequest{ProjectPath: "/p", Tool: "claude", Model: "m", Extra: [][]string{{"--allowedTools=Read,Write"}}}).Validate(); err != nil {
		t.Fatalf("不带空白的 参数=取值 照常：%v", err)
	}
}
