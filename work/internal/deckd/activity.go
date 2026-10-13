package deckd

import (
	"context"
	"sync"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
)

// 会话运行状态（n049，侧栏状态圆点）：deckd 定时读 agent-deck 的会话状态，按 tmux 会话名记下。
// 读不到、过期、或会话不在列表里，一律 unknown，不猜。
// n055（owner 在设计复审选 A）起默认用 CLI `agent-deck list --json` 读：不经 headless 服务，没有 token、没有端口。
// 实测（attempt 2 自查）：不写会话状态；会建 profile、跑 schema 迁移，可能写 codex 消费标记、清理过期的 auth-hold 文件。
// 状态与 /api/menu 的差异：含已归档会话；starting 记为 unknown。开销随 profile 里的会话数线性增长（每个约 6–7 ms），
// 所以间隔按上次耗时自适应。显式给了 -upstream 时仍走 headless 服务（旧行为）。

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
	// every 是下一次取之前等多久：取一次越慢，间隔越长（10 倍于上次耗时，最少 activityEvery），
	// 免得 profile 里会话多了以后 list --json 把一个核占满（n055 attempt 2 自查：每多一个会话约 6–7 ms）。
	every   time.Duration
	lastErr string // 最近一次取失败的原因（成功后清空），/v1/health 里看得到
}

// interval 是当前的取数间隔。
func (a *activityCache) interval() time.Duration {
	if a.every < activityEvery {
		return activityEvery
	}
	return a.every
}

// Err 返回最近一次取失败的原因（没有为空）。缓存为 nil 也安全。
func (a *activityCache) Err() string {
	if a == nil {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastErr
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
	if maxAge := max(activityMaxAge, 3*a.interval()); a.now().Sub(a.at) > maxAge {
		return "unknown"
	}
	if st, ok := a.byTmx[tmux]; ok && st != "" {
		return st
	}
	return "unknown"
}

// refresh 取一次；失败时保留旧数据，由 of 按时间判过期。
func (a *activityCache) refresh(ctx context.Context) {
	start := time.Now()
	m, err := a.fetch(ctx)
	took := time.Since(start)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.every = max(activityEvery, 10*took)
	if err != nil {
		a.lastErr = err.Error()
		return
	}
	a.byTmx, a.at, a.lastErr = m, a.now(), ""
}

func (a *activityCache) run(ctx context.Context) {
	for {
		a.refresh(ctx)
		a.mu.Lock()
		wait := a.interval()
		a.mu.Unlock()
		if sleepCtx(ctx, wait) != nil {
			return
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
		c.Timeout = 60 * time.Second // 会话多时一次要几秒；间隔按耗时放大，不会叠加（refresh 串行）
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
