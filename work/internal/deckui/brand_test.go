package deckui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// n058（adr_032）：产品公开名是「安排 Anpai」，窄处只写「Anpai」，界面上不再出现「Agent Deck」。
func TestBrandForWidthRule(t *testing.T) {
	for _, c := range []struct {
		cols int
		want string
	}{{80, "安排 Anpai"}, {32, "安排 Anpai"}, {10, "安排 Anpai"}, {9, "Anpai"}, {5, "Anpai"}, {0, "Anpai"}} {
		if got := BrandFor(c.cols); got != c.want {
			t.Fatalf("BrandFor(%d)=%q，应为 %q", c.cols, got, c.want)
		}
	}
	if BrandName != "安排 Anpai" || BrandShort != "Anpai" || strings.Contains(BrandName+BrandShort, "Agent Deck") {
		t.Fatal("产品名常量不对")
	}
}

func TestSidebarTitleUsesAnpaiName(t *testing.T) {
	// 宽：完整的「安排 Anpai」，不含旧名；字母徽章模式前面 1 格，sixel 模式前面 4 格
	for _, c := range []struct{ mode, prefix string }{{"none", " "}, {"sixel", "    "}} {
		sb := &Sidebar{Data: sample(), Mode: c.mode, CellW: 9, CellH: 18}
		title := ansi.Strip(sb.Render(32, 30).Lines[0])
		if !strings.HasPrefix(title, c.prefix+"安排 Anpai") || strings.Contains(title, "Agent Deck") {
			t.Fatalf("%s 模式宽栏标题应是「安排 Anpai」：%q", c.mode, title)
		}
	}
	// 窄：放不下 10 列就只写「Anpai」
	narrow := &Sidebar{Data: sample(), Mode: "none"}
	title := ansi.Strip(narrow.Render(8, 30).Lines[0])
	if !strings.HasPrefix(title, " Anpai") || strings.Contains(title, "安排") || strings.Contains(title, "Agent Deck") {
		t.Fatalf("窄栏标题应是「Anpai」：%q", title)
	}
	narrowLogo := &Sidebar{Data: sample(), Mode: "sixel", CellW: 9, CellH: 18}
	title = ansi.Strip(narrowLogo.Render(12, 30).Lines[0])
	if !strings.HasPrefix(title, "    Anpai") || strings.Contains(title, "安排") {
		t.Fatalf("带 logo 的窄栏标题应是「Anpai」：%q", title)
	}
}

// n058：整屏重画时每行先用窄空格铺满再回行首，行首是双宽汉字时也能把压在格子上的旧 sixel 图清掉
// （e2e56「复用 sixel 布局、终端不支持 sixel」一步：不这样做，切成字母徽章后旧 logo 图留着、出占位）。
func TestEncodeBlanksEachLineBeforeDrawing(t *testing.T) {
	out := Frame{Lines: []string{" 安排 Anpai", "x"}}.Encode(12, 3)
	rows := strings.Split(strings.TrimPrefix(out, "\x1b[?25l\x1b[H"), "\r\n")
	if len(rows) != 3 {
		t.Fatalf("应有 3 行：%q", out)
	}
	for i, r := range rows {
		if !strings.HasPrefix(r, strings.Repeat(" ", 12)+"\r") {
			t.Fatalf("第 %d 行没先铺空格：%q", i, r)
		}
	}
	if !strings.Contains(rows[0], "\r 安排 Anpai") {
		t.Fatalf("铺完空格后写原文：%q", rows[0])
	}
}
