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
