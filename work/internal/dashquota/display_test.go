package dashquota

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// n056（owner 2026-10-14 第二轮）：面板加 Antigravity（各额度池一条）、隐藏 Kimi、Claude 读状态栏缓存；这些都不进额度门的 Tool。
func TestDisplaySourcesAndOrder(t *testing.T) {
	now := time.Date(2026, 10, 14, 16, 0, 0, 0, time.Local)
	body := fmt.Sprintf(`{"generated":%q,
 "profiles":[{"key":"cco"},{"key":"codex","quota":{"windows":[{"key":"5h","pct":9}]}}],
 "quota":{"kimi":{"windows":[{"key":"5h","pct":1}]},"grok":{"windows":[{"key":"week","pct":19}]},
  "antigravity":{"plan":"Google AI Pro","rows":[
   {"name":"Gemini","windows":[{"key":"5h","pct":17,"remain":83,"reset":"2026-10-14T07:42:46Z"},{"key":"week","pct":5}]},
   {"name":"Claude","windows":[{"key":"5h","pct":0},{"key":"week","pct":0}]},
   {"name":"空","windows":[{"key":"5h"}]}]}}}`, now.Add(-time.Minute).Format(time.RFC3339))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
	defer srv.Close()
	cache := filepath.Join(t.TempDir(), "statusline-usage-cache.json")
	os.WriteFile(cache, []byte(`{"five_hour":{"utilization":35.0,"resets_at":"2026-10-14T09:29:59.861235+00:00"},
 "seven_day":{"utilization":64.4,"resets_at":"2026-10-17T09:59:59+00:00"},"seven_day_opus":null,
 "extra_usage":{"is_enabled":false},"flag":true,"count":3,"note":"x","list":[1,2]}`), 0o600) // 真实缓存里有各种类型的别的字段
	os.Chtimes(cache, now, now)
	c := &Client{URL: srv.URL, Now: func() time.Time { return now }, ClaudeUsagePath: cache}

	var got []string
	byLabel := map[string]ToolQuota{}
	for _, q := range c.Display(context.Background()) {
		key := q.Tool
		if q.Label != "" {
			key = q.Label
		}
		got = append(got, key)
		byLabel[key] = q
	}
	want := []string{"claude", "codex", "grok", "Antigravity Gemini", "Antigravity Claude", "opencode"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("顺序与来源（kimi 隐藏、Antigravity 每池一条、没有百分比的池不出）：%v", got)
	}
	cl := byLabel["claude"]
	if cl.Status != Available || len(cl.Windows) != 2 || cl.Windows[0].Key != "5h" || cl.Windows[0].Pct != 35 || cl.Windows[1].Pct != 64 ||
		cl.Source == "" || !cl.Windows[0].Reset.Equal(time.Date(2026, 10, 14, 9, 29, 59, 861235000, time.UTC)) {
		t.Fatalf("Claude 读状态栏缓存：%+v", cl)
	}
	if g := byLabel["Antigravity Gemini"]; g.Status != Available || len(g.Windows) != 2 || g.Windows[0].Pct != 17 || g.Windows[0].Reset.IsZero() {
		t.Fatalf("Antigravity Gemini：%+v", g)
	}
	// 额度门用的 Tool 不受影响：Claude 仍未知
	if q := c.Tool(context.Background(), "claude"); q.Status != Unknown || len(q.Windows) != 0 {
		t.Fatalf("Tool 不读状态栏缓存：%+v", q)
	}
	// 已过重置时刻的窗口：不显示上个周期的数
	os.WriteFile(cache, []byte(`{"five_hour":{"utilization":97,"resets_at":"2026-10-14T07:00:00+00:00"},"seven_day":{"utilization":64,"resets_at":"2026-10-17T09:59:59+00:00"}}`), 0o600)
	os.Chtimes(cache, now, now)
	for _, q := range c.Display(context.Background()) {
		if q.Tool == "claude" && (len(q.Windows) != 1 || q.Windows[0].Key != "week" || q.Status != Available) {
			t.Fatalf("已过重置时刻的 5h 不显示、不按旧数判用尽：%+v", q)
		}
	}
	// 缓存超过一天：不用
	old := now.Add(-25 * time.Hour)
	os.Chtimes(cache, old, old)
	for _, q := range c.Display(context.Background()) {
		if q.Tool == "claude" && q.Status != Unknown {
			t.Fatalf("过期缓存不用：%+v", q)
		}
	}
	// 没有缓存文件：Claude 未知（面板不显示）
	c.ClaudeUsagePath = filepath.Join(t.TempDir(), "none.json")
	for _, q := range c.Display(context.Background()) {
		if q.Tool == "claude" && q.Status != Unknown {
			t.Fatalf("%+v", q)
		}
	}
}

func TestClaudeUsagePathFollowsConfigDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if p := ClaudeUsagePath(); p != "/tmp/claude/.claude/statusline-usage-cache.json" {
		t.Fatal(p)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "/home/x/.claude-work")
	if p := ClaudeUsagePath(); p != "/tmp/claude/.claude-work/statusline-usage-cache.json" {
		t.Fatal(p)
	}
}
