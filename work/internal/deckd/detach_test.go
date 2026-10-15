package deckd

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// n051 端到端实测：真实 Claude Code 起来加送 brief 超过客户端的等待上限，客户端一断，进行中的 session send 被杀，
// 会话留下却没有 brief。调用方断开后，启动与送 brief 应照常做完。
func TestSpawnCompletesAfterClientGivesUp(t *testing.T) {
	w := newAgentWorld(t)
	release := make(chan struct{})
	w.up.sendHook = func(ctx context.Context) {
		select {
		case <-release:
		case <-time.After(3 * time.Second):
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", w.http.URL+"/v1/agents",
		strings.NewReader(spawnJSON("balanced", "慢启动", "t", "slow1", "/tmp/alpha", "", "claude", "claude-sonnet-5-5")))
	req.Header.Set("X-Test-As", "main")
	if _, err := http.DefaultClient.Do(req); err == nil {
		t.Fatal("客户端应先超时")
	}
	time.Sleep(100 * time.Millisecond)
	close(release) // 上游此时才就绪
	ok := false
	for i := 0; i < 100 && !ok; i++ {
		w.up.mu.Lock()
		for _, x := range w.up.sends {
			ok = ok || strings.Contains(x, "慢启动")
		}
		w.up.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	if !ok {
		t.Fatal("调用方断开后 brief 仍应送达")
	}
	time.Sleep(50 * time.Millisecond)
	for _, x := range w.store.Sessions("alpha") {
		if x.Title == "慢启动" && strings.Contains(x.Note, "brief") {
			t.Fatalf("不应记 brief 失败：%s", x.Note)
		}
	}
}
