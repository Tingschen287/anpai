package agentdeck

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 上游配置里的绕过开关（n051 真模型端到端实测）：agent-deck v1.15 的 [claude] dangerous_mode 不写时默认为 true
// （internal/session/userconfig.go GetDangerousMode），launch 也没有按会话关掉它的参数，于是每个 Claude 会话都带着
// --dangerously-skip-permissions 起来——deck 自己的启动参数再干净也拦不住。所以启动前读上游实际会用的配置文件，
// 只认显式写明的安全值；读不懂按「确认不了」拒绝。owner 改的是 agent-deck 自己的配置，deck 不替 owner 改。

// AgentDeckConfigPath 是上游 agent-deck 会读的 config.toml（与上游 agentpaths.EffectiveConfigPath 同一顺序）：
// $XDG_CONFIG_HOME/agent-deck（未设时 ~/.config/agent-deck）里有就用它，否则 ~/.agent-deck 里有就用它，
// 都没有时返回 XDG 路径（文件不存在，上游按默认值，即 Claude 带绕过参数）。
func AgentDeckConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	base := filepath.Join(home, ".config")
	if v := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); v != "" && filepath.IsAbs(v) {
		base = v
	}
	xdg := filepath.Join(base, "agent-deck", "config.toml")
	if _, err := os.Stat(xdg); err == nil {
		return xdg, nil
	}
	legacy := filepath.Join(home, ".agent-deck", "config.toml")
	if _, err := os.Stat(legacy); err == nil {
		return legacy, nil
	}
	return xdg, nil
}

// CheckUpstreamConfigNoBypass 检查上游配置会不会让 tool 的会话带上跳过权限的参数。
func CheckUpstreamConfigNoBypass(tool string) error {
	path, err := AgentDeckConfigPath()
	if err != nil {
		return fmt.Errorf("找不到 agent-deck 的配置文件，确认不了它不会加跳过权限的参数：%w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("读不了 %s，确认不了 agent-deck 不会加跳过权限的参数：%w", path, err)
	}
	if err := checkConfigText(tool, string(data)); err != nil {
		return fmt.Errorf("%s：%w", path, err)
	}
	return nil
}

// checkConfigText 是 CheckUpstreamConfigNoBypass 的纯文本部分（便于测试）。启动前尽早拒绝；真正的保证在启动后按参数白名单核对
// （CheckSessionArgs：配置写错时上游回落默认值，也只有那一道看得到）。n051 attempt 3（owner 选 R1-A）起只看所启动工具相关的几节，
// 不再扫整个文件（attempt 3 自查：关键词扫描误拒 MCP 的 AWS_PROFILE 等常见配置）：
//   - 所启动工具的 command（[tool]、[groups.*.tool]、[conductors.*.tool]、[tools.tool]）只能是裸命令或路径，extra_args 为空：
//     上游把 command 里的多余词原样接在命令行上；
//   - [shell] exit_to_shell 不能开（上游把 exec 去掉、后面接一个交互 shell）；
//   - claude 类工具：[claude] 下必须显式写 dangerous_mode = false，且 allow_dangerous_mode、auto_mode 不能为 true；
//   - codex、gemini：[codex]/[gemini] 下 yolo_mode 不能为 true；[tools.*] 的 dangerous_mode 不能为 true；
//   - 用 claude.x = … 点号写法或 claude = {…} 内联表写这些开关的，读不懂，按确认不了拒绝。
func checkConfigText(tool, text string) error {
	section := ""
	claude := map[string]string{}
	yolo := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(stripComment(sc.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = strings.Trim(strings.TrimSpace(strings.Trim(line, "[]")), " ")
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.ToLower(strings.TrimSpace(value))
		full := key
		if section != "" {
			full = section + "." + key
		}
		if toolSection(section, tool) {
			if err := checkToolKey(key, strings.TrimSpace(strings.SplitN(line, "=", 2)[1])); err != nil {
				return fmt.Errorf("第 %d 行 [%s] %w", n, section, err)
			}
		}
		if section == "shell" && key == "exit_to_shell" && value != "false" {
			return fmt.Errorf("第 %d 行：[shell] exit_to_shell 会让上游在 CLI 外面再套一个交互 shell，deck 核对不了，请 owner 去掉或改为 false", n)
		}
		switch {
		case full == "claude" || strings.HasPrefix(full, "claude.") && section != "claude":
			if k := strings.TrimPrefix(full, "claude."); k == "claude" || isSwitch(k) {
				return fmt.Errorf("第 %d 行用点号或内联表写了 claude 的开关，deck 读不懂，确认不了不会加跳过权限的参数", n)
			}
		case section == "claude":
			claude[key] = value
		case strings.HasPrefix(section, "tools.") && (key == "dangerous_mode" || key == "yolo_mode") && value != "false":
			// 上游自定义工具声明了 dangerous_mode 时，会把它的 dangerous_flag 加到命令上（n051 自查）
			return fmt.Errorf("第 %d 行：[%s] %s 会让这个自定义工具跳过权限确认，请 owner 去掉或改为 false", n, section, key)
		case section == "codex" || section == "gemini":
			if key == "yolo_mode" {
				yolo[section] = value
			}
		case strings.HasPrefix(full, "codex.yolo_mode") || strings.HasPrefix(full, "gemini.yolo_mode"):
			yolo[strings.SplitN(full, ".", 2)[0]] = value
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("读配置失败：%w", err)
	}
	switch {
	case tool == "claude":
		if claude["dangerous_mode"] != "false" {
			return errors.New("agent-deck 会给 Claude Code 加 --dangerously-skip-permissions（v1.15 的 [claude] dangerous_mode 不写时默认开启）。" +
				"请 owner 在这个文件的 [claude] 下写 dangerous_mode = false 后再开")
		}
		for _, k := range []string{"allow_dangerous_mode", "auto_mode"} {
			if claude[k] == "true" {
				return fmt.Errorf("[claude] %s = true 会让 Claude Code 跳过权限确认，请 owner 改为 false 后再开", k)
			}
		}
	case tool == "codex" || tool == "gemini":
		if yolo[tool] == "true" {
			return fmt.Errorf("[%s] yolo_mode = true 会让会话跳过权限确认，请 owner 改为 false 后再开", tool)
		}
	}
	return nil
}

// toolSection：这一节会影响 tool 的启动命令（[tool]、[groups."…".tool]、[conductors.x.tool]、[profiles.x.tool]、[tools.tool]）。
func toolSection(section, tool string) bool {
	s := strings.ReplaceAll(section, `"`, "")
	return s == tool || s == "tools."+tool || strings.HasSuffix(s, "."+tool) &&
		(strings.HasPrefix(s, "groups.") || strings.HasPrefix(s, "conductors.") || strings.HasPrefix(s, "profiles."))
}

// checkToolKey：所启动工具那一节里，command 只能是裸命令或路径、extra_args 只能是空数组、wrapper 不能写（上游会再套一层 bash -c）。
func checkToolKey(key, raw string) error {
	switch key {
	case "command":
		v := strings.TrimSpace(raw)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		} else {
			return fmt.Errorf("command = %s：deck 只放行一个带引号的命令名或路径", raw)
		}
		if v == "" || strings.ContainsAny(v, " \t\\\"'$`;&|<>(){}") {
			return fmt.Errorf("command = %s：只能是裸命令名或路径（上游把多出来的词原样接在命令行上），参数请写进 deck 设置的 extra_args 由 owner 逐字声明", raw)
		}
	case "extra_args":
		if strings.ReplaceAll(raw, " ", "") != "[]" {
			return fmt.Errorf("extra_args = %s：必须为空，额外参数请写进 deck 设置由 owner 逐字声明", raw)
		}
	case "wrapper":
		return fmt.Errorf("wrapper = %s：上游会在 CLI 外面再套一层 shell，deck 核对不了，请 owner 去掉", raw)
	}
	return nil
}

func isSwitch(k string) bool {
	switch k {
	case "dangerous_mode", "allow_dangerous_mode", "auto_mode", "extra_args", "command":
		return true
	}
	return false
}

// stripComment 去掉不在引号里的 # 注释。
func stripComment(line string) string {
	var q rune
	for i, r := range line {
		switch {
		case q != 0 && r == q:
			q = 0
		case q == 0 && (r == '"' || r == '\''):
			q = r
		case q == 0 && r == '#':
			return line[:i]
		}
	}
	return line
}
