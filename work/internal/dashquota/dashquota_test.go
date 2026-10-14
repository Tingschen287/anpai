package dashquota

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 形状取自 2026-10-10 13:37 的真实看板（数值改过）。
const sample = `{
 "generated": "%s",
 "profiles": [
  {"key":"cco"},{"key":"kimi"},
  {"key":"codex","quota":{"windows":[{"key":"5h","pct":35,"reset":"2026-10-10T15:11:07+08:00"},{"key":"week","pct":92,"reset":"2026-10-15T12:18:39+08:00"}]}},
  {"key":"grok"},{"key":"oc","zen_limit":{"avg_tokens":1}}
 ],
 "quota": {
  "cco":{"windows":[{"key":"5h","pct":9,"reset":"2026-10-10T08:00:00+00:00"},{"key":"week","pct":2,"reset":"2026-10-14T10:00:00+00:00"}]},
  "kimi":{"windows":[{"key":"5h","pct":0},{"key":"week","pct":25}]},
  "grok":{"windows":[{"key":"week","pct":78}]},
  "ccs":null
 }
}`

func serve(t *testing.T, generated time.Time) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/data" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, sample, generated.Format(time.RFC3339))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestStatusesFromDashboard(t *testing.T) {
	now := time.Date(2026, 10, 10, 13, 40, 0, 0, time.Local)
	c := &Client{URL: serve(t, now.Add(-3*time.Minute)).URL, Now: func() time.Time { return now }}
	want := map[string]string{"claude": Available, "codex": Exhausted, "grok": Warn, "kimi": Available, "opencode": Unknown}
	for _, q := range c.All(context.Background()) {
		if q.Status != want[q.Tool] {
			t.Errorf("%s 应为 %s，得到 %s（%s）", q.Tool, want[q.Tool], q.Status, q.Reason)
		}
	}
	if q := c.Tool(context.Background(), "codex"); len(q.Windows) != 2 || q.Windows[1].Pct != 92 {
		t.Errorf("codex 的窗口应来自 profiles：%+v", q.Windows)
	}
	if q := c.Tool(context.Background(), "opencode"); !strings.Contains(q.Reason, "估算") {
		t.Errorf("OpenCode 没有窗口时应说明原因：%q", q.Reason)
	}
}

func TestStaleDashboardIsUnknown(t *testing.T) {
	now := time.Date(2026, 10, 10, 13, 40, 0, 0, time.Local)
	c := &Client{URL: serve(t, now.Add(-30*time.Minute)).URL, Now: func() time.Time { return now }}
	q := c.Tool(context.Background(), "claude")
	if q.Status != Unknown || !strings.Contains(q.Reason, "没更新") {
		t.Fatalf("过期数据应为未知：%+v", q)
	}
}

func TestUnreachableDashboardIsUnknownNotInvented(t *testing.T) {
	c := &Client{URL: "http://127.0.0.1:1", HTTP: &http.Client{Timeout: 200 * time.Millisecond}}
	q := c.Tool(context.Background(), "claude")
	if q.Status != Unknown || len(q.Windows) != 0 || !strings.Contains(q.Reason, "取不到") {
		t.Fatalf("取不到时应为未知且不带数字：%+v", q)
	}
}

func TestThresholdsAreConfigurable(t *testing.T) {
	now := time.Date(2026, 10, 10, 13, 40, 0, 0, time.Local)
	c := &Client{URL: serve(t, now).URL, Now: func() time.Time { return now }, Thresholds: Thresholds{Warn: 20, Exhausted: 30}}
	if q := c.Tool(context.Background(), "kimi"); q.Status != Warn {
		t.Fatalf("阈值 20/30 下 kimi 周 25%% 应为告急：%+v", q)
	}
}

func TestResultsAreCached(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		fmt.Fprintf(w, sample, time.Now().Format(time.RFC3339))
	}))
	defer srv.Close()
	c := &Client{URL: srv.URL}
	c.All(context.Background())
	c.All(context.Background())
	if hits != 1 {
		t.Fatalf("60 秒内应只取一次，取了 %d 次", hits)
	}
}

// n056：OpenCode 按看板的撞墙均值算当天额度（与 calendar.js 同口径：今天该模型的增量 + 缓存读 + reasoning ÷ 均值）。
func TestOpenCodeZenDailyWindow(t *testing.T) {
	now := time.Date(2026, 10, 14, 15, 0, 0, 0, time.Local)
	body := fmt.Sprintf(`{"generated":%q,
 "profiles":[{"key":"oc","zen_limit":{"model":"muse-free","avg_tokens":63337176,"samples":7}}],
 "quota":{},
 "models":[
  {"date":"2026-10-14","profile":"oc","model":"muse-free","incr":1000000,"cache_read":30000000,"reasoning":668588,"output":999},
  {"date":"2026-10-14","profile":"oc","model":"other","incr":50000000},
  {"date":"2026-10-13","profile":"oc","model":"muse-free","incr":60000000},
  {"date":"2026-10-14","profile":"codex","model":"muse-free","incr":60000000}
 ]}`, now.Add(-time.Minute).Format(time.RFC3339))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
	defer srv.Close()
	c := &Client{URL: srv.URL, Now: func() time.Time { return now }}
	q := c.Tool(context.Background(), "opencode")
	if q.Status != Available || len(q.Windows) != 1 {
		t.Fatalf("%+v", q)
	}
	w := q.Windows[0]
	// (1000000+30000000+668588)/63337176 = 50%（output 不计；别的模型、别的日子、别的来源不计）
	if w.Key != "day" || w.Pct != 50 || !w.Reset.Equal(time.Date(2026, 10, 15, 0, 0, 0, 0, time.Local)) {
		t.Fatalf("%+v", w)
	}
}

func TestOpenCodeZenCapsAndMissingModel(t *testing.T) {
	now := time.Date(2026, 10, 14, 15, 0, 0, 0, time.Local)
	for _, tc := range []struct {
		zen    string
		status string
		pct    int
	}{
		{`{"model":"m","avg_tokens":100}`, Exhausted, 100}, // 超过均值：封顶 100
		{`{"avg_tokens":100}`, Unknown, 0},                 // 没有模型名：不算
		{`{"model":"m","avg_tokens":0}`, Unknown, 0},       // 没有均值：不算
	} {
		body := fmt.Sprintf(`{"generated":%q,"profiles":[{"key":"oc","zen_limit":%s}],"models":[{"date":"2026-10-14","profile":"oc","model":"m","incr":250}]}`,
			now.Format(time.RFC3339), tc.zen)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		c := &Client{URL: srv.URL, Now: func() time.Time { return now }}
		q := c.Tool(context.Background(), "opencode")
		srv.Close()
		if q.Status != tc.status || tc.status != Unknown && q.Windows[0].Pct != tc.pct {
			t.Errorf("%s：%+v", tc.zen, q)
		}
	}
}
