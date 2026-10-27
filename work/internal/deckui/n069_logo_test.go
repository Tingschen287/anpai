package deckui

import (
	"context"
	"errors"
	"image/color"
	"strings"
	"sync"
	"testing"
	"time"

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

// 界面语言换了，侧栏把终端标签标题改成新语言的品牌名；同一语言不重复设；设失败下一拍重试；路上又换了语言最终收敛。
func TestTitleSyncFollowsLanguage(t *testing.T) {
	var mu sync.Mutex
	var got []string
	fail := false
	release := make(chan struct{}, 8)
	ts := &titleSync{sock: "s", done: i18n.ZH, set: func(ctx context.Context, sock, title string) error {
		mu.Lock()
		got = append(got, title)
		f := fail
		mu.Unlock()
		<-release
		if f {
			return errors.New("布局不可用")
		}
		return nil
	}}
	wait := func(n int) {
		t.Helper()
		for i := 0; i < 200; i++ {
			mu.Lock()
			l := len(got)
			mu.Unlock()
			if l >= n {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("等不到第 %d 次设标题：%v", n, got)
	}
	settle := func() {
		for i := 0; i < 200; i++ {
			ts.mu.Lock()
			b := ts.busy
			ts.mu.Unlock()
			if !b {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("一直在路上")
	}
	ts.tick(i18n.ZH) // 起动时的语言，deck up 已经设好
	time.Sleep(50 * time.Millisecond)
	if len(got) != 0 {
		t.Fatalf("语言没变不该设：%v", got)
	}
	mu.Lock()
	fail = true
	mu.Unlock()
	ts.tick(i18n.EN)
	wait(1)
	release <- struct{}{}
	settle()
	ts.tick(i18n.EN) // 上次失败：下一拍重试
	wait(2)
	mu.Lock()
	fail = false
	mu.Unlock()
	release <- struct{}{}
	settle()
	ts.tick(i18n.EN) // 重试又失败了（fail 是在第二次发出时读的）；再来一拍
	wait(3)
	release <- struct{}{}
	settle()
	ts.tick(i18n.EN)
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	n := len(got)
	mu.Unlock()
	if n != 3 {
		t.Fatalf("成功之后同一语言不再设：%v", got)
	}
	if got[0] != "Anpai" || got[1] != "Anpai" {
		t.Fatalf("英文标题是 Anpai：%v", got)
	}
	// 在路上时换回中文：这一次回来后下一拍收敛到中文
	mu.Lock()
	fail = false
	mu.Unlock()
	ts.tick(i18n.ZH)
	wait(4)
	ts.tick(i18n.EN) // 路上又来一拍：忽略
	release <- struct{}{}
	settle()
	ts.tick(i18n.EN)
	wait(5)
	release <- struct{}{}
	settle()
	if got[3] != "安排 Anpai" || got[4] != "Anpai" {
		t.Fatalf("路上换回中文：先设中文，再收敛到当前的英文：%v", got)
	}
}
