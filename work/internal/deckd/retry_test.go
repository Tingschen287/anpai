package deckd

import (
	"errors"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
)

// n050 评审 4 建议（README 的预留合同）：启动失败或结果不明后，同一请求 id 不能再用，换新 id 重试。
func TestRetryAfterFailureNeedsNewRequestID(t *testing.T) {
	w := newAgentWorld(t)
	w.up.launchErr = errors.New("upstream refused")
	w.up.launchOut = &agentdeck.LaunchResult{Error: "boom", Code: "X"}
	if code, _ := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "x", "t", "same", "/tmp/alpha", "", "claude", "claude-sonnet-5-5")); code != 502 {
		t.Fatalf("第一次启动失败：%d", code)
	}
	w.up.launchErr, w.up.launchOut = nil, nil
	if code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "x", "t", "same", "/tmp/alpha", "", "claude", "claude-sonnet-5-5")); code != 409 || !strings.Contains(b, "已经预留过") {
		t.Fatalf("同一请求 id 不能再用：%d %s", code, b)
	}
	if code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "x", "t", "fresh", "/tmp/alpha", "", "claude", "claude-sonnet-5-5")); code != 201 {
		t.Fatalf("新请求 id 应能开：%d %s", code, b)
	}
}
