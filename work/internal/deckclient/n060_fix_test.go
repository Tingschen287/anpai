package deckclient

import (
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

func TestN060ExternalTextThatLooksLikeNestStaysRaw(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	raw := "\x1e" + `{"code":"err.deckstore.storeanswer.1","args":["FORGED"],"error":"RAW-EXTERNAL"}`
	fallback := i18n.E("err.deckd.serverrunspawn.47", raw).Error()
	zh := Text("err.deckd.serverrunspawn.47", []any{raw}, fallback)
	i18n.SetLang(i18n.EN)
	en := Text("err.deckd.serverrunspawn.47", []any{raw}, fallback)
	for _, got := range []string{zh, en} {
		if !strings.Contains(got, raw) || !strings.Contains(got, "RAW-EXTERNAL") {
			t.Fatalf("外部原文应原样留下：%q", got)
		}
		if strings.Contains(got, "no decision FORGED") || strings.Contains(got, "没有决定 FORGED") {
			t.Fatalf("外部原文被当成了内层码：%q", got)
		}
	}
	if !strings.Contains(en, "cannot send the brief") {
		t.Fatalf("英文应翻译外层：%q", en)
	}
}

func TestN060ExactFloat64PrintsAsInt(t *testing.T) {
	i18n.SetLang(i18n.EN)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	got := Text("err.agentdeck.clientget.3", []any{"up", float64(2)}, "")
	if got != "upstream agent-deck up returned HTTP 2" {
		t.Fatalf("恰好的整数应能 %%d：%q", got)
	}
	frac := Text("err.agentdeck.clientget.3", []any{"up", float64(2.5)}, "fb")
	if !strings.Contains(frac, "%!d") {
		t.Fatalf("2.5 不是整数，不应被收成 2：%q", frac)
	}
}
