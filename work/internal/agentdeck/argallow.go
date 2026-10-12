package agentdeck

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 启动参数整体白名单（n051 attempt 3，owner 选方案 R1-A）：deck 不再猜哪些参数危险（各家 CLI 的连写、空格、驼峰、别名、
// settings 文件、profile 列不完，B1、评审 2 R1、attempt 3 自查三次都是这一类）。启动后核对时，CLI 进程的参数只能是
// 上游 agent-deck 按 deck 的启动请求会加的已知参数，加上 owner 在 deck 设置里逐字声明的额外参数；其余一律拒绝。
//
// 上游会加哪些参数（agent-deck v1.15.0 源码，internal/session/instance.go）：
//   - claude：--session-id <uuid>、--resume <id>、--add-dir <dir>、--model <m>、--effort <e>（1306-1430、1686-1810、9150-9297）；
//     权限参数（--dangerously-skip-permissions、--permission-mode auto、--allow-dangerously-skip-permissions）不在白名单里；
//   - codex：--model <m>、--config model_reasoning_effort=<e>、resume <sid>、fork <sid>（2210-2296）；--yolo 不在白名单里；
//   - gemini：--resume <sid>、--model <m>（1818-1884）；--yolo 不在白名单里。
// CLI 自己的设置文件（~/.claude/settings.json 的 defaultMode、CODEX_HOME 里的 config.toml）不在这里查，归 owner 定的「CLI 自身默认模式」。

// LaunchArgs 是核对 CLI 进程参数用的依据。
type LaunchArgs struct {
	Tool   string     // agent-deck 的工具名：claude、codex、gemini 或自定义工具
	Binary string     // deck 设置里的可执行文件（取文件名认进程）
	Extra  [][]string // owner 在 deck 设置里逐字声明的额外参数组（例如 {"--allowedTools", "Read,Write"}）
}

// knownArgs：工具 → 上游会加的参数 → 后面跟几个值。
var knownArgs = map[string]map[string]int{
	// --chrome、--teammate-mode、--channels：上游按 [claude] use_chrome、use_teammate_mode 与频道设置加，与权限无关（attempt 3 自查：误拒）
	"claude": {"--session-id": 1, "--resume": 1, "--add-dir": 1, "--model": 1, "--effort": 1, "--chrome": 0, "--teammate-mode": 1, "--channels": 1},
	"codex":  {"--model": 1, "--config": 1, "resume": 1, "fork": 1},
	"gemini": {"--resume": 1, "--model": 1},
}

// knownValueOK 是个别已知参数对取值的限制。
func knownValueOK(tool, flag, value string) bool {
	if strings.HasPrefix(value, "-") {
		// 值的位置上放一个参数（--resume --dangerously-skip-permissions）：CLI 会把它当参数读（attempt 3 自查）
		return false
	}
	if tool == "codex" && flag == "--config" {
		// 上游只用 --config 传推理强度；其余配置覆盖都可能改权限或沙箱
		k, v, ok := strings.Cut(value, "=")
		return ok && k == "model_reasoning_effort" && v != "" && !strings.ContainsAny(v, "\\\"' \t")
	}
	return true
}

// interpreters：CLI 可能经解释器运行（codex 是 node 脚本；测试脚本的包装是 bash 脚本）：这时按脚本名认。
var interpreters = map[string]bool{"node": true, "nodejs": true, "bun": true, "deno": true, "bash": true, "sh": true, "dash": true, "zsh": true, "python": true, "python3": true}

// cliArgs：这个进程是不是要核对的 CLI；是的话返回程序名之后的参数。
func cliArgs(argv []string, want LaunchArgs) ([]string, bool) {
	if len(argv) == 0 {
		return nil, false
	}
	name, rest := filepath.Base(argv[0]), argv[1:]
	if interpreters[name] {
		if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
			return nil, false // bash -c '…'、node -e …：不是在跑一个脚本文件
		}
		name, rest = filepath.Base(rest[0]), rest[1:]
		for _, ext := range []string{".js", ".mjs", ".cjs", ".py", ".sh"} {
			name = strings.TrimSuffix(name, ext)
		}
	}
	if name == want.Tool || want.Binary != "" && name == filepath.Base(want.Binary) {
		if want.Tool == "claude" && len(rest) > 0 && rest[0] == "daemon" {
			// Claude Code 自己的后台进程（claude daemon run --origin transient …，会话里的 claude 起它），不是这次启动的会话（attempt 3 自查）
			return nil, false
		}
		return rest, true
	}
	return nil, false
}

// CheckCLIArgs 按白名单核对 CLI 进程的参数（程序名之后的部分）。
func CheckCLIArgs(args []string, want LaunchArgs) error {
	known := knownArgs[want.Tool]
	for i := 0; i < len(args); {
		a := args[i]
		flag, value, inline := strings.Cut(a, "=")
		if !strings.HasPrefix(a, "--") {
			flag, inline = a, false // 位置参数（codex 的 resume/fork）与短参数不拆等号
		}
		if n, ok := known[flag]; ok {
			switch {
			case inline && n == 1:
				if !knownValueOK(want.Tool, flag, value) {
					return fmt.Errorf("参数 %s 的取值不在白名单里（硬拒绝）", a)
				}
				i++
				continue
			case !inline && i+n < len(args):
				if n == 1 && !knownValueOK(want.Tool, flag, args[i+1]) {
					return fmt.Errorf("参数 %s %s 的取值不在白名单里（硬拒绝）", a, args[i+1])
				}
				i += 1 + n
				continue
			}
		}
		if n := matchExtra(args[i:], want.Extra); n > 0 {
			i += n
			continue
		}
		return fmt.Errorf("参数 %q 不在白名单里：deck 只放行上游按启动请求加的参数和 owner 在 deck 设置里逐字声明的额外参数（硬拒绝）", a)
	}
	return nil
}

// matchExtra：args 开头是否逐字等于某个声明的参数组；是的话返回组的长度。
func matchExtra(args []string, extra [][]string) int {
	for _, g := range extra {
		if len(g) == 0 || len(g) > len(args) {
			continue
		}
		ok := true
		for j := range g {
			if args[j] != g[j] {
				ok = false
				break
			}
		}
		if ok {
			return len(g)
		}
	}
	return 0
}

// declaredAllow：owner 能在 deck 设置 extra_args 里声明的参数（n051 attempt 4 设计复审，owner 选 B1-A：声明也走白名单）。
// 以前按「已知危险参数」查声明（评审 3 B1 之后按参数语义查），CLI 的隐藏参数与新参数（claude --inherit-permission-mode、
// codex --approve-for-me、claude --agents/--plugin-dir）照样声明得进来。现在每个工具只认这里列的几种，其余一律拒绝；
// 要放行新的参数，改这张表（附出处与理由）。
//   - claude：工具许可的细化与附加系统提示，不改权限模式、不加载设置或插件；
//   - codex：-c 只认给命令设环境变量（shell_environment_policy.*）与工作区沙箱的网络开关（owner 2026-10-12 为隔离测试批准）；
//   - gemini 与其他工具：不允许声明。
var declaredAllow = map[string]map[string]bool{
	"claude": {"--allowedTools": true, "--allowed-tools": true, "--disallowedTools": true, "--disallowed-tools": true, "--append-system-prompt": true},
	"codex":  {"-c": true}, // 只认短写法：--config 在启动参数里已是上游加推理强度用的已知参数（attempt 4 自查：两道检查不一致）
}

// envKeyOK：codex 可声明的 shell_environment_policy 只限 set.<变量名>，且不能是改程序加载或查找方式、或改 Codex 自己设置来源的变量
// （attempt 4 自查：LD_PRELOAD、PATH、HOME、CODEX_HOME、inherit=all 等以前都声明得进来）。
func envKeyOK(key string) bool {
	name, ok := strings.CutPrefix(key, "shell_environment_policy.set.")
	if !ok || !envNameRe.MatchString(name) || strings.HasPrefix(name, "LD_") {
		return false
	}
	switch name {
	case "PATH", "HOME", "CODEX_HOME", "SHELL", "BASH_ENV", "ENV", "ZDOTDIR", "PYTHONPATH", "NODE_OPTIONS":
		return false
	}
	return true
}

var envNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// CheckExtraArgs 核对 owner 为 tool 声明的额外参数：每组必须是「白名单里的一个参数 + 一个取值」（或 参数=取值），
// 取值不能是参数、不能含转义；codex 的 -c 只认白名单里的键。其余一律拒绝（硬拒绝，owner 也声明不进来）。
func CheckExtraArgs(tool string, extra [][]string) error {
	for _, g := range extra {
		if err := checkDeclaredGroup(tool, g); err != nil {
			return fmt.Errorf("额外参数 %q：%w（deck 只放行白名单里的额外参数，见 agentdeck.declaredAllow）", g, err)
		}
	}
	return nil
}

func checkDeclaredGroup(tool string, g []string) error {
	var flag, value string
	switch len(g) {
	case 1:
		var ok bool
		if flag, value, ok = strings.Cut(g[0], "="); !ok {
			return fmt.Errorf("要写成 参数 取值 两个元素，或 参数=取值")
		}
	case 2:
		flag, value = g[0], g[1]
	default:
		return fmt.Errorf("每组只能是一个参数加一个取值")
	}
	if !declaredAllow[tool][flag] {
		return fmt.Errorf("%s 不在 %s 可声明的参数里", flag, tool)
	}
	if value == "" || strings.HasPrefix(value, "-") {
		return fmt.Errorf("参数 %s 的取值不能为空、不能是参数", flag)
	}
	if low := strings.ToLower(value); strings.Contains(low, `\u`) || strings.Contains(low, `\x`) || strings.ContainsRune(value, 0) {
		return fmt.Errorf("含转义写法，deck 不解码，请改成不带转义的写法")
	}
	if tool == "codex" {
		key, v, ok := strings.Cut(value, "=")
		key = strings.TrimSpace(key)
		switch {
		case !ok:
			return fmt.Errorf("-c 的取值要写成 键=值")
		case envKeyOK(key):
		case key == "sandbox_workspace_write.network_access" && (strings.TrimSpace(v) == "true" || strings.TrimSpace(v) == "false"):
		default:
			return fmt.Errorf("-c 只认 shell_environment_policy.set.<变量名>（LD_*、PATH、HOME、CODEX_HOME 等除外）与 sandbox_workspace_write.network_access=true|false，不认 %s", key)
		}
	}
	return nil
}

// CLIWait 是启动后等 CLI 进程出现的上限（bash → env → CLI，node 再起原生程序）。
var CLIWait = 10 * time.Second

// CheckSessionArgs 启动后核对：取 tmux 会话里全部进程。CLI 进程的参数按白名单查；其余进程（bash -c、MCP 服务等）只查明确的绕过参数。
// 一个 CLI 进程都没找到（等了 CLIWait）就报错：核对不了。
func CheckSessionArgs(ctx context.Context, socket, tmuxSession string, want LaunchArgs) error {
	if tmuxSession == "" {
		return fmt.Errorf("不知道会话在哪个 tmux 会话里，核对不了它的命令行")
	}
	deadline := time.Now().Add(CLIWait)
	for {
		found, err := checkSessionOnce(ctx, socket, tmuxSession, want)
		if err != nil || found {
			return err
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("tmux 会话 %s 里等了 %s 也没找到 %s 的进程，核对不了它的启动参数", tmuxSession, CLIWait, want.Tool)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func checkSessionOnce(ctx context.Context, socket, tmuxSession string, want LaunchArgs) (bool, error) {
	panes, err := ListTmuxPanes(ctx, socket)
	if err != nil {
		return false, err
	}
	var queue []int
	for _, p := range panes {
		if p.Session == tmuxSession && p.PanePID > 0 {
			queue = append(queue, p.PanePID)
		}
	}
	if len(queue) == 0 {
		return false, fmt.Errorf("tmux 会话 %s 里没有在跑的窗格，核对不了它的命令行", tmuxSession)
	}
	children := procChildren("/proc")
	seen := map[int]bool{}
	found := false
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		ok, err := procArgsOK("/proc", pid, want)
		if err != nil {
			return false, fmt.Errorf("tmux 会话 %s 的进程 %d %w", tmuxSession, pid, err)
		}
		found = found || ok
		queue = append(queue, children[pid]...)
	}
	return found, nil
}

// procArgsOK 核对一个进程；返回它是不是 CLI 进程。进程已经退出（读不到）不算错。
func procArgsOK(proc string, pid int, want LaunchArgs) (bool, error) {
	data, err := os.ReadFile(filepath.Join(proc, strconv.Itoa(pid), "cmdline"))
	if err != nil || len(data) == 0 {
		return false, nil
	}
	argv := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	if args, ok := cliArgs(argv, want); ok {
		if err := CheckCLIArgs(args, want); err != nil {
			return true, fmt.Errorf("（%s）的启动参数：%w", filepath.Base(argv[0]), err)
		}
		return true, nil
	}
	if err := checkBypassFlags(append(argv, strings.Join(argv, " "))...); err != nil {
		return false, fmt.Errorf("的命令行带着绕过参数：%w", err)
	}
	return false, nil
}
