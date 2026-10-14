package deckd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	w.srv.quota = &dashquota.Client{URL: srv.URL}
	if !w.srv.quotaUnsure(context.Background(), "opencode") {
		t.Fatal("估算的 OpenCode 额度不能放开额度门")
	}
	code, b := w.do("owner", "GET", "/v1/quota", "")
	if code != 200 || !strings.Contains(b, `"tool":"opencode"`) || !strings.Contains(b, `"estimated":true`) || !strings.Contains(b, `"key":"day"`) {
		t.Fatalf("面板照样拿到估算窗口：%d %s", code, b)
	}
}
