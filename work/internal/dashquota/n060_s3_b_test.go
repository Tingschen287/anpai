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

	"github.com/Tingschen287/terminal-deck/work/internal/wirecheck"
)

// 段 3 必改 1（第 2 段复审 4）：看板响应与额度缓存文件里 JSON 转义写法的伪造标记，解码之后也要中和。
const esc = `\ue000\ue001\ue002\ue003\ue004`

func TestN060S3EscapedDashboardResponse(t *testing.T) {
	now := time.Date(2026, 10, 14, 15, 0, 0, 0, time.Local)
	body := fmt.Sprintf(`{"generated":%q,
 "profiles":[{"key":"oc`+esc+`","zen_limit":{"model":"m`+esc+`","avg_tokens":1,"samples":1}},{"key":"codex","quota":{"windows":[{"key":"5h`+esc+`","pct":35}]}}],
 "quota":{"antigravity":{"rows":[{"name":"pool `+esc+`","windows":[{"key":"w`+esc+`","pct":10}]}]},"cco":{"windows":[{"key":"k`+esc+`","pct":9}]}},
 "models":[{"date":"2026-10-14","profile":"oc`+esc+`","model":"m`+esc+`","incr":1}]}`, now.Add(-time.Minute).Format(time.RFC3339))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
	defer srv.Close()
	c := &Client{URL: srv.URL, Now: func() time.Time { return now }, ClaudeUsagePath: "/nonexistent"}
	p, err := c.fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Profiles) != 2 || len(p.Quota) != 2 {
		t.Fatalf("没解析出内容，测试没有测到东西：%+v", p)
	}
	if hits := wirecheck.MarksIn(p); len(hits) > 0 {
		t.Errorf("看板响应解码之后还有标记：%v", hits)
	}
	// 面板拿到的额度（含 Antigravity 池名这种从 RawMessage 里二次解出来的字段）
	got := c.Display(context.Background())
	var ag bool
	for _, q := range got {
		ag = ag || q.Tool == "antigravity"
	}
	if !ag {
		t.Fatalf("Antigravity 池没解出来，测试没有测到东西：%+v", got)
	}
	if hits := wirecheck.MarksIn(got); len(hits) > 0 {
		t.Errorf("面板额度里还有标记：%v", hits)
	}
}

func TestN060S3EscapedUsageCacheFile(t *testing.T) {
	now := time.Date(2026, 10, 14, 15, 0, 0, 0, time.Local)
	path := filepath.Join(t.TempDir(), "usage.json")
	doc := fmt.Sprintf(`{"five_hour":{"utilization":12.5,"resets_at":%q},"seven_day":{"utilization":3,"resets_at":%q},"note":"n`+esc+`"}`,
		now.Add(time.Hour).Format(time.RFC3339), now.Add(24*time.Hour).Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Client{Now: func() time.Time { return now }, ClaudeUsagePath: path}
	wins, ok := c.claudeStatusline()
	if !ok || len(wins) != 2 {
		t.Fatalf("缓存没读出来，测试没有测到东西：%v %v", wins, ok)
	}
	if hits := wirecheck.MarksIn(wins); len(hits) > 0 {
		t.Errorf("缓存文件里的值还有标记：%v", hits)
	}
}
