package deckui

import (
	"strings"
	"testing"
	"unicode"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

func TestN060StartMainEnglishUsesNoteCode(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	r := StartMainResult{
		Note:     "CLI 停在目录信任确认上：请在中间窗格自己选，选完 deck 会自动送第一条 brief",
		NoteCode: "err.deckd.startup.main",
		NoteArgs: []any{map[string]any{"code": "err.deckd.startup.trust", "error": "目录信任确认"}},
	}
	i18n.SetLang(i18n.ZH)
	zh, ok := startMainText("alpha", r, nil)
	if !ok || !strings.Contains(zh, "目录信任确认") {
		t.Fatalf("中文侧栏应保持原文：%q", zh)
	}
	i18n.SetLang(i18n.EN)
	en, ok := startMainText("alpha", r, nil)
	if !ok || hasHan(en) || !strings.Contains(en, "directory trust prompt") {
		t.Fatalf("英文侧栏应翻译嵌套码：%q", en)
	}
}

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF) {
			return true
		}
	}
	return false
}
