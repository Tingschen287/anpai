package agentdeck

import "testing"

// n051 attempt 4 设计复审（owner 选 B1-A）：owner 声明的额外参数也走白名单——每个工具只认固定几种参数，其余一律拒绝。
// 评审 3 B1 与 attempt 4 自查的反例都声明不进来；测试脚本声明的几组与常见的安全声明照收。
func TestDeclaredExtraArgsWhitelist(t *testing.T) {
	refuse := []struct {
		tool string
		g    []string
	}{
		// 评审 3 B1
		{"codex", []string{"-c", `approval_policy = "never"`}},
		{"codex", []string{"-c", `sandbox_mode = "danger-full-access"`}},
		{"codex", []string{"-anever"}},
		{"codex", []string{"-sdanger-full-access"}},
		{"claude", []string{"--permission-mode", "auto"}},
		{"claude", []string{"--settings", `{"permissions":{"defaultMode":"bypassPermissions"}}`}},
		{"gemini", []string{"-y"}},
		// attempt 4 自查
		{"claude", []string{"--inherit-permission-mode", "bypassPermissions"}},
		{"claude", []string{"--inherit-permission-mode=auto"}},
		{"codex", []string{"--approve-for-me"}},
		{"claude", []string{"--agents", `{"w":{"permissionMode":"auto"}}`}},
		{"claude", []string{"--plugin-dir", "/x/plugin"}},
		{"codex", []string{"--add-dir", "/"}},
		{"codex", []string{"-C", "/"}},
		{"codex", []string{"-c", "approvals_reviewer=auto_review"}},
		{"codex", []string{"-c", "profile=yolo"}},
		{"codex", []string{"-c", "sandbox_workspace_write.writable_roots=[\"/\"]"}},
		{"codex", []string{"-c", "sandbox_workspace_write.network_access=yes"}},
		{"codex", []string{"-c", "shell_environment_policy.set.X=\"\\u0041\""}},
		// 形状不对
		{"claude", []string{"--allowedTools"}},
		{"claude", []string{"--allowedTools", "--dangerously-skip-permissions"}},
		{"claude", []string{"--allowedTools", "Read", "--yolo"}},
		{"claude", []string{}},
		{"claude", []string{"-c", "shell_environment_policy.set.X=1"}}, // claude 不认 -c
		{"gemini", []string{"--allowedTools", "Read"}},                 // gemini 不允许声明
		{"kimi", []string{"--x", "1"}},
	}
	for _, c := range refuse {
		if CheckExtraArgs(c.tool, [][]string{c.g}) == nil {
			t.Errorf("%s 该拒绝 %q", c.tool, c.g)
		}
	}
	allow := []struct {
		tool string
		g    []string
	}{
		{"claude", []string{"--allowedTools", "Bash(deck agent:*),Bash(/x/bin/deck agent:*),Write,Read"}}, // 测试脚本
		{"codex", []string{"-c", `shell_environment_policy.set.XDG_STATE_HOME="/x/state"`}},               // 测试脚本
		{"codex", []string{"-c", `shell_environment_policy.set.XDG_CONFIG_HOME="/x/cfg"`}},                // 测试脚本
		{"codex", []string{"-c", "sandbox_workspace_write.network_access=true"}},                          // 测试脚本（owner 批准）
		{"claude", []string{"--allowedTools", "Bash(aws s3 ls --profile=dev:*)"}},                         // 自由文本里的词
		{"claude", []string{"--append-system-prompt", "Use profile=dev for AWS"}},
		{"claude", []string{"--disallowedTools=Bash(rm:*)"}},
	}
	for _, c := range allow {
		if err := CheckExtraArgs(c.tool, [][]string{c.g}); err != nil {
			t.Errorf("%s 不该拒绝 %q：%v", c.tool, c.g, err)
		}
	}
}
