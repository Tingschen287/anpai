package deckd

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 会话运行状态：取到就用，取不到保留旧数据但过期后一律 unknown；没配上游（nil）也是 unknown。
func TestActivityCacheGoesUnknownWhenStale(t *testing.T) {
	now := time.Now()
	fail := false
	a := newActivityCache(func(context.Context) (map[string]string, error) {
		if fail {
			return nil, errors.New("上游没响应")
		}
		return map[string]string{"agentdeck_a": "running", "agentdeck_b": "waiting"}, nil
	})
	a.now = func() time.Time { return now }
	if a.of("agentdeck_a") != "unknown" {
		t.Fatal("还没取过应是 unknown")
	}
	a.refresh(context.Background())
	if a.of("agentdeck_a") != "running" || a.of("agentdeck_b") != "waiting" || a.of("agentdeck_c") != "unknown" || a.of("") != "unknown" {
		t.Fatal("取到后按 tmux 会话名给状态，不在菜单里的是 unknown")
	}
	fail = true
	now = now.Add(10 * time.Second)
	a.refresh(context.Background())
	if a.of("agentdeck_a") != "running" {
		t.Fatal("一次取不到不应立刻丢掉仍在有效期内的数据")
	}
	now = now.Add(10 * time.Second)
	if a.of("agentdeck_a") != "unknown" {
		t.Fatal("超过 15 秒没取到应是 unknown")
	}
	var none *activityCache
	if none.of("agentdeck_a") != "unknown" {
		t.Fatal("没配上游时应是 unknown")
	}
}
