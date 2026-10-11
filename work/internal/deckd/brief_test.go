package deckd

import (
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// n051 Codex 端到端实测的两处。

// Codex 用登录 shell 跑命令，PATH 里未必有 deck：brief 里写 deck 的绝对路径。
func TestBriefsUseDeckCommandPath(t *testing.T) {
	old := DeckCommand
	t.Cleanup(func() { DeckCommand = old })
	DeckCommand = "/opt/deck/bin/deck"
	p := deckstore.ProjectDef{ID: "alpha", Name: "Alpha", Path: "/tmp/alpha", Envelope: deckstore.DefaultEnvelope("alpha")}
	mb := MainBrief(p)
	for _, want := range []string{"/opt/deck/bin/deck agent spawn", "/opt/deck/bin/deck agent escalate", "/opt/deck/bin/deck agent decisions"} {
		if !strings.Contains(mb, want) {
			t.Fatalf("主 agent 的 brief 应写绝对路径 %q：\n%s", want, mb)
		}
	}
	if sb := subBrief(p, deckstore.Session{Title: "x"}, "做事"); !strings.Contains(sb, "/opt/deck/bin/deck agent report --done") {
		t.Fatalf("子 agent 的 brief 应写绝对路径：\n%s", sb)
	}
	DeckCommand = "deck"
	if !strings.Contains(MainBrief(p), "：deck agent spawn") {
		t.Fatal("默认仍写 deck")
	}
}

// 上游送完 brief 但证实不了提交（Codex 实测 delivery=unverified）：打字已结束，主 agent 照常放开输入，记下并通知 owner。
func TestMainBriefUnverifiedStillOpensInputAndNotifies(t *testing.T) {
	w := newAgentWorld(t)
	set := w.store.Settings()
	g := set.Projects[0]
	g.ID, g.Name, g.Path = "gamma", "Gamma", "/tmp/gamma"
	g.Envelope = deckstore.DefaultEnvelope("gamma")
	set.Projects = append(set.Projects, g)
	if err := w.store.ReplaceSettings(set); err != nil {
		t.Fatal(err)
	}
	w.up.sendErr = &agentdeck.ErrDeliveryUnconfirmed{SessionID: "x", Detail: "delivery=unverified"}
	code, b := w.do("owner", "POST", "/v1/projects/gamma/main", `{}`)
	if code != 201 {
		t.Fatalf("起主 agent：%d %s", code, b)
	}
	sess := decode(t, b)["session"].(map[string]any)
	if sess["brief_confirmed"] != true || !strings.Contains(sess["note"].(string), "没证实提交") {
		t.Fatalf("送完但没证实：放开输入并记下：%v", sess)
	}
	ns := w.store.Notifications(0, "gamma")
	found := false
	for _, n := range ns {
		found = found || strings.Contains(n.Text, "看一眼")
	}
	if !found {
		t.Fatal("应请 owner 看一眼")
	}
	// 明确失败（不是「没证实」）照旧不放开
	w.up.sendErr = errorString("upstream refused")
	g2 := g
	g2.ID, g2.Name, g2.Path = "delta", "Delta", "/tmp/delta"
	g2.Envelope = deckstore.DefaultEnvelope("delta")
	set = w.store.Settings()
	set.Projects = append(set.Projects, g2)
	w.store.ReplaceSettings(set)
	_, b = w.do("owner", "POST", "/v1/projects/delta/main", `{}`)
	if sess := decode(t, b)["session"].(map[string]any); sess["brief_confirmed"] == true {
		t.Fatalf("brief 明确失败：不放开：%v", sess)
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }
