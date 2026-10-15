package agentdeck

import (
	"context"
	"strings"
	"testing"
)

// n057：grok、antigravity 在上游配置里登记成自定义工具，上游 launch 不接受它们的 --model，模型经 -c "<工具> --model <m>" 传；
// 启动后的参数白名单认它们的 --model；grok 的 --always-approve 算跳过权限；读上游配置确认自定义工具不是 Codex 兼容的。

func TestLaunchPassesModelViaCommandForCustomTools(t *testing.T) {
	for _, c := range []struct{ tool, model, want string }{
		{"grok", "grok-4.7-build-fast", "launch|/tmp/p|-c|grok --model 'grok-4.7-build-fast'|--json|--no-wait"},
		{"antigravity", "it's-odd", `launch|/tmp/p|-c|antigravity --model 'it'\''s-odd'|--json|--no-wait`},
	} {
		cl, argv := fakeLaunch(t)
		if _, err := cl.Launch(context.Background(), LaunchRequest{ProjectPath: "/tmp/p", Tool: c.tool, Model: c.model, NoWait: true}); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(argv(), "|"); got != c.want {
			t.Fatalf("%s：\n%s\n%s", c.tool, got, c.want)
		}
	}
	// 内置工具照旧用 --model
	cl, argv := fakeLaunch(t)
	cl.Launch(context.Background(), LaunchRequest{ProjectPath: "/tmp/p", Tool: "opencode", Model: "opencode/x"})
	if got := strings.Join(argv(), "|"); got != "launch|/tmp/p|-c|opencode|--json|--model|opencode/x" {
		t.Fatalf("opencode：%s", got)
	}
	if err := CheckToolModel("grok", true); err == nil || !strings.Contains(err.Error(), "替你挑默认模型") {
		t.Fatalf("grok 有模型可选：不能声明 model_not_applicable：%v", err)
	}
}

func TestArgAllowlistForCustomTools(t *testing.T) {
	ok := [][]string{{"--model", "grok-4.7"}, {"--model=grok-4.5"}}
	for _, a := range ok {
		if err := CheckCLIArgs(a, LaunchArgs{Tool: "grok", Binary: "grok"}); err != nil {
			t.Errorf("grok %v 应放行：%v", a, err)
		}
		if err := CheckCLIArgs(a, LaunchArgs{Tool: "antigravity", Binary: "agy"}); err != nil {
			t.Errorf("agy %v 应放行：%v", a, err)
		}
	}
	for _, a := range [][]string{{"--always-approve"}, {"--yolo"}, {"--permission-mode", "bypassPermissions"}, {"--dangerously-skip-permissions"}, {"--model", "--yolo"}} {
		if err := CheckCLIArgs(a, LaunchArgs{Tool: "grok", Binary: "grok"}); err == nil {
			t.Errorf("grok %v 应拒绝", a)
		}
	}
	if args, ok := cliArgs([]string{"/home/u/.local/bin/agy", "--model", "x"}, LaunchArgs{Tool: "antigravity", Binary: "agy"}); !ok || len(args) != 2 {
		t.Fatal("按 binary 的文件名认出 agy 进程")
	}
	found := false
	for _, f := range BypassFlags {
		found = found || f == "--always-approve"
	}
	if !found {
		t.Fatal("grok 的 --always-approve 是跳过权限的参数")
	}
}

func TestCustomToolNotCodex(t *testing.T) {
	cases := []struct {
		name, text string
		want       bool
	}{
		{"登记过、命令不是 codex", "[claude]\ndangerous_mode = false\n\n[tools.grok]\ncommand = \"grok\"\nicon = \"X\"\n", true},
		{"带路径", "[tools.grok]\ncommand = '/home/u/.local/bin/grok'  # 注释\n", true},
		{"没登记", "[claude]\ndangerous_mode = false\n", false},
		{"声明 codex 兼容", "[tools.grok]\ncommand = \"grok\"\ncompatible_with = \"codex\"\n", false},
		{"命令就是 codex", "[tools.grok]\ncommand = \"/usr/bin/codex\"\n", false},
		{"命令带参数", "[tools.grok]\ncommand = \"grok --x\"\n", false},
		{"没写命令", "[tools.grok]\nicon = \"X\"\n", false},
		{"别的节改了它", "[tools.grok]\ncommand = \"grok\"\n[profiles.work.grok]\ncompatible_with = \"codex\"\n", false},
		{"顶层点号写法", "tools.grok.command = \"grok\"\n", false},
		{"看不懂的行", "[tools.grok]\ncommand = \"grok\"\nenv = {\n", false},
	}
	for _, c := range cases {
		if got := customToolNotCodex("grok", c.text); got != c.want {
			t.Errorf("%s：%v（期望 %v）", c.name, got, c.want)
		}
	}
	for _, tool := range []string{"codex", "claude", ""} {
		if CustomToolNotCodex(tool) {
			t.Errorf("%q 不是自定义工具，不走这条", tool)
		}
	}
}
