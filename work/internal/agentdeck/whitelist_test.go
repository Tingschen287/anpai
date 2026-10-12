package agentdeck

import "testing"

// n051 评审 2 R1（owner 选方案 A）：涉及权限或沙箱的设置按白名单查，看不懂的写法一律拒绝。
func TestSensitiveSettingsWhitelist(t *testing.T) {
	refuse := []string{
		`codex -c 'approval_policy = "\u006eever"'`,           // 评审 2 的反例
		`codex -c 'sandbox_mode = "danger-full-\u0061ccess"'`, // 评审 2 的反例
		`codex -c "approv\u0061l_policy=never"`,               // 转义改写键名
		`codex -c 'approval_policy="\x6eever"'`,               // TOML 1.1 的 \x
		`codex -c approval_policy=never`,
		`codex -c approval_policy=NEVER`,
		`codex -c approval_policy='never'`,
		`codex -c 'approval_policy="""never"""'`, // 多行字符串：看不懂
		`codex -c approval_policy=`,              // 空值
		`codex -c 'approval_policy="on-request'`, // 引号不成对
		`codex -c profile=fast`,                  // 选 profile：看不到 profile 里写了什么
		`codex -c profiles.fast.approval_policy=never`,
		`codex -c sandbox_workspace_write.writable_roots=["/"]`, // 认不出的沙箱键
		`codex --ask-for-approval=never`,
		`codex -a "never"`,
		`claude --permission-mode=bypassPermissions`,
		`claude --dangerously-skip-permissions`,
	}
	for _, s := range refuse {
		if CheckNoBypass(s) == nil {
			t.Errorf("该拒绝：%s", s)
		}
	}
	allow := []string{
		`codex -c approval_policy=on-request`,
		`codex -c 'sandbox_mode="workspace-write"'`,
		`codex -c "approval_policy = \"untrusted\""`,
		`codex -c sandbox_workspace_write.network_access=true`,
		`codex -c shell_environment_policy.set.XDG_STATE_HOME=/tmp/x/state`,
		`codex --ask-for-approval=untrusted -s workspace-write`,
		`claude --permission-mode=plan --model claude-haiku-5-5`,
		`修复 approval 页面的文案`, // 标题里出现敏感词但不是赋值
	}
	for _, s := range allow {
		if err := CheckNoBypass(s); err != nil {
			t.Errorf("不该拒绝 %s：%v", s, err)
		}
	}
}

func TestUpstreamConfigWhitelist(t *testing.T) {
	refuse := map[string]string{
		"profile 下的禁止值": "[profiles.fast]\napproval_policy = \"never\"\n",
		"转义":            "[codex]\nextra_args = [\"-a\", \"\\u006eever\"]\n",
		"多行字符串":         "approval_policy = '''\nnever'''\n",
		"认不出的沙箱键":       "[codex]\nsandbox_permissions = [\"disk-full-read-access\"]\n",
		"yolo":          "[codex]\nyolo_mode = true\n",
	}
	for name, cfg := range refuse {
		if checkConfigText("codex", cfg) == nil {
			t.Errorf("%s：该拒绝：%q", name, cfg)
		}
	}
	allow := "[codex]\nyolo_mode = false\n\n[tools.mine]\ncommand = \"mine\"\ndangerous_flag = \"--auto\"\ndangerous_mode = false\n" +
		"\n[claude]\ndangerous_mode = false\n"
	for _, tool := range []string{"codex", "claude"} {
		if err := checkConfigText(tool, allow); err != nil {
			t.Errorf("%s 不该拒绝：%v", tool, err)
		}
	}
}
