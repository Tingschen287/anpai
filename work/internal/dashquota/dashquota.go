// Package dashquota 从 owner 的 Token 看板读各家额度（n050，架构稿 §6）。
//
// 看板是本机常驻服务（默认 http://127.0.0.1:8899），/api/data 给出各来源的 5h / 周窗口已用百分比与重置时间。
// 这里只读，不改看板。取不到、过期、或该工具没有窗口数据时如实给「未知」并说明原因，不编数字。
//
// 工具 → 看板来源的对应只是起点：同一工具的不同模型可能走不同账户，这一层只按工具给出账户级窗口；
// 模型级的取舍（例如看板新鲜但对应的不是这个模型的额度池时，不覆盖 queue 里该模型的「额度用尽」观测）由调用方决定。
package dashquota

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 状态。
const (
	Available = "available"
	Warn      = "warn"
	Exhausted = "exhausted"
	Unknown   = "unknown"
)

// ProfileOf 是工具到看板来源的对应。
var ProfileOf = map[string]string{
	"claude":   "cco",
	"codex":    "codex",
	"grok":     "grok",
	"opencode": "oc",
	"kimi":     "kimi",
}

// Window 是一个额度窗口。
type Window struct {
	Key   string    `json:"key"` // 5h、week
	Pct   int       `json:"pct"`
	Reset time.Time `json:"reset,omitempty"`
}

// ToolQuota 是一个工具的额度。
type ToolQuota struct {
	Tool      string    `json:"tool"`
	Profile   string    `json:"profile"`
	Status    string    `json:"status"`
	Windows   []Window  `json:"windows"`
	Reason    string    `json:"reason,omitempty"`
	Generated time.Time `json:"generated,omitempty"`
	// Estimated 为真表示窗口是估算的（OpenCode 免费模型的撞墙日均值），只给面板显示；
	// Tool 永远不返回估算窗口，deckd 的额度门照旧把这类来源当「未知」（n056 自查：估算不能放开 frontier 子 agent 的门）。
	Estimated bool `json:"estimated,omitempty"`
	// Label 是面板上显示的名字（同一工具有几个额度池时区分，例如 Antigravity 的 Gemini 与 Claude）；空就按工具名。
	Label string `json:"label,omitempty"`
	// Source 说明窗口从哪来（面板专用的补充来源，例如 Claude 状态栏的缓存）；空表示 Token 看板。
	Source string `json:"source,omitempty"`
}

// Thresholds 是告急与用尽的阈值（百分比），可配。
type Thresholds struct {
	Warn      int
	Exhausted int
}

// DefaultThresholds 是架构稿 §6 的默认值。
var DefaultThresholds = Thresholds{Warn: 75, Exhausted: 90}

// Client 读看板并缓存。
type Client struct {
	URL        string        // 默认 http://127.0.0.1:8899
	MaxAge     time.Duration // 看板数据超过这个年龄就当未知，默认 10 分钟
	CacheFor   time.Duration // 默认 60 秒
	Thresholds Thresholds
	HTTP       *http.Client
	Now        func() time.Time
	// ClaudeUsagePath 是 Claude 状态栏缓存的位置；空用 ClaudeUsagePath()（测试换掉）。
	ClaudeUsagePath string

	mu      sync.Mutex
	fetched time.Time
	data    *payload
	err     error
}

type payload struct {
	Generated string                     `json:"generated"`
	Profiles  []profile                  `json:"profiles"`
	Quota     map[string]json.RawMessage `json:"quota"`
	Models    []modelDay                 `json:"models"`
}

type profile struct {
	Key      string    `json:"key"`
	Quota    *winsJSON `json:"quota"`
	ZenLimit *zenLimit `json:"zen_limit"`
}

// zenLimit 是看板对 OpenCode 免费模型估出的日限额：几个撞墙日的全 token 用量取均值（看板 collect.py zen_hit_days）。
type zenLimit struct {
	Model     string  `json:"model"`
	AvgTokens float64 `json:"avg_tokens"`
	Samples   int     `json:"samples"`
}

// modelDay 是看板按日、按模型的用量。
type modelDay struct {
	Date      string  `json:"date"`
	Profile   string  `json:"profile"`
	Model     string  `json:"model"`
	Incr      float64 `json:"incr"`
	CacheRead float64 `json:"cache_read"`
	Reasoning float64 `json:"reasoning"`
}

type winsJSON struct {
	Windows []struct {
		Key   string `json:"key"`
		Pct   *int   `json:"pct"`
		Reset string `json:"reset"`
	} `json:"windows"`
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) fetch(ctx context.Context) (*payload, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cache := c.CacheFor
	if cache == 0 {
		cache = 60 * time.Second
	}
	if !c.fetched.IsZero() && c.now().Sub(c.fetched) < cache {
		return c.data, c.err
	}
	url := c.URL
	if url == "" {
		url = "http://127.0.0.1:8899"
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 3 * time.Second}
	}
	c.fetched = c.now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/data", nil)
	if err != nil {
		c.data, c.err = nil, err
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		c.data, c.err = nil, fmt.Errorf("Token 看板取不到（%v）", err)
		return nil, c.err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.data, c.err = nil, fmt.Errorf("Token 看板返回 %d", resp.StatusCode)
		return nil, c.err
	}
	var p payload
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		c.data, c.err = nil, fmt.Errorf("Token 看板数据解析不了（%v）", err)
		return nil, c.err
	}
	c.data, c.err = &p, nil
	return &p, nil
}

// All 返回全部工具的额度，按 ProfileOf 的键。
func (c *Client) All(ctx context.Context) []ToolQuota {
	var out []ToolQuota
	for _, tool := range []string{"claude", "codex", "grok", "opencode", "kimi"} {
		out = append(out, c.Tool(ctx, tool))
	}
	return out
}

// Tool 返回一个工具的额度。
func (c *Client) Tool(ctx context.Context, tool string) ToolQuota {
	prof, ok := ProfileOf[tool]
	q := ToolQuota{Tool: tool, Profile: prof, Status: Unknown}
	if !ok {
		q.Reason = "看板里没有这个工具的来源"
		return q
	}
	p, err := c.fetch(ctx)
	if err != nil {
		q.Reason = err.Error()
		return q
	}
	gen, err := time.Parse(time.RFC3339, p.Generated)
	if err != nil {
		q.Reason = "看板数据没有生成时间，无法判断是否新鲜"
		return q
	}
	q.Generated = gen
	maxAge := c.MaxAge
	if maxAge == 0 {
		maxAge = 10 * time.Minute
	}
	if age := c.now().Sub(gen); age > maxAge {
		q.Reason = fmt.Sprintf("看板数据已 %d 分钟没更新", int(age.Minutes()))
		return q
	}
	wins, found := windowsFor(p, prof)
	if !found || len(wins) == 0 {
		q.Reason = "看板没有这个来源的额度窗口（例如 OpenCode 免费席位只有估算）"
		return q
	}
	q.Windows = wins
	q.Status = c.statusOf(wins)
	return q
}

func (c *Client) statusOf(wins []Window) string {
	th := c.Thresholds
	if th.Warn == 0 && th.Exhausted == 0 {
		th = DefaultThresholds
	}
	status := Available
	for _, w := range wins {
		switch {
		case w.Pct >= th.Exhausted:
			status = Exhausted
		case w.Pct >= th.Warn && status != Exhausted:
			status = Warn
		}
	}
	return status
}

// fresh 说明看板数据的生成时间在 MaxAge 之内（与 Tool 判过期同一标准）。
func (c *Client) fresh(gen time.Time) bool {
	maxAge := c.MaxAge
	if maxAge == 0 {
		maxAge = 10 * time.Minute
	}
	return !gen.IsZero() && c.now().Sub(gen) <= maxAge
}

// DisplayTools 是额度面板显示的来源与顺序（owner 2026-10-14）：加上 Antigravity；Kimi 是 owner 公司的，暂时隐藏
// （要恢复就把 "kimi" 加回来）。
var DisplayTools = []string{"claude", "codex", "grok", "antigravity", "opencode"}

// Display 是给额度面板看的额度（deckd 的 /v1/quota），按 DisplayTools。在 Tool 的基础上补几个面板专用的来源（n056）：
//   - OpenCode 没有真实窗口、但看板有撞墙日均值：补估算的「日」窗口，标 Estimated（owner 要求按看板的均值逻辑显示）；
//   - Claude 看板里没有窗口：读 owner 的 Claude Code 状态栏脚本缓存的官方用量（5 小时、7 天），标 Source；
//   - Antigravity：看板顶层 quota.antigravity 的各额度池（Gemini、Claude），各成一条。
//
// 这些都不进 Tool：deckd 的额度门照旧只认看板的真实窗口（n056 自查）。
func (c *Client) Display(ctx context.Context) []ToolQuota {
	var out []ToolQuota
	for _, tool := range DisplayTools {
		if tool == "antigravity" {
			out = append(out, c.antigravity(ctx)...)
			continue
		}
		q := c.Tool(ctx, tool)
		switch {
		case tool == "opencode" && q.Status == Unknown && c.fresh(q.Generated):
			// 只在看板新鲜时补：Tool 在判过期之前就写了 Generated，过期或跨日的旧快照不能拿来估算（n056 评审 1 B1）
			if p, err := c.fetch(ctx); err == nil {
				if w, ok := zenWindow(p, c.now()); ok {
					q.Windows, q.Status, q.Estimated = []Window{w}, c.statusOf([]Window{w}), true
					q.Reason = "估算：看板撞墙日均值（不作额度门依据）"
				}
			}
		case tool == "claude" && q.Status == Unknown:
			if wins, ok := c.claudeStatusline(); ok {
				q.Windows, q.Status, q.Reason = wins, c.statusOf(wins), ""
				q.Source = "Claude 状态栏缓存"
			}
		}
		out = append(out, q)
	}
	return out
}

// ClaudeUsagePath 是 owner 的 Claude Code 状态栏脚本（~/.claude/statusline.py）缓存官方用量的文件：
// /tmp/claude/<CLAUDE_CONFIG_DIR 的目录名>/statusline-usage-cache.json，脚本最多 5 分钟刷新一次。只读，不碰凭据。
func ClaudeUsagePath() string {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = ".claude"
	}
	return filepath.Join("/tmp/claude", filepath.Base(dir), "statusline-usage-cache.json")
}

// claudeStatusline 读状态栏缓存里的 five_hour、seven_day（utilization 是已用百分比，resets_at 是重置时间）。
// 文件超过一天没更新就不用（说明 Claude Code 好久没开，数字不可信）。
func (c *Client) claudeStatusline() ([]Window, bool) {
	path := c.ClaudeUsagePath
	if path == "" {
		path = ClaudeUsagePath()
	}
	st, err := os.Stat(path)
	if err != nil || c.now().Sub(st.ModTime()) > 24*time.Hour {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	// 缓存里还有许多别的字段（类型各异）：只解出要的两项，别的不管（真实缓存整体按对象解会失败）
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return nil, false
	}
	var wins []Window
	for _, k := range [][2]string{{"five_hour", "5h"}, {"seven_day", "week"}} {
		var x *struct {
			Utilization *float64 `json:"utilization"`
			ResetsAt    string   `json:"resets_at"`
		}
		if json.Unmarshal(raw[k[0]], &x) != nil || x == nil || x.Utilization == nil {
			continue
		}
		w := Window{Key: k[1], Pct: int(math.Round(*x.Utilization))}
		if t, err := time.Parse(time.RFC3339, x.ResetsAt); err == nil {
			w.Reset = t
		}
		if !w.Reset.IsZero() && !w.Reset.After(c.now()) {
			// 已过重置时刻：缓存里的是上个周期的数，不显示（状态栏脚本此时也会重取；n056 自查）
			continue
		}
		wins = append(wins, w)
	}
	return wins, len(wins) > 0
}

// antigravity 取看板顶层 quota.antigravity 的各额度池（rows），每个池一条，名字是「Antigravity <池名>」。
// 看板取不到、过期或没有这项时返回空（面板不显示）。
func (c *Client) antigravity(ctx context.Context) []ToolQuota {
	p, err := c.fetch(ctx)
	if err != nil {
		return nil
	}
	gen, err := time.Parse(time.RFC3339, p.Generated)
	maxAge := c.MaxAge
	if maxAge == 0 {
		maxAge = 10 * time.Minute
	}
	if err != nil || c.now().Sub(gen) > maxAge {
		return nil
	}
	raw, ok := p.Quota["antigravity"]
	if !ok {
		return nil
	}
	var ag struct {
		Rows []struct {
			Name string `json:"name"`
			winsJSON
		} `json:"rows"`
	}
	if json.Unmarshal(raw, &ag) != nil {
		return nil
	}
	var out []ToolQuota
	for _, r := range ag.Rows {
		var wins []Window
		for _, x := range r.Windows {
			if x.Pct == nil {
				continue
			}
			w := Window{Key: x.Key, Pct: *x.Pct}
			if t, err := time.Parse(time.RFC3339, x.Reset); err == nil {
				w.Reset = t
			}
			wins = append(wins, w)
		}
		if len(wins) == 0 {
			continue
		}
		out = append(out, ToolQuota{Tool: "antigravity", Profile: "antigravity", Label: strings.TrimSpace("Antigravity " + r.Name),
			Status: c.statusOf(wins), Windows: wins, Generated: gen})
	}
	return out
}

// zenWindow 按看板前端（calendar.js）的同一口径算 OpenCode 当天额度（n056，owner：「opencode需要你去看我的token Dashboard
// 其实关于这个的额度我是有一份取平均值的逻辑的」）：分子是本地今天该免费模型的全 token 用量（增量 + 缓存读 + reasoning），
// 分母是撞墙日均值；按自然日，重置在本地次日 0 点。窗口键 "day"。缺模型名或均值时不算。
func zenWindow(p *payload, now time.Time) (Window, bool) {
	var z *zenLimit
	for _, pr := range p.Profiles {
		if pr.Key == "oc" && pr.ZenLimit != nil {
			z = pr.ZenLimit
		}
	}
	if z == nil || z.Model == "" || z.AvgTokens <= 0 {
		return Window{}, false
	}
	local := now.Local()
	today := local.Format("2006-01-02")
	used := 0.0
	for _, m := range p.Models {
		if m.Date == today && m.Profile == "oc" && m.Model == z.Model {
			used += m.Incr + m.CacheRead + m.Reasoning
		}
	}
	pct := int(math.Round(used / z.AvgTokens * 100))
	if pct > 100 {
		pct = 100
	}
	y, mo, d := local.Date()
	return Window{Key: "day", Pct: pct, Reset: time.Date(y, mo, d+1, 0, 0, 0, 0, time.Local)}, true
}

// windowsFor：先看顶层 quota（cco、kimi、grok），再看 profiles 里的 quota（codex）。
func windowsFor(p *payload, prof string) ([]Window, bool) {
	var w *winsJSON
	if raw, ok := p.Quota[prof]; ok && string(raw) != "null" {
		var x winsJSON
		if json.Unmarshal(raw, &x) == nil && len(x.Windows) > 0 {
			w = &x
		}
	}
	if w == nil {
		for _, pr := range p.Profiles {
			if pr.Key == prof && pr.Quota != nil {
				w = pr.Quota
			}
		}
	}
	if w == nil {
		return nil, false
	}
	var out []Window
	for _, x := range w.Windows {
		if x.Pct == nil {
			continue // 没有百分比的窗口不猜
		}
		win := Window{Key: x.Key, Pct: *x.Pct}
		if t, err := time.Parse(time.RFC3339, x.Reset); err == nil {
			win.Reset = t
		}
		out = append(out, win)
	}
	return out, true
}
