package deckui

import (
	"image/color"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/logo"
)

// n069：iTerm2 上的 logo。设了合成背景（deck up 在 iTerm2 上写的）侧栏用不留空洞的画法；没设就是 n056 在 Windows Terminal 上验收的透明画法。

func TestSidebarSixelOpaqueWhenBackgroundSet(t *testing.T) {
	t.Cleanup(func() { logo.SetBackground(nil) })
	bg := color.RGBA{20, 20, 24, 255}
	logo.SetBackground(&bg)
	sb := &Sidebar{Data: sample(), Mode: "sixel", CellW: 16, CellH: 32}
	f := sb.Render(40, 30)
	if len(f.Images) < 2 {
		t.Fatalf("应有标题与会话的图：%d", len(f.Images))
	}
	var selected int
	for _, im := range f.Images {
		if !strings.HasPrefix(im.Data, "\x1bP0;0;0q\"1;1;") {
			t.Fatalf("设了背景应走合成画法（P2=0，每个像素都画）：%q", im.Data[:24])
		}
		// 背景寄存器 0：普通行是终端背景（20,20,24 → 8%,8%,9%）；选中的行是行底色（48,48,48 → 19%）
		switch {
		case strings.Contains(im.Data, "#0;2;8;8;9"):
		case strings.Contains(im.Data, "#0;2;19;19;19"):
			selected++
		default:
			t.Fatalf("背景寄存器不对：%q", im.Data[:60])
		}
	}
	if selected != 1 {
		t.Fatalf("选中的那一行（且只有它）的图应合成到行底色上：%d", selected)
	}
	// 标题行的 deck 图标不在选中行上，用终端背景
	if !strings.Contains(f.Images[0].Data, "#0;2;8;8;9") {
		t.Fatal("标题图标用终端背景")
	}
	// 选中项目标题时（Proj 不空）没有会话行被选中
	sb2 := &Sidebar{Data: sample(), Mode: "sixel", CellW: 16, CellH: 32}
	sb2.Proj = sb2.Data.Groups()[0].ID
	for _, im := range sb2.Render(40, 30).Images {
		if strings.Contains(im.Data, "#0;2;19;19;19") {
			t.Fatal("选中的是项目标题，没有会话行的图该用行底色")
		}
	}
}

func TestSidebarSixelTransparentWithoutBackgroundIsUnchanged(t *testing.T) {
	logo.SetBackground(nil)
	sb := &Sidebar{Data: sample(), Mode: "sixel", CellW: 9, CellH: 18}
	for _, im := range sb.Render(40, 30).Images {
		if !strings.HasPrefix(im.Data, "\x1bP0;1;0q\"1;1;") {
			t.Fatalf("没设背景应是原来的透明画法（n056）：%q", im.Data[:24])
		}
	}
}

// 背景色变了（deck up 在另一个终端里重连、改了主题），缓存的 sixel 不能沿用旧背景。
func TestSidebarSixelCacheFollowsBackground(t *testing.T) {
	t.Cleanup(func() { logo.SetBackground(nil) })
	sb := &Sidebar{Data: sample(), Mode: "sixel", CellW: 16, CellH: 32}
	logo.SetBackground(&color.RGBA{0, 0, 0, 255})
	a := sb.deckIcon()
	logo.SetBackground(&color.RGBA{255, 255, 255, 255})
	b := sb.deckIcon()
	logo.SetBackground(nil)
	c := sb.deckIcon()
	if a == b || a == c || b == c || !strings.Contains(a, "#0;2;0;0;0") || !strings.Contains(b, "#0;2;100;100;100") {
		t.Fatal("三种背景应各画各的")
	}
}

// 标题与格子像素：像素未知（0）时按 10x20；很窄的侧栏也不崩。
func TestSidebarSixelUnknownCellPixelsAndNarrow(t *testing.T) {
	t.Cleanup(func() { logo.SetBackground(nil) })
	logo.SetBackground(&color.RGBA{0, 0, 0, 255})
	for _, w := range []int{4, 10, 20, 32} {
		sb := &Sidebar{Data: sample(), Mode: "sixel"} // CellW、CellH 都是 0
		f := sb.Render(w, 12)
		if len(f.Lines) != 12 {
			t.Fatalf("宽 %d：行数 %d", w, len(f.Lines))
		}
		for _, im := range f.Images {
			if !strings.HasPrefix(im.Data, "\x1bP0;0;0q\"1;1;") {
				t.Fatalf("宽 %d：%q", w, im.Data[:20])
			}
		}
	}
}

func TestBrandIn(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	// 不受进程当前语言影响：deck up 自己永远中文，标题要按设置里的语言取
	for lang, want := range map[string]string{i18n.ZH: "安排 Anpai", i18n.EN: "Anpai", "": "安排 Anpai", "fr": "安排 Anpai"} {
		if got := BrandIn(lang); got != want {
			t.Errorf("BrandIn(%q)=%q，应为 %q", lang, got, want)
		}
	}
	i18n.SetLang(i18n.EN)
	if BrandIn(i18n.ZH) != "安排 Anpai" || BrandIn(i18n.EN) != "Anpai" {
		t.Error("BrandIn 与进程语言无关")
	}
}
