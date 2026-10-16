package agentdeck

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// n051 真模型端到端实测：agent-deck v1.15 的 [claude] dangerous_mode 不写时默认开启。
func TestUpstreamConfigBypass(t *testing.T) {
	safe := "[claude]\ndangerous_mode = false\n"
	cases := []struct {
		name, tool, text string
		ok               bool
	}{
		{"没有配置文件：上游默认开启", "claude", "", false},
		{"显式关闭", "claude", safe, true},
		{"显式开启", "claude", "[claude]\ndangerous_mode = true\n", false},
		{"没写在 [claude] 下", "claude", "[codex]\ndangerous_mode = false\n", false},
		{"auto_mode", "claude", safe + "auto_mode = true\n", false},
		{"allow_dangerous_mode", "claude", safe + "allow_dangerous_mode = true\n", false},
		{"extra_args 带绕过取值", "claude", safe + `extra_args = ["--permission-mode", "bypassPermissions"]` + "\n", false},
		{"分组的 command 带绕过参数", "claude", safe + "[groups.\"x\".claude]\ncommand = \"claude --dangerously-skip-permissions\"\n", false},
		{"注释里的绕过参数不算", "claude", "# dangerous_flag = \"--dangerously-skip-permissions\"\n" + safe + "# dangerous_mode = true\n", true},
		{"行尾注释", "claude", "[claude]\ndangerous_mode = false # 关掉\n", true},
		{"引号里的 # 不是注释", "claude", safe + "command = \"claude#--yolo\"\n", true},
		{"点号写法读不懂", "claude", "claude.dangerous_mode = false\n", false},
		{"内联表读不懂", "claude", "claude = { dangerous_mode = false }\n", false},
		{"codex 默认不开", "codex", "", true},
		{"codex yolo", "codex", "[codex]\nyolo_mode = true\n", false},
		{"codex 命令带绕过参数", "codex", "[codex]\ncommand = \"codex --dangerously-bypass-approvals-and-sandbox\"\n", false},
		{"codex 的 -c 覆盖", "codex", "[codex]\ncommand = \"codex -c approval_policy=never\"\n", false},
		{"codex 的 --config= 覆盖", "codex", "[codex]\ncommand = \"codex --config=sandbox_mode=danger-full-access\"\n", false},
		// n051 attempt 3（R1-A）：command 只能是裸命令或路径，带任何参数都拒绝（参数由 owner 在 deck 设置里逐字声明）
		{"codex 的其他 -c 覆盖也拒绝", "codex", "[codex]\ncommand = \"codex -c model_reasoning_effort=high\"\n", false},
		{"裸命令路径", "codex", "[codex]\ncommand = \"/opt/bin/codex\"\n", true},
		{"带空格的 codex -c 覆盖", "codex", "[codex]\nextra_args = [\"-c\", \"approval_policy = \\\"never\\\"\"]\n", false},
		{"自定义工具 dangerous_mode", "claude", safe + "[tools.kimi]\ncommand = \"kimi\"\ndangerous_mode = true\n", false},
		{"自定义工具 dangerous_mode = false", "claude", safe + "[tools.kimi]\ncommand = \"kimi\"\ndangerous_mode = false\n", true},
		{"MCP 的 npx -y 不算", "claude", safe + "[mcps.fs]\ncommand = \"npx\"\nargs = [\"-y\", \"@x/fs\"]\n", true},
		{"多行数组把参数名与取值分开", "claude", safe + "extra_args = [\n  \"--permission-mode\",\n  \"bypassPermissions\",\n]\n", false},
	}
	for _, c := range cases {
		err := checkConfigText(c.tool, c.text)
		if (err == nil) != c.ok {
			t.Errorf("%s：期望通过=%v，得到 %v", c.name, c.ok, err)
		}
	}
}

func TestAgentDeckConfigPathOrder(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, "xdg")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	want := filepath.Join(xdg, "agent-deck", "config.toml")
	if p, _ := AgentDeckConfigPath(); p != want {
		t.Fatalf("都没有时返回 XDG 路径：%s", p)
	}
	legacy := filepath.Join(home, ".agent-deck", "config.toml")
	os.MkdirAll(filepath.Dir(legacy), 0o755)
	os.WriteFile(legacy, []byte("[claude]\ndangerous_mode = false\n"), 0o644)
	if p, _ := AgentDeckConfigPath(); p != legacy {
		t.Fatalf("只有旧目录时用旧目录：%s", p)
	}
	if err := CheckUpstreamConfigNoBypass("claude"); err != nil {
		t.Fatalf("旧目录里显式关闭应通过：%v", err)
	}
	os.MkdirAll(filepath.Dir(want), 0o755)
	os.WriteFile(want, []byte(""), 0o644)
	if p, _ := AgentDeckConfigPath(); p != want {
		t.Fatalf("XDG 目录里有就优先用它：%s", p)
	}
	if err := CheckUpstreamConfigNoBypass("claude"); err == nil || !strings.Contains(err.Error(), "dangerous_mode = false") {
		t.Fatalf("XDG 里的空配置（上游默认开启）应拒绝并说明怎么改：%v", err)
	}
}

// 启动后的命令行核对：用真实 tmux（隔离 socket）起一个带绕过参数的会话与一个干净的会话。
func TestSessionProcessesBypass(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("没有 tmux")
	}
	sock := filepath.Join(t.TempDir(), "s")
	run := func(a ...string) {
		if out, err := exec.Command("tmux", append([]string{"-S", sock, "-f", "/dev/null"}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("tmux %v：%v %s", a, err, out)
		}
	}
	t.Cleanup(func() { exec.Command("tmux", "-S", sock, "kill-server").Run() })
	// 假的 CLI：名叫 claude、codex 的 bash 脚本，只是睡着（进程的命令行就是上游会起的样子）
	bin := t.TempDir()
	for _, n := range []string{"claude", "codex"} {
		os.WriteFile(filepath.Join(bin, n), []byte("#!/bin/bash\nsleep 30\n"), 0o755)
	}
	cl, cx := filepath.Join(bin, "claude"), filepath.Join(bin, "codex")
	// 与上游一样经 bash -c 起（n051 attempt 3：R1 方案 A，按参数白名单核对 CLI 进程）
	start := func(name, cmd string) { run("new-session", "-d", "-s", name, "bash", "-c", cmd) }
	start("good", "export AWS_PROFILE=default && "+cl+" --session-id u-1 --model 'claude-haiku-5-5'") // 与权限无关的赋值不误拒
	start("goodmcp", cl+" --session-id u-1 --model m & bash -c 'sleep 30; :' npx -y some-mcp; wait")  // MCP 服务常见的 npx -y 不误拒
	start("dangerous", cl+" --session-id u-1 --dangerously-skip-permissions")
	start("automode", cl+" --session-id u-1 --permission-mode auto")       // 空格写法（attempt 3 自查）
	start("settings", cl+" --session-id u-1 --settings /tmp/yolo.json")    // CLI 设置文件（attempt 3 自查）
	start("attached", cx+" -anever -sdanger-full-access --model gpt")      // 短参数连写（attempt 3 自查）
	start("profile", cx+" --profile yolo --model gpt")                     // --profile 空格写法（attempt 3 自查）
	start("escaped", cx+" -c 'approval_policy=\"\\u006eever\"' --model g") // TOML 转义（评审 2 R1）
	start("extra", cx+" -c sandbox_workspace_write.network_access=true --model g")
	start("codexok", cx+" --model gpt --config model_reasoning_effort=high resume abc")
	start("nocli", "sleep 30")
	start("sidecar", cl+" --session-id u-1 & bash -c 'sleep 30; :' x --dangerously-skip-permissions; wait") // CLI 之外的进程只查明确的绕过参数
	time.Sleep(300 * time.Millisecond)
	ctx := context.Background()
	defer func(d time.Duration) { CLIWait = d }(CLIWait)
	CLIWait = 500 * time.Millisecond
	claude := LaunchArgs{Tool: "claude", Binary: "claude"}
	codex := LaunchArgs{Tool: "codex", Binary: "codex"}
	for _, c := range []struct {
		s    string
		want LaunchArgs
		ok   bool
	}{
		{"good", claude, true}, {"goodmcp", claude, true}, {"codexok", codex, true},
		{"dangerous", claude, false}, {"automode", claude, false}, {"settings", claude, false},
		{"attached", codex, false}, {"profile", codex, false}, {"escaped", codex, false},
		{"extra", codex, false}, // 没声明
		{"extra", LaunchArgs{Tool: "codex", Extra: [][]string{{"-c", "sandbox_workspace_write.network_access=true"}}}, true},
		{"nocli", claude, false},   // 找不到 CLI 进程：核对不了
		{"sidecar", claude, false}, // CLI 干净，旁边的进程带绕过参数
		{"none", claude, false}, {"", claude, false},
	} {
		if err := CheckSessionArgs(ctx, sock, c.s, c.want); (err == nil) != c.ok {
			t.Errorf("%s（%s）：通过=%v，期望 %v（%v）", c.s, c.want.Tool, err == nil, c.ok, err)
		}
	}
	if ok, err := TmuxSessionAlive(ctx, sock, "good"); !ok || err != nil {
		t.Fatalf("在的会话应报在：%v %v", ok, err)
	}
	if ok, err := TmuxSessionAlive(ctx, sock, "none"); ok || err != nil {
		t.Fatalf("不在的会话应报不在：%v %v", ok, err)
	}
}
