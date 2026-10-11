package deckd

import (
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// n051 自查：主 agent 退出后，这个项目要能再起主 agent；还在（或确认不了）时不行。
func TestRestartMainAfterOldMainIsGone(t *testing.T) {
	w := newAgentWorld(t)
	old, _ := w.store.Session(w.main.Key)
	if old.TmuxSession == "" {
		t.Fatal("夹具里的主 agent 应有 tmux 会话名")
	}
	if code, _ := w.do("owner", "POST", "/v1/projects/alpha/main", `{}`); code != 409 {
		t.Fatalf("旧主 agent 还在：拒绝：%d", code)
	}
	// 确认不了（没接上存活检查）也拒绝
	alive := w.srv.agents.bypass.Alive
	w.srv.agents.bypass.Alive = nil
	w.gone = map[string]bool{old.TmuxSession: true}
	if code, _ := w.do("owner", "POST", "/v1/projects/alpha/main", `{}`); code != 409 {
		t.Fatalf("确认不了旧主 agent 已结束：拒绝：%d", code)
	}
	w.srv.agents.bypass.Alive = alive
	code, b := w.do("owner", "POST", "/v1/projects/alpha/main", `{}`)
	if code != 201 {
		t.Fatalf("旧主 agent 的会话已不在：应能再起：%d %s", code, b)
	}
	if x, _ := w.store.Session(old.Key); x.Status != deckstore.StatusStopped {
		t.Fatalf("旧主 agent 应记为已结束：%s", x.Status)
	}
	ns := w.store.Notifications(0, "alpha")
	found := false
	for _, n := range ns {
		found = found || strings.Contains(n.Text, "记为已结束")
	}
	if !found {
		t.Fatal("应通知旧主 agent 记为已结束")
	}
	// 新主 agent 还在：再起一次被拒
	if code, _ := w.do("owner", "POST", "/v1/projects/alpha/main", `{}`); code != 409 {
		t.Fatalf("新主 agent 还在：拒绝：%d", code)
	}
}
