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
	if (!found || len(wins) == 0) && tool == "opencode" {
		if w, ok := zenWindow(p, c.now()); ok {
			wins, found = []Window{w}, true
		}
	}
	if !found || len(wins) == 0 {
		q.Reason = "看板没有这个来源的额度窗口（例如 OpenCode 免费席位只有估算）"
		return q
	}
	q.Windows = wins
	th := c.Thresholds
	if th.Warn == 0 && th.Exhausted == 0 {
		th = DefaultThresholds
	}
	q.Status = Available
	for _, w := range wins {
		switch {
		case w.Pct >= th.Exhausted:
			q.Status = Exhausted
		case w.Pct >= th.Warn && q.Status != Exhausted:
			q.Status = Warn
		}
	}
	return q
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
