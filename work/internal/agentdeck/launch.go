package agentdeck

import (
	"context"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"strings"
)

// LaunchRequest 是一次派工请求。
//
// Model 必填。派工时说不清用的是哪个模型，事后就没法核对派工纪律，
// 所以本包宁可拒绝派工，也不替调用方挑一个默认值。
type LaunchRequest struct {
	ProjectPath string
	Title       string
	Group       string
	// 要跑的工具，对应上游的 -c，例如 claude、codex。
	Tool string
	// 模型 ID，对应上游的 --model。
	Model string
	// ModelNotApplicable 显式声明这个工具压根没有模型可选，例如 shell。
	//
	// 这不是给「不知道用哪个模型」开的后门：不知道和不适用是两回事，
	// 前者必须拒绝派工。只有调用方明说「这工具没有模型」时才能置位，
	// 而且一旦置位就不许再给 Model，自相矛盾的请求同样拒绝。
	ModelNotApplicable bool
	// 没有「首条消息」字段（n039 起）：上游 launch -m 会在 Work Deck 登记写入权之前就把消息送进会话，
	// 登记失败时消息已经发出。任务文本一律等登记成功后，经 arbiter 的受仲裁投递口（SendMessage）送。
	// 不等待 agent 就绪。
	NoWait bool
	// Extra 是 owner 在 deck 设置里声明的额外参数组（已按 CheckExtraArgs 白名单核过，n055）。n051 只拿它当启动后核对的白名单、
	// 从没真正传给 CLI（端到端靠测试脚本的包装层加上），真实使用时 --allowedTools、Codex 的 network_access 都不生效。
	// 现在随启动传上去：claude 用上游的 --extra-arg（每个元素一个，上游逐个 shell 转义后接在命令末尾）；
	// codex 上游没有 --extra-arg，按上游 -c "工具 参数…" 的写法传（上游把工具名后面的参数接在它自己加的参数之后）。
	Extra [][]string
}

// Validate 检查这次派工说不说得清楚。
func (r LaunchRequest) Validate() error {
	if r.ProjectPath == "" {
		return i18n.E("err.agentdeck.launchrequestvalidate.1")
	}
	if r.Tool == "" {
		return i18n.E("err.agentdeck.launchrequestvalidate.2")
	}
	switch {
	case r.Model == "" && !r.ModelNotApplicable:
		return i18n.E("err.agentdeck.launchrequestvalidate.3")
	case r.Model != "" && r.ModelNotApplicable:
		return i18n.E("err.agentdeck.launchrequestvalidate.4", r.Model)
	}
	if len(r.Extra) > 0 {
		if r.Tool != "claude" && r.Tool != "codex" {
			return i18n.E("err.agentdeck.launchrequestvalidate.5", r.Tool)
		}
		if err := CheckExtraArgs(r.Tool, r.Extra); err != nil {
			return err
		}
	}
	return CheckToolModel(r.Tool, r.ModelNotApplicable)
}

// toolArg 是 launch -c 的取值：工具名；codex 带声明的额外参数时接在后面（逐个 shell 转义）；
// 模型经命令传的自定义工具（grok、antigravity）把模型参数接在后面（n057）。
func (r LaunchRequest) toolArg() string {
	if flag := ToolsModelViaCommand[r.Tool]; flag != "" && !r.ModelNotApplicable {
		return r.Tool + " " + flag + " " + shQuote(r.Model)
	}
	if r.Tool != "codex" || len(r.Extra) == 0 {
		return r.Tool
	}
	parts := []string{r.Tool}
	for _, g := range r.Extra {
		for _, tok := range g {
			parts = append(parts, shQuote(tok))
		}
	}
	return strings.Join(parts, " ")
}

// LaunchResult 是上游 launch --json 回传的内容。
//
// 字段名对应上游 JSON 的键，改动上游版本时先比对 manifest.go 的依赖清单。
type LaunchResult struct {
	SessionID       string `json:"session_id"`
	ID              string `json:"id"`
	Success         bool   `json:"success"`
	Profile         string `json:"profile"`
	Tool            string `json:"tool"`
	Command         string `json:"command"`
	ResolvedCommand string `json:"resolved_command"`
	Title           string `json:"title"`
	Group           string `json:"group"`
	Path            string `json:"path"`
	// 上游只在模型 ID 非空时才带这两个字段。
	ModelID      string `json:"model_id"`
	ModelVersion string `json:"model_version"`

	// 上游失败时填这两个，成功时为空。
	Error string `json:"error"`
	Code  string `json:"code"`
}

// Launch 通过 upstream agent-deck 启动一个真实会话。
func (c *Client) Launch(ctx context.Context, req LaunchRequest) (LaunchResult, error) {
	if err := req.Validate(); err != nil {
		return LaunchResult{}, err
	}

	args := []string{req.ProjectPath, "-c", req.toolArg(), "--json"}
	if req.Tool == "claude" {
		for _, g := range req.Extra {
			for _, tok := range g {
				args = append(args, "--extra-arg", tok)
			}
		}
	}
	if !req.ModelNotApplicable && ToolsModelViaCommand[req.Tool] == "" {
		args = append(args, "--model", req.Model)
	}
	if req.Title != "" {
		args = append(args, "-t", req.Title)
	}
	if req.Group != "" {
		args = append(args, "-g", req.Group)
	}
	if req.NoWait {
		args = append(args, "--no-wait")
	}

	out, err := c.exec(ctx, "launch", args...)
	if err != nil {
		return LaunchResult{}, err
	}
	var res LaunchResult
	if jsonErr := i18n.UnmarshalExternal(out, &res); jsonErr != nil {
		return LaunchResult{}, i18n.E("err.agentdeck.clientlaunch.1", jsonErr)
	}
	// 上游失败时退出码可能仍是 0，真正的结论在 success 字段上，
	// 原因在 error/code 里。把它的原话带出来，不要只说一句「失败了」。
	if !res.Success {
		// 上游没给原因时，用嵌套码。不要先把中文压成字符串，否则英文句里会留下中文。
		// 上游自己的原文仍是普通字符串，不当成码。
		var detail any
		switch {
		case res.Error == "" && res.Code == "":
			detail = i18n.E("err.agentdeck.clientlaunch.4")
		case res.Code != "":
			detail = res.Code + ": " + res.Error
		default:
			detail = res.Error
		}
		return res, i18n.E("err.agentdeck.clientlaunch.2", detail)
	}
	if res.SessionID == "" {
		// 拿不到会话号就绑不上，这时宁可报错，也不落一条绑不住的记录。
		return res, i18n.E("err.agentdeck.clientlaunch.3")
	}
	return res, nil
}

// shQuote 用单引号包一个 shell 参数（codex 声明参数经上游 -c 包装时用）。
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
