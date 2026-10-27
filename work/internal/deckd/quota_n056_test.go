package deckd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/dashquota"
)

// n056 自查：OpenCode 的撞墙日均值只给面板显示（标估算），deckd 的额度门照旧按「未知」处理，frontier 子 agent 仍上报 owner。
func TestEstimatedOpenCodeQuotaStillGatesFrontier(t *testing.T) {
	now := time.Now()
	body := fmt.Sprintf(`{"generated":%q,"profiles":[{"key":"oc","zen_limit":{"model":"m","avg_tokens":1000000,"samples":5}}],"quota":{},
 "models":[{"date":%q,"profile":"oc","model":"m","incr":10}]}`, now.Format(time.RFC3339), now.Format("2006-01-02"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
	defer srv.Close()
	w := newAgentWorld(t)
	w.srv.agents.quotaOf = nil
	// Claude 的状态栏缓存用测试自己的夹具文件：原来读本机真实的 /tmp/claude/…，本机没有 Claude Code 缓存时
	// 响应里就没有 source 字段，wirecheck 里对 source 的例外会被判过期（Mac 上暴露，n067）
	cache := filepath.Join(t.TempDir(), "statusline-usage-cache.json")
	usage := fmt.Sprintf(`{"five_hour":{"utilization":35.0,"resets_at":%q},"seven_day":{"utilization":64.4,"resets_at":%q}}`,
		now.Add(2*time.Hour).Format(time.RFC3339), now.Add(48*time.Hour).Format(time.RFC3339)) // 重置时间要在未来，过去的会被当成上个周期丢掉
	if err := os.WriteFile(cache, []byte(usage), 0o600); err != nil {
		t.Fatal(err)
	}
	w.srv.quota = &dashquota.Client{URL: srv.URL, ClaudeUsagePath: cache}
	if !w.srv.quotaUnsure(context.Background(), "opencode") {
		t.Fatal("估算的 OpenCode 额度不能放开额度门")
	}
	code, b := w.do("owner", "GET", "/v1/quota", "")
	if code != 200 || !strings.Contains(b, `"source":`) || !strings.Contains(b, `"tool":"opencode"`) || !strings.Contains(b, `"estimated":true`) || !strings.Contains(b, `"key":"day"`) {
		t.Fatalf("面板照样拿到估算窗口：%d %s", code, b)
	}
}
