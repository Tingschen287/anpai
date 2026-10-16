package agentdeck

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/lease"
)

// 停会话的上游调用恰好一处、在 StopSession 里，且先核许可（n044）。
// 和 TestEveryWriteIntoASessionNeedsAPermit 同一个判法：包里别的函数想停会话，只能经 StopSession。
func TestSessionStopOnlyRunsInStopSessionAfterAPermit(t *testing.T) {
	files, fset := packageASTs(t)
	type site struct {
		fn  string
		pos token.Pos
	}
	var stops []site
	var beginPos token.Pos
	for _, f := range files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if fn.Name.Name == "StopSession" && sel.Sel.Name == "Begin" && beginPos == token.NoPos {
					beginPos = call.Pos()
				}
				var args []ast.Expr
				switch sel.Sel.Name {
				case "exec":
					args = call.Args[1:]
				case "execWithStdin":
					if len(call.Args) > 2 {
						args = call.Args[2:]
					}
				default:
					return true
				}
				if len(args) >= 2 && strLit(args[0]) == "session" && strLit(args[1]) == "stop" {
					stops = append(stops, site{fn.Name.Name, call.Pos()})
				}
				return true
			})
		}
	}
	if len(stops) != 1 || stops[0].fn != "StopSession" {
		var where []string
		for _, s := range stops {
			where = append(where, s.fn+" @ "+fset.Position(s.pos).String())
		}
		t.Fatalf("停会话的上游调用应当恰好一处、在 StopSession 里，实际：%v", where)
	}
	if beginPos == token.NoPos || beginPos > stops[0].pos {
		t.Error("StopSession 必须在执行 session stop 之前核对并登记许可（Permit.Begin）")
	}
	m, ok := reflect.TypeOf(&Client{}).MethodByName("StopSession")
	if !ok || m.Type.NumIn() < 3 || m.Type.In(2) != reflect.TypeOf(lease.Permit{}) {
		t.Error("StopSession 的第二个参数必须是 lease.Permit")
	}
}

// fakeUpstream 写一个记录参数的假上游：每次调用把 argv 追加到 args 文件。
func fakeUpstream(t *testing.T) (bin, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	bin = filepath.Join(dir, "agent-deck")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"$@\" >> \""+argsFile+"\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsFile
}

// 零值许可在调上游之前就被拒；上游一次都不调。
func TestStopSessionRefusesAZeroPermit(t *testing.T) {
	bin, argsFile := fakeUpstream(t)
	c, err := NewClient(bin, "_t", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.StopSession(t.Context(), lease.Permit{}, "s1"); err == nil || !strings.Contains(err.Error(), "没有投递许可") {
		t.Fatalf("零值许可必须被拒：%v", err)
	}
	if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
		t.Error("许可被拒时上游不该被调用")
	}
}

// 有许可就跑 session stop <id> --json，别的参数一个不加。owner 接管着的会话签不出许可，停不了。
func TestStopSessionRunsSessionStopOnlyWithAPermit(t *testing.T) {
	bin, argsFile := fakeUpstream(t)
	c, err := NewClient(bin, "_t", "", "")
	if err != nil {
		t.Fatal(err)
	}
	leases, err := lease.Open(filepath.Join(t.TempDir(), "leases.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(leases.Close)
	if err := leases.Grant("s1", "测试"); err != nil {
		t.Fatal(err)
	}
	p, err := leases.Automatic("s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.StopSession(t.Context(), p, "s1"); err != nil {
		t.Fatalf("有许可应当能停：%v", err)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != "session stop s1 --json" {
		t.Errorf("上游参数应当正好是 session stop s1 --json，实际 %q", got)
	}

	// 手动许可（owner 的回答）不能拿来停会话。
	mp, err := leases.Manual("s1")
	if err != nil {
		t.Fatalf("前提：Work Deck 持有的会话签得出手动许可：%v", err)
	}
	os.Remove(argsFile)
	if err := c.StopSession(t.Context(), mp, "s1"); err == nil || !strings.Contains(err.Error(), "只接受 Work Deck 的自动许可") {
		t.Errorf("手动许可应当被拒：%v", err)
	}
	if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
		t.Error("手动许可被拒时上游不该被调用")
	}

	if err := leases.OwnerTakeover("s1", "测试"); err != nil {
		t.Fatal(err)
	}
	if err := c.StopSession(t.Context(), p, "s1"); err == nil {
		t.Error("owner 接管后，旧许可不该还能停会话")
	}
}

// shell 不接任务说明；配置里的名字与 launch 回执两处都核（n043 复审新问题 1）。
// 自定义工具要在配置里声明 agent: true 才派；launch 后实际命令是 shell 解释器的一律不送（n044 attempt 1 评审 blocker 1）。
func TestToolsThatAreNotAgentsGetNoTaskText(t *testing.T) {
	if err := CheckTakesTaskText("shell", true); err == nil || !strings.Contains(err.Error(), "不是 agent") {
		t.Errorf("shell 声明了也应当被拒：%v", err)
	}
	for _, tool := range []string{"claude", "codex", "gemini", "aider"} {
		if err := CheckTakesTaskText(tool, false); err != nil {
			t.Errorf("上游内置的 agent %s 不该在 launch 前被拒：%v", tool, err)
		}
	}
	if err := CheckTakesTaskText("n044agent", false); err == nil || !strings.Contains(err.Error(), "没声明") {
		t.Errorf("没声明 agent 的自定义工具应当被拒：%v", err)
	}
	if err := CheckTakesTaskText("n044agent", true); err != nil {
		t.Errorf("声明了 agent 的自定义工具不该在 launch 前被拒：%v", err)
	}
	// 自定义工具的命令是 shell 解释器：上游回执写的是自定义名字，但实际命令暴露了它。
	for _, cmd := range []string{"bash", "/bin/sh -l", "env -i FOO=1 zsh", "/usr/bin/env bash"} {
		if err := CheckLaunchedTakesTaskText(LaunchResult{Tool: "n044sh", Command: "n044sh", ResolvedCommand: cmd}); err == nil || !strings.Contains(err.Error(), "shell 解释器") {
			t.Errorf("实际命令 %q 应当被认成 shell：%v", cmd, err)
		}
	}
	if err := CheckLaunchedTakesTaskText(LaunchResult{Tool: "n044sh", Command: "bash"}); err == nil {
		t.Error("回执没给 resolved_command 时按 command 核")
	}
	// 配成 bash 的会话，上游回执写的是 shell（实测 launch -c bash --json 的 tool 字段）。
	if err := CheckLaunchedTakesTaskText(LaunchResult{Tool: "shell", Command: "bash"}); err == nil || !strings.Contains(err.Error(), "bash") {
		t.Errorf("上游起成 shell 的会话应当被拒，并点名原命令：%v", err)
	}
	if err := CheckLaunchedTakesTaskText(LaunchResult{Command: "x"}); err == nil {
		t.Error("回执没写工具时不该信")
	}
	if err := CheckLaunchedTakesTaskText(LaunchResult{Tool: "n044agent", Command: "n044agent", ResolvedCommand: "cat"}); err != nil {
		t.Errorf("自定义 agent 工具不该被拒：%v", err)
	}
}

// codex 一定写 trust；上游内置的其他工具不写；认不出的自定义工具说不准（n043 owner 验收补充发现）。
func TestCodexTrustWriteIsClassifiedPerTool(t *testing.T) {
	for tool, want := range map[string]CodexTrust{
		"codex": CodexTrustYes, "claude": CodexTrustNo, "gemini": CodexTrustNo, "opencode": CodexTrustNo,
		"shell": CodexTrustNo, "grok": CodexTrustMaybe, "my-codex-wrapper": CodexTrustMaybe,
	} {
		if got := CheckCodexTrust(tool); got != want {
			t.Errorf("%s：得到 %v，期望 %v", tool, got, want)
		}
	}
}

// 退出提示看外层是哪台 tmux server（n043 复审新问题 4）：和上游状态栏的 ctrl+q 对齐，吞键的那种情况说清楚。
func TestNestingAdviceFollowsTheOuterServer(t *testing.T) {
	t.Setenv("TMUX", "")
	if a := NestingAdvice(""); !strings.Contains(a, "Ctrl+Q") || !strings.Contains(a, "ctrl+q detach") {
		t.Errorf("不在 tmux 里时应当按上游状态栏说 Ctrl+Q：%q", a)
	}
	t.Setenv("TMUX", "/tmp/tmux-1000/default,123,0")
	if a := NestingAdvice(""); !strings.Contains(a, "C-b C-b d") || !strings.Contains(a, "ctrl+q 在这里会被外层吞掉") {
		t.Errorf("外层是默认 server 时应当说按两次前缀键、ctrl+q 被吞：%q", a)
	}
	t.Setenv("TMUX", "/tmp/tmux-1000/n043host,123,0")
	if a := NestingAdvice(""); !strings.Contains(a, "退出会话按 Ctrl+Q") || strings.Contains(a, "被外层吞掉") {
		t.Errorf("外层是别的 server 时应当说按 Ctrl+Q：%q", a)
	}
}
