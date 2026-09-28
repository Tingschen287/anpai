package agentdeck

import (
	"context"
	"encoding/json"
	"fmt"
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
}

// Validate 检查这次派工说不说得清楚。
func (r LaunchRequest) Validate() error {
	if r.ProjectPath == "" {
		return fmt.Errorf("派工缺少项目路径")
	}
	if r.Tool == "" {
		return fmt.Errorf("派工缺少要运行的工具")
	}
	switch {
	case r.Model == "" && !r.ModelNotApplicable:
		return fmt.Errorf("派工缺少模型 ID：模型未知时拒绝派工，不替你挑默认值")
	case r.Model != "" && r.ModelNotApplicable:
		return fmt.Errorf("请求自相矛盾：既声明了工具没有模型，又给了模型 %q", r.Model)
	}
	return CheckToolModel(r.Tool, r.ModelNotApplicable)
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

	args := []string{req.ProjectPath, "-c", req.Tool, "--json"}
	if !req.ModelNotApplicable {
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
	if jsonErr := json.Unmarshal(out, &res); jsonErr != nil {
		return LaunchResult{}, fmt.Errorf("upstream agent-deck launch 的输出不是合法 JSON：%w", jsonErr)
	}
	// 上游失败时退出码可能仍是 0，真正的结论在 success 字段上，
	// 原因在 error/code 里。把它的原话带出来，不要只说一句「失败了」。
	if !res.Success {
		detail := res.Error
		if res.Code != "" {
			detail = res.Code + ": " + detail
		}
		if detail == "" {
			detail = "上游没有说明原因"
		}
		return res, fmt.Errorf("upstream agent-deck launch 报告失败（%s）", detail)
	}
	if res.SessionID == "" {
		// 拿不到会话号就绑不上，这时宁可报错，也不落一条绑不住的记录。
		return res, fmt.Errorf("upstream agent-deck launch 没有回传 session_id")
	}
	return res, nil
}
