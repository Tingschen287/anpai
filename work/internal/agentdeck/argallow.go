package agentdeck

import (
	"context"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
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
	// 在上游配置里登记的自定义工具（n057）：deck 经 -c "<工具> --model <m>" 传模型（ToolsModelViaCommand），
	// 上游登记里不写 resume_flag、dangerous_flag，不会再加别的参数
	"grok":        {"--model": 1},
	"antigravity": {"--model": 1},
	"kimi":        {"--model": 1}, // n060 段 4：同 grok，经 -c "kimi --model <模型>" 传；kimi 的 -m 简写不在白名单里（deck 只发 --model）
	// opencode（上游内置）：-m <模型>、恢复时 -s <会话号>、事件端口 --port <N>（上游 internal/session/instance.go:1894-1964
	// buildOpenCodeCommand/ExtraFlags/SSEPortFlag；n057 自查：以前不在这里，OpenCode 起来就被停掉）。--agent 不在白名单里：
	// 它换用 OpenCode 自己配置的 agent，权限由那份配置定
	"opencode": {"-m": 1, "-s": 1, "--port": 1},
}

// knownValueOK 是个别已知参数对取值的限制。
func knownValueOK(tool, flag, value string) bool {
	if strings.HasPrefix(value, "-") {
		// 值的位置上放一个参数（--resume --dangerously-skip-permissions）：CLI 会把它当参数读（attempt 3 自查）
		return false
	}
	if tool == "opencode" && flag == "--port" {
		_, err := strconv.Atoi(value)
		return err == nil
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
					return i18n.E("err.agentdeck.checkcliargs.1", a)
				}
				i++
				continue
			case !inline && i+n < len(args):
				if n == 1 && !knownValueOK(want.Tool, flag, args[i+1]) {
					return i18n.E("err.agentdeck.checkcliargs.2", a, args[i+1])
				}
				i += 1 + n
				continue
			}
		}
		if n := matchExtra(args[i:], want.Extra); n > 0 {
			i += n
			continue
		}
		return i18n.E("err.agentdeck.checkcliargs.3", a)
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
			return i18n.E("err.agentdeck.checkextraargs.1", g, err)
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
			return i18n.E("err.agentdeck.checkdeclaredgroup.1")
		}
		if tool == "claude" && strings.ContainsAny(g[0], " \t\r\n") {
			// 上游 launch --extra-arg 拒收「以 - 开头又含空白」的元素（n055：声明的参数随启动传上去）
			return i18n.E("err.agentdeck.checkdeclaredgroup.2")
		}
	case 2:
		flag, value = g[0], g[1]
	default:
		return i18n.E("err.agentdeck.checkdeclaredgroup.3")
	}
	if !declaredAllow[tool][flag] {
		return i18n.E("err.agentdeck.checkdeclaredgroup.4", flag, tool)
	}
	if value == "" || strings.HasPrefix(value, "-") {
		return i18n.E("err.agentdeck.checkdeclaredgroup.5", flag)
	}
	if low := strings.ToLower(value); strings.Contains(low, `\u`) || strings.Contains(low, `\x`) || strings.ContainsRune(value, 0) {
		return i18n.E("err.agentdeck.checkdeclaredgroup.6")
	}
	if tool == "codex" && strings.ContainsAny(value, "{}") {
		// codex 的声明经上游 -c "codex 参数…" 的包装传上去，上游把包装里的 {command} 换成整条命令（自查）
		return i18n.E("err.agentdeck.checkdeclaredgroup.7")
	}
	if tool == "codex" {
		key, v, ok := strings.Cut(value, "=")
		key = strings.TrimSpace(key)
		switch {
		case !ok:
			return i18n.E("err.agentdeck.checkdeclaredgroup.8")
		case envKeyOK(key):
		case key == "sandbox_workspace_write.network_access" && (strings.TrimSpace(v) == "true" || strings.TrimSpace(v) == "false"):
		default:
			return i18n.E("err.agentdeck.checkdeclaredgroup.9", key)
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
		return i18n.E("err.agentdeck.checksessionargs.1")
	}
	deadline := time.Now().Add(CLIWait)
	for {
		found, err := checkSessionOnce(ctx, socket, tmuxSession, want)
		if err != nil || found {
			return err
		}
		if !time.Now().Before(deadline) {
			return i18n.E("err.agentdeck.checksessionargs.2", tmuxSession, CLIWait, want.Tool)
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
		return false, i18n.E("err.agentdeck.checksessiononce.1", tmuxSession)
	}
	children := procChildren("")
	seen := map[int]bool{}
	found := false
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		ok, err := procArgsOK("", pid, want)
		if err != nil {
			return false, i18n.E("err.agentdeck.checksessiononce.2", tmuxSession, pid, err)
		}
		found = found || ok
		queue = append(queue, children[pid]...)
	}
	return found, nil
}

// procArgsOK 核对一个进程；返回它是不是 CLI 进程。进程已经退出（读不到）不算错；命令行确认不了（errProcArgsUnconfirmable）算错。
// proc 为空用本平台的进程表（Linux /proc，macOS sysctl kern.procargs2），否则读那个 /proc 形状的目录（测试用）。
func procArgsOK(proc string, pid int, want LaunchArgs) (bool, error) {
	argv, ok, err := procArgvChecked(proc, pid)
	if err != nil {
		// 读到了命令行数据但确认不了（macOS：截断、读不足 argc 个参数、自检没过）：不能当作「没有这个进程」，按确认不了拒绝
		return false, err
	}
	if !ok {
		return false, nil
	}
	if args, ok := cliArgs(argv, want); ok {
		if err := CheckCLIArgs(args, want); err != nil {
			return true, i18n.E("err.agentdeck.procargsok.1", filepath.Base(argv[0]), err)
		}
		return true, nil
	}
	// 逐字的绕过参数：每个元素和连成一串的那份都查；连串只查最后一个元素（checkGluedTail）
	if err := checkBypassFlags(append(argv[:len(argv):len(argv)], strings.Join(argv, " "))...); err != nil {
		return false, i18n.E("err.agentdeck.procargsok.2", err)
	}
	if err := checkGluedTail(argv); err != nil {
		return false, i18n.E("err.agentdeck.procargsok.2", err)
	}
	return false, nil
}

// glueEnvRe：绕过参数后面紧跟的「环境变量样式文字」：变量名（字母或下划线开头，后接字母数字下划线）加等号，例如 PATH=、XSYNTHETIC_SECRET=。
var glueEnvRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// checkGluedTail 认「连串」：目标进程改写自己参数区末尾的 NUL 时，最后一个参数会连上紧跟其后的环境变量
// （--dangerously-skip-permissionsPATH=/usr/bin；Linux 的 /proc/<pid>/cmdline 同样，见 procs_darwin.go 的 parseProcargs2）。
// 逐字相等的 checkBypassFlags 认不出这种串，这里再认。
//
// 范围（owner 选定，macOS 与 Linux 相同）：连串只会出现在 argv 的最后一个元素——被改写的是参数区末尾的那个 NUL，
// 前面的元素各有自己的结尾 NUL。所以只对最后一个 argv 元素做：
//   - 最后一个元素里的词（按空白切开、转小写，只看词的开头）以某个已知绕过参数（BypassFlags）开头、后面紧跟 glueEnvRe；
//   - 最后一个元素里的 --flag=badKEY=…（bypassValues：取值以被拒的值开头、后面紧跟 glueEnvRe），以及同一元素里「参数名 空白 badKEY=…」；
//   - 倒数第二个元素恰好是 bypassValues 里的参数名、最后一个元素的第一个词是 badKEY=…。
//
// 原有逐字相等的 checkBypassFlags（含 argv 连成一串的那份）不变，对每个位置都查；本函数不重复它。
//
// 判定规则（只放宽匹配，不放宽拒绝）：
//   - 绕过参数必须是词的开头，/opt/--dangerously-skip-permissionsPATH=x 这样中间出现的不算；
//   - 紧跟的必须是「变量名=」：--dangerously-skip-permissions-docs、--dangerously-skip-permissionsfoo（没有等号）、
//     --dangerously-skip-permissions-x=1（横线不是变量名的字符）、--dangerously-skip-permissions2=1（变量名不能数字开头）都按原语义处理，不拒；
//   - 只用在非 CLI 进程的检查上（procArgsOK）。CLI 进程仍按参数白名单查，不经过这里；CheckNoBypass 的启动前检查也不经过这里。
//
// 保守的代价（两个平台相同，owner 接受）：进程没改写过内存、只是最后一个参数字面上就是这样的串
// （例如 printf '%s\n' --yoloMODEL=demo 的最后一个参数）也会被拒绝；同样的字面串放在非末尾位置则放行。
// 字节上看，改写过的连串和这种字面串分不开，所以不区分。
func checkGluedTail(argv []string) error {
	if len(argv) == 0 {
		return nil
	}
	words := strings.Fields(argv[len(argv)-1])
	for i, w := range words {
		lw := strings.ToLower(w)
		for _, b := range BypassFlags {
			if rest, ok := strings.CutPrefix(lw, b); ok && glueEnvRe.MatchString(rest) {
				return i18n.E("err.agentdeck.checkbypassflags.1", w)
			}
		}
		name, value, hasValue := strings.Cut(lw, "=")
		bad, ok := bypassValues[name]
		if !ok {
			continue
		}
		if !hasValue && i+1 < len(words) {
			value, hasValue = strings.ToLower(words[i+1]), true
		}
		if hasValue && gluedValue(value, bad) {
			return i18n.E("err.agentdeck.checkbypassflags.2", w, value)
		}
	}
	// 参数名独占倒数第二个元素，取值在最后一个元素里
	if len(argv) >= 2 && len(words) > 0 {
		prev := strings.ToLower(strings.TrimSpace(argv[len(argv)-2]))
		if bad, ok := bypassValues[prev]; ok {
			if value := strings.ToLower(words[0]); gluedValue(value, bad) {
				return i18n.E("err.agentdeck.checkbypassflags.2", prev, value)
			}
		}
	}
	return nil
}

// gluedValue：取值（去掉成对引号后）以被拒的值开头、后面紧跟 glueEnvRe。
func gluedValue(value, bad string) bool {
	rest, ok := strings.CutPrefix(strings.Trim(value, `"'`), bad)
	return ok && glueEnvRe.MatchString(rest)
}
