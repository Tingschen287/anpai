package agentdeck

import (
	"strings"
	"testing"
)

// n051 attempt 3（owner 选 R1-A）：按进程的实际命令行认 CLI、按参数白名单核对（端到端里的几种形态）。
func TestCLIArgsAllowlist(t *testing.T) {
	codexExtra := [][]string{{"-c", `shell_environment_policy.set.XDG_STATE_HOME="/x/state"`}, {"-c", "sandbox_workspace_write.network_access=true"}}
	codex := LaunchArgs{Tool: "codex", Binary: "codex", Extra: codexExtra}
	claude := LaunchArgs{Tool: "claude", Binary: "claude", Extra: [][]string{{"--allowedTools", "Read,Write"}}}
	cases := []struct {
		name  string
		argv  string // 用 | 分隔
		want  LaunchArgs
		isCLI bool
		ok    bool
	}{
		{"bash -c 不是 CLI", "bash|-c|exec claude --session-id u", claude, false, true},
		{"env 不是 CLI", "/usr/bin/env|node|/x/bin/codex|--yolo", codex, false, true}, // 只查明确的绕过参数：--yolo 在其中
		{"包装脚本（bash 跑名叫 claude 的脚本）", "/bin/bash|/t/bin/claude|--session-id|u-1|--model|m", claude, true, true},
		{"原生 claude 带声明的参数", "/home/u/.local/bin/claude|--allowedTools|Read,Write|--session-id|u-1|--model|m", claude, true, true},
		{"声明的参数取值不同", "/home/u/.local/bin/claude|--allowedTools|Bash(*)|--session-id|u-1", claude, true, false},
		{"claude --resume 与 --add-dir", "claude|--resume|abc|--add-dir|/p|--model=m|--effort|high", claude, true, true},
		{"claude 跳过权限", "claude|--session-id|u|--dangerously-skip-permissions", claude, true, false},
		{"claude 允许跳过", "claude|--session-id|u|--allow-dangerously-skip-permissions", claude, true, false},
		{"claude auto 模式", "claude|--permission-mode|auto", claude, true, false},
		{"claude --settings", "claude|--settings|{\"permissions\":{\"defaultMode\":\"bypassPermissions\"}}", claude, true, false},
		{"claude -p", "claude|-p|hi", claude, true, false},
		{"node 跑 codex.js", "node|/x/lib/codex/bin/codex.js|-c|shell_environment_policy.set.XDG_STATE_HOME=\"/x/state\"|-c|sandbox_workspace_write.network_access=true|--model|gpt", codex, true, true},
		{"原生 codex resume", "/x/vendor/bin/codex|--model|gpt|--config|model_reasoning_effort=high|resume|sid", codex, true, true},
		{"codex 其他 --config", "codex|--config|approval_policy=on-request", codex, true, false},
		{"codex 短参数连写", "codex|-anever", codex, true, false},
		{"codex --profile", "codex|--profile|yolo", codex, true, false},
		{"codex --yolo", "codex|--yolo|--model|gpt", codex, true, false},
		{"codex 位置参数提示词", "codex|--model|gpt|做点什么", codex, true, false},
		{"已知参数缺值", "codex|--model", codex, true, false},
		{"gemini -y", "gemini|-y", LaunchArgs{Tool: "gemini"}, true, false},
		{"gemini --resume", "gemini|--resume|s|--model|g", LaunchArgs{Tool: "gemini"}, true, true},
		{"自定义工具只放行声明的", "kimi|--x", LaunchArgs{Tool: "kimi", Extra: [][]string{{"--x"}}}, true, true},
	}
	for _, c := range cases {
		argv := strings.Split(c.argv, "|")
		args, isCLI := cliArgs(argv, c.want)
		if isCLI != c.isCLI {
			t.Errorf("%s：认作 CLI=%v，期望 %v", c.name, isCLI, c.isCLI)
			continue
		}
		var err error
		if isCLI {
			err = CheckCLIArgs(args, c.want)
		} else {
			err = checkBypassFlags(append(argv, strings.Join(argv, " "))...)
			if c.name == "env 不是 CLI" {
				if err == nil {
					t.Errorf("%s：明确的绕过参数应拒绝", c.name)
				}
				continue
			}
		}
		if (err == nil) != c.ok {
			t.Errorf("%s：通过=%v，期望 %v（%v）", c.name, err == nil, c.ok, err)
		}
	}
}
