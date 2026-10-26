package deckd

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// n060 段 3 返工 4（Codex 复审 3 的 R3-2）：deckd 的 /v1/tools 给出的工具版本本来就是 Clean 过的（带真打码标记），
// 界面消费它时要走保留标记的显示出口，不能再经过入口中和的 sanitizeFull。这条测试守住「来源确实已 Clean」这个前提：
// 如果以后 deckd 不再 Clean 版本，界面那边的假设就要重新核对。
func TestN060S3Fix4RealToolsVersionIsAlreadyCleaned(t *testing.T) {
	f := newFixture(t)
	f.srv.WithAgents(nil, nil, func(context.Context, string) (string, string, error) {
		return "/tmp/fake-tool", "tool 1.2 token=abcdefghijklmnop", nil
	}, nil)
	r := httptest.NewRequest("GET", "/v1/tools", nil)
	r.Header.Set("X-Test-As", "owner")
	r.Header.Set(i18n.MarkHeader, i18n.MarkNeutral)
	w := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(w, r)
	var out []ToolStatus
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(out) == 0 {
		t.Fatalf("tools HTTP=%d", w.Code)
	}
	for _, v := range out {
		if !v.Installed {
			continue
		}
		if !i18n.HasMark(v.Version) {
			t.Errorf("deckd 给的工具版本应已 Clean（带真打码标记）：%+v", v)
		}
	}
}
