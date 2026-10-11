package deckd

import (
	"errors"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// n051 真模型端到端实测：agent-deck v1.15 默认给 Claude 加 --dangerously-skip-permissions，deck 自己的参数检查看不到。

func TestUpstreamConfigBypassRejectedBeforeLaunch(t *testing.T) {
	w := newAgentWorld(t)
	w.cfgBypass = map[string]error{"claude": errors.New("agent-deck 会给 Claude Code 加 --dangerously-skip-permissions")}
	before := w.store.Slots("alpha")
	code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "x", "t", "c1", "/tmp/alpha", "", "claude", "claude-sonnet-5"))
	if code != 400 || !strings.Contains(b, "dangerously") {
		t.Fatalf("上游配置会加绕过参数：硬拒绝：%d %s", code, b)
	}
	code, b = w.do("owner", "POST", "/v1/projects/alpha/main", `{}`)
	if code != 400 || !strings.Contains(b, "dangerously") {
		t.Fatalf("起主 agent 同样硬拒绝：%d %s", code, b)
	}
	if len(w.up.launches) != 0 {
		t.Fatal("拒绝时不应启动")
	}
	if u := w.store.Slots("alpha"); u != before {
		t.Fatalf("拒绝时不占名额：%+v → %+v", before, u)
	}
}

func TestBypassChecksMissingRefuses(t *testing.T) {
	w := newAgentWorld(t)
	w.srv.WithBypassChecks(BypassChecks{})
	if code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "x", "t", "c2", "/tmp/alpha", "", "claude", "claude-sonnet-5")); code != 400 || !strings.Contains(b, "确认不了") {
		t.Fatalf("没接上绕过检查就不开：%d %s", code, b)
	}
}

func TestProcessBypassStopsSessionWithoutBrief(t *testing.T) {
	w := newAgentWorld(t)
	w.procBypass = errors.New("进程 42 的命令行带着绕过参数")
	before := w.store.Slots("alpha")
	code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "带绕过", "t", "c3", "/tmp/alpha", "", "claude", "claude-sonnet-5"))
	if code != 502 || !strings.Contains(b, "已停掉") {
		t.Fatalf("启动后发现绕过参数：停掉并报错：%d %s", code, b)
	}
	if len(w.up.stops) != 1 || len(w.up.sends) != 0 {
		t.Fatalf("应停掉会话且不送 brief：stops=%v sends=%v", w.up.stops, w.up.sends)
	}
	for _, x := range w.store.Sessions("alpha") {
		if x.Title == "带绕过" && (x.Status == deckstore.StatusRunning || x.Status == deckstore.StatusUncertain) {
			t.Fatalf("停成了：会话不应还算在跑：%s", x.Status)
		}
	}
	if u := w.store.Slots("alpha"); u != before {
		t.Fatalf("停成了释放名额：%+v → %+v", before, u)
	}
	ns := w.store.Notifications(0, "alpha")
	if !strings.Contains(ns[len(ns)-1].Text, "跳过权限") {
		t.Fatalf("应通知 owner：%v", ns[len(ns)-1])
	}
}

func TestProcessBypassStopFailsKeepsSlotUncertain(t *testing.T) {
	w := newAgentWorld(t)
	w.procBypass = errors.New("核对不了")
	w.up.stopErr = errors.New("上游停不了")
	before := w.store.Slots("alpha")
	w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "停不了", "t", "c4", "/tmp/alpha", "", "claude", "claude-sonnet-5"))
	held := false
	for _, x := range w.store.Sessions("alpha") {
		if x.Title == "停不了" {
			held = x.Status == deckstore.StatusUncertain
		}
	}
	if u := w.store.Slots("alpha"); u.Subagents != before.Subagents+1 {
		t.Fatalf("没停成继续占名额：%+v → %+v", before, u)
	}
	if !held || len(w.up.sends) != 0 {
		t.Fatalf("没停成：标结果不明、继续占名额、不送 brief：held=%v sends=%v", held, w.up.sends)
	}
	ns := w.store.Notifications(0, "alpha")
	if !strings.Contains(ns[len(ns)-1].Text, "立即处理") {
		t.Fatalf("应请 owner 立即处理：%v", ns[len(ns)-1])
	}
}
