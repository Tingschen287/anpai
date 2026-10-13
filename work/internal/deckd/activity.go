package deckd

import (
	"context"
	"sync"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
)

// 会话运行状态（n049，侧栏状态圆点）：deckd 定时读 agent-deck 的会话状态，按 tmux 会话名记下。
// 读不到、过期、或会话不在列表里，一律 unknown，不猜。
// n055（owner 在设计复审选 A）起默认用 CLI `agent-deck list --json` 读：只读、不经 headless 服务，没有 token、没有端口；
// 上游注释写明它与 /api/menu、TUI 给的是同一个状态（issue #610）。显式给了 -upstream 时仍走 headless 服务（旧行为）。

const (
	activityEvery  = 3 * time.Second
	activityMaxAge = 15 * time.Second
)

type activityCache struct {
	mu    sync.Mutex
	at    time.Time
	byTmx map[string]string
	now   func() time.Time
	fetch func(ctx context.Context) (map[string]string, error)
}

func newActivityCache(fetch func(ctx context.Context) (map[string]string, error)) *activityCache {
	return &activityCache{byTmx: map[string]string{}, now: time.Now, fetch: fetch}
}

// of 返回某个 tmux 会话的状态。缓存为 nil（没配上游）也安全。
func (a *activityCache) of(tmux string) string {
	if a == nil || tmux == "" {
		return "unknown"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.now().Sub(a.at) > activityMaxAge {
		return "unknown"
	}
	if st, ok := a.byTmx[tmux]; ok && st != "" {
		return st
	}
	return "unknown"
}

// refresh 取一次；失败时保留旧数据，由 of 按时间判过期。
func (a *activityCache) refresh(ctx context.Context) {
	m, err := a.fetch(ctx)
	if err != nil {
		return
	}
	a.mu.Lock()
	a.byTmx, a.at = m, a.now()
	a.mu.Unlock()
}

func (a *activityCache) run(ctx context.Context) {
	t := time.NewTicker(activityEvery)
	defer t.Stop()
	for {
		a.refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// listActivity 用 CLI `agent-deck list --json` 读状态（n055 默认）。
func listActivity(binary, profile string) func(ctx context.Context) (map[string]string, error) {
	return func(ctx context.Context) (map[string]string, error) {
		c, err := agentdeck.NewClient(binary, profile, "", "")
		if err != nil {
			return nil, err
		}
		c.Timeout = 5 * time.Second
		list, err := c.ListSessions(ctx)
		if err != nil {
			return nil, err
		}
		out := map[string]string{}
		for _, x := range list {
			if x.TmuxSession != "" {
				out[x.TmuxSession] = x.Status
			}
		}
		return out, nil
	}
}

// upstreamActivity 用 agent-deck 的 headless 服务读菜单（GET /api/menu，只读）。
func upstreamActivity(profile, addr, token string) func(ctx context.Context) (map[string]string, error) {
	return func(ctx context.Context) (map[string]string, error) {
		c, err := agentdeck.NewClient("agent-deck", profile, addr, token)
		if err != nil {
			return nil, err
		}
		c.Timeout = 2 * time.Second
		menu, err := c.MenuSnapshot(ctx)
		if err != nil {
			return nil, err
		}
		out := map[string]string{}
		for _, x := range menu.SessionsOf() {
			if x.TmuxSession != "" {
				out[x.TmuxSession] = x.Status
			}
		}
		return out, nil
	}
}
