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

// n051 attempt 3（owner 选 R1-A）：启动前的配置检查只看所启动工具相关的几节——command 只能是裸命令、extra_args 为空、
// 不套 wrapper、不开 exit_to_shell；其余键 agent-deck 不会传给 CLI，不再按关键词扫（attempt 3 自查：误拒 AWS_PROFILE 等）。
// 真正的保证在启动后按参数白名单核对（TestSessionProcessesBypass）。
func TestUpstreamConfigWhitelist(t *testing.T) {
	safe := "[claude]\ndangerous_mode = false\n"
	refuse := []struct{ name, tool, cfg string }{
		{"短参数连写", "codex", "[codex]\ncommand = \"codex -anever -sdanger-full-access\"\n"},
		{"空格写法", "claude", safe + "command = \"claude --permission-mode auto\"\n"},
		{"settings 文件", "claude", safe + "[conductors.c.claude]\ncommand = \"claude --settings /tmp/yolo.json\"\n"},
		{"分组 command 带参数", "claude", safe + "[groups.\"a/b\".claude]\ncommand = \"claude --add-dir /\"\n"},
		{"profile 下的 command", "codex", "[profiles.p.codex]\ncommand = \"codex --profile yolo\"\n"},
		{"自定义工具 command 带参数", "gemini", "[tools.gemini]\ncommand = \"gemini -y\"\n"},
		{"extra_args 非空", "codex", "[codex]\nextra_args = [\"-a\", \"\\u006eever\"]\n"},
		{"多行 extra_args", "codex", "[codex]\nextra_args = [\n\"--yolo\"\n]\n"},
		{"wrapper", "claude", safe + "[tools.claude]\nwrapper = \"bash -c '{command}'\"\n"},
		{"exit_to_shell", "claude", safe + "[shell]\nexit_to_shell = true\n"},
		{"command 不带引号", "codex", "[codex]\ncommand = codex\n"},
		{"yolo", "codex", "[codex]\nyolo_mode = true\n"},
	}
	for _, c := range refuse {
		if checkConfigText(c.tool, c.cfg) == nil {
			t.Errorf("%s：该拒绝：%q", c.name, c.cfg)
		}
	}
	allow := safe + "[codex]\nyolo_mode = false\ncommand = \"/opt/bin/codex\"\n" +
		"[tools.mine]\ncommand = \"mine --x\"\ndangerous_flag = \"--auto\"\ndangerous_mode = false\n" + // 别的工具的 command 不管
		"[mcps.aws]\nenv = { AWS_PROFILE = \"default\" }\n[remotes.box]\nprofile = \"default\"\n" + // 与权限无关的配置不误拒
		"[profiles.fast]\napproval_policy = \"never\"\n" + // agent-deck 不读这个键，不会传给 CLI
		"[shell]\nexit_to_shell = false\n"
	for _, tool := range []string{"codex", "claude"} {
		if err := checkConfigText(tool, allow); err != nil {
			t.Errorf("%s 不该拒绝：%v", tool, err)
		}
	}
}
