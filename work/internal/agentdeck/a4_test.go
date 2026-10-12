package agentdeck

import "testing"

// n051 评审 3 B1：owner 声明的额外参数按参数语义核对；评审给的反例声明不进来，测试脚本与常见的安全声明照收。
func TestDeclaredExtraArgsBySemantics(t *testing.T) {
	refuse := [][]string{
		{"-c", `approval_policy = "never"`},
		{"-c", `sandbox_mode = "danger-full-access"`},
		{"--config", `profile = "yolo"`},
		{"-anever"},
		{"-sdanger-full-access"},
		{"--permission-mode", "auto"},
		{"--permission-mode=dontAsk"},
		{"--settings", `{"permissions":{"defaultMode":"bypassPermissions"}}`},
		{"--settings=/tmp/s.json"},
		{"--profile", "dev"},
		{"-y"},
		{"--approvalMode", "yolo"},
		{"--approval-mode", "yolo"},
		{"-a", "never"},
		{"-s", "danger-full-access"},
		{"--sandbox"}, // 缺值
		{"-c"},
		{"--dangerously-skip-permissions"},
		{"--no-dangerous-thing"},
	}
	for _, g := range refuse {
		if CheckExtraArgs([][]string{g}) == nil {
			t.Errorf("该拒绝 %q", g)
		}
	}
	allow := [][]string{
		{"--allowedTools", "Bash(deck agent:*),Bash(/x/bin/deck agent:*),Write,Read"}, // 测试脚本
		{"-c", `shell_environment_policy.set.XDG_STATE_HOME="/x/state"`},              // 测试脚本
		{"-c", "sandbox_workspace_write.network_access=true"},                         // 测试脚本（owner 批准）
		{"--allowedTools", "Bash(aws s3 ls --profile=dev:*)"},                         // 自由文本里的词
		{"--append-system-prompt", "Use profile=dev for AWS"},
		{"--permission-mode", "plan"},
		{"-a", "on-request"},
		{"--sandbox=workspace-write"},
		{"-c", `approval_policy = "on-request"`},
	}
	for _, g := range allow {
		if err := CheckExtraArgs([][]string{g}); err != nil {
			t.Errorf("不该拒绝 %q：%v", g, err)
		}
	}
}
