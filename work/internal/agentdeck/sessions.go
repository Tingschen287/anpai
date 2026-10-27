package agentdeck

import "context"

// Session 是上游一个会话在 Work Deck 眼里的样子。
//
// 字段对应上游 GET /api/sessions 的 JSON 键。
type Session struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Tool        string `json:"tool"`
	Status      string `json:"status"`
	GroupPath   string `json:"groupPath"`
	ProjectPath string `json:"projectPath"`
	TmuxSession string `json:"tmuxSession"`
	// TmuxSocketName 是 tmux -L 选择器，上游在会话创建时定下。
	//
	// 它带 omitempty：用默认 tmux server 时值为空，字段整个不出现。
	// 只看一个默认 socket 的会话就断言「接口不暴露它」是错的，
	// 本包此前正是这么错过一次。
	TmuxSocketName string `json:"tmuxSocketName"`
	Command        string `json:"command"`
	CreatedAt      string `json:"createdAt"`
	LastAccessed   string `json:"lastAccessedAt"`
}

// Group 是上游的会话分组。
type Group struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	SessionCount int    `json:"sessionCount"`
}

// SessionList 是 GET /api/sessions 的整个响应。
type SessionList struct {
	Sessions []Session `json:"sessions"`
	Groups   []Group   `json:"groups"`
	Profile  string    `json:"profile"`
}

// Sessions 冷启动时取一次全量会话。
//
// 之后的实时变化走 SSE。上游的 state.db 只在排障时看，永远不作为真相来源：
// 没有 hook overlay 时它的 instances.status 并不权威。本包不读该文件。
func (c *Client) Sessions(ctx context.Context) (SessionList, error) {
	var out SessionList
	if err := c.get(ctx, "/api/sessions", &out); err != nil {
		return SessionList{}, err
	}
	return out, nil
}
