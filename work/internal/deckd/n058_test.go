package deckd

import (
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// n058（adr_032）：产品公开名改为「安排 Anpai」。主 agent 与子 agent 读到的 brief、转告前缀里不再出现旧名。
func TestBriefsAndReportsUseAnpaiName(t *testing.T) {
	p := deckstore.ProjectDef{ID: "alpha", Name: "Alpha", Path: "/tmp/alpha", Envelope: deckstore.DefaultEnvelope("alpha")}
	mb := MainBrief(p)
	sb := subBrief(p, deckstore.Session{Title: "x"}, "做事")
	if !strings.Contains(mb, "通过「安排 Anpai」工作") || !strings.Contains(sb, "通过「安排 Anpai」派出") {
		t.Fatalf("brief 应写「安排 Anpai」：\n%s\n%s", mb, sb)
	}
	for _, s := range []string{mb, sb} {
		if strings.Contains(s, "Agent Deck") {
			t.Fatalf("brief 里不应再有旧名：\n%s", s)
		}
	}
}

// 转告给主 agent 的前缀也用新名。
func TestReportTextUsesAnpaiName(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	text := w.srv.reportText(deckstore.Report{Sub: sub.Key, Source: deckstore.ReportInferred, Text: "做完了"})
	if !strings.HasPrefix(text, "［安排 Anpai 转告·推断提醒］") || strings.Contains(text, "Agent Deck") {
		t.Fatalf("转告前缀应是「安排 Anpai」：%q", text)
	}
}
