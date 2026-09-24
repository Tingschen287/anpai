package agentdeck

import (
	"context"
	"encoding/json"
	"fmt"
)

// ListedSession 是 `list --json` 输出里的一行。
//
// 注意它和 Session（`/api/sessions` 与菜单事件用的那个）不是同一组字段：
// 命名风格是 snake_case，**而且没有 socket 名**。
// 上游 `cmd/agent-deck/main.go:2250` 的 sessionJSON 结构体里就没有那个字段，
// 所以它不可能出现——这和「字段有但这次为空」是两回事，见 MANIFEST.md 第 5 节。
type ListedSession struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Tool        string `json:"tool"`
	Status      string `json:"status"`
	Path        string `json:"path"`
	Group       string `json:"group"`
	TmuxSession string `json:"tmux_session"`
	Profile     string `json:"profile"`
	Archived    bool   `json:"archived"`
}

// ListSessions 走 CLI 的 `list --json`，直接读上游存储。
//
// 为什么要有它，而不是都走 HTTP：headless 服务对菜单快照做内存缓存，
// 只在自己处理变更时失效。CLI `launch` 是另一个进程，服务感知不到，
// 所以刚派出去的会话在 `/api/sessions`、`/api/menu` 和 SSE 里都看不到，
// 直到服务重启。这条约束由 TestIntegrationLaunchLandsInStorageEvenThoughServerCachesIt
// 钉着。
//
// `list` 不经服务，看得见。实测：同一时刻菜单里 0 个会话，list 里 1 个，
// 见 TestIntegrationListSeesWhatTheCachedMenuCannot。
func (c *Client) ListSessions(ctx context.Context) ([]ListedSession, error) {
	out, err := c.exec(ctx, "list", "--json")
	if err != nil {
		return nil, err
	}
	var sessions []ListedSession
	if err := json.Unmarshal(out, &sessions); err != nil {
		return nil, fmt.Errorf("解析 list --json 输出失败：%w", err)
	}
	return sessions, nil
}

// TmuxSessionOf 从 list 的结果里找某个会话的 tmux 会话名。
func TmuxSessionOf(sessions []ListedSession, sessionID string) (string, bool) {
	for _, s := range sessions {
		if s.ID == sessionID {
			return s.TmuxSession, s.TmuxSession != ""
		}
	}
	return "", false
}
