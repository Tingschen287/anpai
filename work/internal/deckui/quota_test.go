package deckui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/dashquota"
)

// n056：额度面板每个来源前贴 logo（sixel），位置在名字那一行的第 1 列；放不下两行就不贴；未知的来源不出现。
func TestQuotaFrameSixelLogos(t *testing.T) {
	qs := []dashquota.ToolQuota{
		{Tool: "claude", Status: dashquota.Unknown},
		{Tool: "codex", Status: dashquota.Available, Windows: []dashquota.Window{{Key: "5h", Pct: 9}, {Key: "week", Pct: 8}}},
		{Tool: "grok", Status: dashquota.Available, Windows: []dashquota.Window{{Key: "week", Pct: 19}}},
	}
	asked := map[string]bool{}
	sixelOf := func(tool string) string { asked[tool] = true; return "SIX-" + tool }
	f := QuotaFrame(qs, nil, 40, 20, "sixel", sixelOf)
	if len(f.Images) != 2 || asked["claude"] {
		t.Fatalf("只给显示出来的来源贴图：%+v %v", f.Images, asked)
	}
	for _, im := range f.Images {
		line := ansi.Strip(f.Lines[im.Row])
		if im.Col != 1 || !strings.HasPrefix(line, "      ") || !strings.Contains(line, map[string]string{"SIX-codex": "Codex", "SIX-grok": "Grok Build"}[im.Data]) {
			t.Fatalf("图应在名字那一行的第 1 列、位置留空：%+v %q", im, line)
		}
	}
	// 屏幕只够放下第一个来源的两行
	if f := QuotaFrame(qs, nil, 40, 3, "sixel", sixelOf); len(f.Images) != 1 {
		t.Fatalf("放不下两行就不贴：%+v", f.Images)
	}
	// 字母徽章：不贴图
	if f := QuotaFrame(qs, nil, 40, 20, "none", sixelOf); len(f.Images) != 0 {
		t.Fatal("none 不贴图")
	}
	// 取不到：只写原因
	f = QuotaFrame(qs, errors.New("连不上 deckd\x1b[31m"), 40, 20, "sixel", sixelOf)
	out := ansi.Strip(strings.Join(f.Lines, "\n"))
	if len(f.Images) != 0 || !strings.Contains(out, "取不到：连不上 deckd") || strings.Contains(out, "Codex") || strings.Contains(strings.Join(f.Lines, ""), "\x1b[31m") {
		t.Fatalf("%q", out)
	}
}
