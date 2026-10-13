package deckd

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// attempt 2 自查（blocker）：新会话空输入框里的灰色提示 Try "…" 不算 owner 的草稿，照常算就绪（与上游 guard.go 同一规则）。
func TestComposerPlaceholderIsNotDraft(t *testing.T) {
	ph := strings.Replace(claudeComposerScreen, "❯ ", `❯ Try "write a test for <filepath>"`, 1)
	if !ComposerReady("claude", ph) {
		t.Fatal(`Try "…" 是占位，不是草稿`)
	}
	half := strings.Replace(claudeComposerScreen, "❯ ", `❯ Try "这是我自己打的半句`, 1)
	if ComposerReady("claude", half) {
		t.Fatal("引号没收尾的是 owner 打的字")
	}
}

// attempt 2 自查（blocker）：list --json 随会话数变慢时，间隔按上次耗时放大（10 倍，最少 3 秒），状态不因此过期成「未知」；
// 取失败的原因记下、在 /v1/health 里看得到，成功后清空。
func TestActivityAdaptsToSlowList(t *testing.T) {
	calls := 0
	fail := false
	c := newActivityCache(func(context.Context) (map[string]string, error) {
		calls++
		time.Sleep(400 * time.Millisecond)
		if fail {
			return nil, errors.New("agent-deck 不在 PATH 上")
		}
		return map[string]string{"agentdeck_a": "running"}, nil
	})
	c.refresh(context.Background())
	if iv := c.interval(); iv < 4*time.Second {
		t.Fatalf("一次 0.4 秒：间隔至少 4 秒：%v", iv)
	}
	c.now = func() time.Time { return time.Now().Add(14 * time.Second) } // 超过默认 15 秒以内的一半；按 3 倍间隔仍有效
	if c.of("agentdeck_a") != "running" {
		t.Fatal("间隔放大后，有效期也跟着放大")
	}
	fail = true
	c.refresh(context.Background())
	if !strings.Contains(c.Err(), "PATH") {
		t.Fatalf("记下失败原因：%q", c.Err())
	}
	fail = false
	c.refresh(context.Background())
	if c.Err() != "" {
		t.Fatal("成功后清空")
	}
	w := newAgentWorld(t)
	w.srv.activity = newActivityCache(func(context.Context) (map[string]string, error) { return nil, errors.New("list 超时") })
	w.srv.activity.refresh(context.Background())
	if _, b := w.do("owner", "GET", "/v1/health", ""); !strings.Contains(b, "activity_error") || !strings.Contains(b, "list 超时") {
		t.Fatalf("健康检查里看得到：%s", b)
	}
}

// attempt 2 自查：等 owner 处理启动确认时 CLI 已经退出（例如选了 No, exit）：不再等，记为已停止、通知 owner。
func TestBriefWaitStopsWhenCLIExited(t *testing.T) {
	w := newAgentWorld(t)
	addGamma(t, w)
	w.up.setOutput(claudeTrustScreen)
	before := w.up.sendCount()
	if code, b := w.do("owner", "POST", "/v1/projects/gamma/main", `{}`); code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	x := mainOf(t, w, "gamma")
	w.gone = map[string]bool{x.TmuxSession: true}
	waitUntil(t, "记为已停止", func() bool {
		y, _ := w.store.Session(x.Key)
		return y.Status == deckstore.StatusStopped && !y.StartPending()
	})
	if w.up.sendCount() != before {
		t.Fatal("CLI 退出了：不送")
	}
}
