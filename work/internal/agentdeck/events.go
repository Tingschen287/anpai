package agentdeck

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"net/http"
	"strings"
)

// MenuEvent 是 SSE /events/menu 推来的一份菜单快照。
//
// 上游每次推的是全量，不是增量，所以收到就整份替换，不要自己合并。
type MenuEvent struct {
	Profile       string     `json:"profile"`
	GeneratedAt   string     `json:"generatedAt"`
	TotalGroups   int        `json:"totalGroups"`
	TotalSessions int        `json:"totalSessions"`
	Items         []MenuItem `json:"items"`
}

// MenuItem 是菜单里的一行，可能是分组也可能是会话。
type MenuItem struct {
	Index   int      `json:"index"`
	Type    string   `json:"type"`
	Level   int      `json:"level"`
	Path    string   `json:"path"`
	Group   *Group   `json:"group"`
	Session *Session `json:"session"`
}

// SessionsOf 把一份菜单快照里的会话挑出来。
func (m MenuEvent) SessionsOf() []Session {
	var out []Session
	for _, it := range m.Items {
		if it.Session != nil {
			out = append(out, *it.Session)
		}
	}
	return out
}

// WatchMenu 订阅 SSE /events/menu，每收到一份快照就调一次 onEvent。
//
// 它会一直读到 ctx 被取消或连接断开。断线重连由调用方决定，本包不自作主张地重试，
// 因为「连不上」这件事必须让界面知道，而不是悄悄地在后台空转。
func (c *Client) WatchMenu(ctx context.Context, onEvent func(MenuEvent)) error {
	if c.Addr == "" {
		return i18n.E("err.agentdeck.clientget.1")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+c.Addr+"/events/menu", nil)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("Accept", "text/event-stream")

	// SSE 是长连接，不能套用普通请求的超时。
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return i18n.E("err.agentdeck.clientwatchmenu.1", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return i18n.E("err.agentdeck.clientwatchmenu.2", resp.StatusCode)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var eventName string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if eventName != "menu" || payload == "" {
				continue
			}
			var ev MenuEvent
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				// 单条解析不了不该掀翻整条流，但也不能装作没发生。
				return i18n.E("err.agentdeck.clientwatchmenu.3", err)
			}
			onEvent(ev)
		case line == "":
			eventName = ""
		}
	}
	if err := sc.Err(); err != nil {
		return i18n.E("err.agentdeck.clientwatchmenu.4", err)
	}
	return ctx.Err()
}
