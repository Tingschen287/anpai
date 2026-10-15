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
