package deckui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"regexp"
	"strconv"
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
		// 背景色在最后一个寄存器：普通行是终端背景（20,20,24 → 8%,8%,9%）；选中的行是行底色（48,48,48 → 19%）
		switch {
		case bgOf(im.Data) == "8;8;9":
		case bgOf(im.Data) == "19;19;19":
			selected++
		default:
			t.Fatalf("背景寄存器不对：%q", im.Data[:60])
		}
	}
	if selected != 1 {
		t.Fatalf("选中的那一行（且只有它）的图应合成到行底色上：%d", selected)
	}
	// 标题行的 deck 图标不在选中行上，用终端背景
	if bgOf(f.Images[0].Data) != "8;8;9" {
		t.Fatal("标题图标用终端背景")
	}
	// 选中项目标题时（Proj 不空）没有会话行被选中
	sb2 := &Sidebar{Data: sample(), Mode: "sixel", CellW: 16, CellH: 32}
	sb2.Proj = sb2.Data.Groups()[0].ID
	for _, im := range sb2.Render(40, 30).Images {
		if bgOf(im.Data) == "19;19;19" {
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
	if a == b || a == c || b == c || bgOf(a) != "0;0;0" || bgOf(b) != "100;100;100" {
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

// bgOf 取一段 sixel 里最后定义的颜色寄存器（合成画法把背景色放在最后一个，tmux 再编码时补出的区域就是这个颜色）。
func bgOf(data string) string {
	last, val := -1, ""
	for _, m := range regexp.MustCompile(`#(\d+);2;(\d+;\d+;\d+)`).FindAllStringSubmatch(data, -1) {
		n, _ := strconv.Atoi(m[1])
		if n > last {
			last, val = n, m[2]
		}
	}
	return val
}

// 经 tmux 的横条（样张 9）：合成画法的图高取 6 的整数倍且不超过这几行的像素（logo.FitHeight），
// 否则 sixel 的 6 行一段让 32 行的图数据成了 36 行、超过 tmux 的一格，被占成两格并补齐。透明画法（WT）的尺寸一点不变。
func TestSidebarSixelHeightFitsWholeCellsOnlyWhenComposited(t *testing.T) {
	t.Cleanup(func() { logo.SetBackground(nil) })
	dims := func(sb *Sidebar) (rows []int) {
		for _, im := range sb.Render(40, 30).Images {
			var a, b, w, h int
			if _, err := fmt.Sscanf(im.Data[strings.Index(im.Data, "\""):], "\"%d;%d;%d;%d", &a, &b, &w, &h); err != nil {
				t.Fatalf("光栅属性：%v", err)
			}
			rows = append(rows, h)
		}
		return rows
	}
	for _, cell := range [][2]int{{16, 32}, {16, 38}, {9, 18}, {8, 19}} {
		sb := &Sidebar{Data: sample(), Mode: "sixel", CellW: cell[0], CellH: cell[1]}
		logo.SetBackground(nil)
		for _, h := range dims(sb) {
			if h != min(cell[1], 2*cell[0]) && h != min(2*cell[1], 4*cell[0]) {
				t.Fatalf("透明画法的尺寸要保持 n056 验收的：格子 %v 得到 %d", cell, h)
			}
		}
		bg := color.RGBA{16, 16, 16, 255}
		logo.SetBackground(&bg)
		sb = &Sidebar{Data: sample(), Mode: "sixel", CellW: cell[0], CellH: cell[1]}
		for _, h := range dims(sb) {
			if h%6 != 0 || (h > cell[1] && h > 2*cell[1]) || h > min(2*cell[1], 4*cell[0]) {
				t.Fatalf("合成画法：格子 %v 的图高 %d 应是 6 的倍数且不超过这几行", cell, h)
			}
		}
	}
	logo.SetBackground(nil)
	if logo.FitHeight(32) != 32 {
		t.Fatal("没设背景时原样")
	}
	bg := color.RGBA{1, 1, 1, 255}
	logo.SetBackground(&bg)
	for in, want := range map[int]int{32: 30, 64: 60, 38: 36, 76: 72, 5: 0, 6: 6, 0: 0} {
		if got := logo.FitHeight(in); got != want {
			t.Fatalf("FitHeight(%d)=%d，应为 %d", in, got, want)
		}
	}
	// 不足一个 sixel 段（6 像素）：不画，而不是画一个比预留还高的图
	if s := logo.Sixel(logo.Deck(), logo.FitHeight(5), logo.FitHeight(5)); s != "" {
		t.Fatalf("预留不足 6 像素时不该画图：%q", s[:min(len(s), 20)])
	}
}

// 额度面板的 logo（Codex 复审 2 的 R2-3）：合成画法下边长与画布高度一起取整，保证等比、总高是 6 的倍数、不越出预留行；
// 以前只取整高度，两行块在 16×32、8×16 的格子上被压扁 6%。
func TestQuotaLogoStaysSquareWhenComposited(t *testing.T) {
	t.Cleanup(func() { logo.SetBackground(nil) })
	for _, composited := range []bool{true, false} {
		if composited {
			bg := color.RGBA{16, 16, 16, 255}
			logo.SetBackground(&bg)
		} else {
			logo.SetBackground(nil)
		}
		for _, cell := range [][2]int{{16, 32}, {8, 16}, {16, 38}, {9, 18}, {10, 20}, {8, 19}, {7, 14}} {
			for _, rows := range []int{2, 3, 4} {
				side, height := quotaLogoSize(cell[0], cell[1], rows)
				if composited && (side%6 != 0 || height%6 != 0) {
					t.Fatalf("合成画法 %v %d 行：边长 %d、总高 %d 应都是 6 的倍数", cell, rows, side, height)
				}
				if height > rows*cell[1] || side > 4*cell[0] || side > 2*cell[1] || height < side {
					t.Fatalf("%v %d 行：边长 %d 总高 %d 越出预留（%d 行 × %d、宽 4 格 × %d）或总高小于边长", cell, rows, side, height, rows, cell[1], cell[0])
				}
				if !composited && (side != min(2*cell[1], 4*cell[0]) || height != rows*cell[1]) {
					t.Fatalf("透明画法（n056）的尺寸要原样：%v %d 行得 %d×%d", cell, rows, side, height)
				}
				// 等比：画布（logo 方图垫高后）的宽高比与目标 side×height 一致（整数误差一个像素以内），不会被压扁
				im := PadVertical(logo.For("claude"), side, height)
				b := im.Bounds()
				if d := b.Dx()*height - b.Dy()*side; d > b.Dx() || -d > b.Dx() {
					t.Fatalf("%v %d 行：画布 %dx%d 缩到 %dx%d 不等比（差 %d）", cell, rows, b.Dx(), b.Dy(), side, height, d)
				}
				// 真正编出来的 sixel 的光栅属性与算出来的一致
				data := quotaSixel(logo.For("claude"), cell[0], cell[1], rows)
				var a, bb, w2, h2 int
				if _, err := fmt.Sscanf(data[strings.Index(data, "\""):], "\"%d;%d;%d;%d", &a, &bb, &w2, &h2); err != nil || w2 != side || h2 != height {
					t.Fatalf("%v %d 行：sixel 光栅 %dx%d 应为 %dx%d（%v）", cell, rows, w2, h2, side, height, err)
				}
			}
		}
	}
}
