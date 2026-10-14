package logo

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"strconv"
	"strings"
	"testing"
)

// decodeSixel 是测试用的极简解码器：返回每个像素的颜色号（-1 表示没画），以及颜色定义。
func decodeSixel(t *testing.T, s string) ([][]int, map[int][3]int) {
	t.Helper()
	if !strings.HasPrefix(s, "\x1bP0;1;0q") || !strings.HasSuffix(s, "\x1b\\") {
		t.Fatalf("Sixel 头尾不对：%q", s[:min(20, len(s))])
	}
	body := strings.TrimSuffix(strings.TrimPrefix(s, "\x1bP0;1;0q"), "\x1b\\")
	var w, h int
	if _, err := fmt.Sscanf(body, "\"1;1;%d;%d", &w, &h); err != nil {
		t.Fatalf("缺光栅属性：%v", err)
	}
	body = body[strings.IndexAny(body, "#"):]
	grid := make([][]int, h)
	for y := range grid {
		grid[y] = make([]int, w)
		for x := range grid[y] {
			grid[y][x] = -1
		}
	}
	colors := map[int][3]int{}
	cur, x, band := 0, 0, 0
	num := func(i int) (int, int) {
		j := i
		for j < len(body) && body[j] >= '0' && body[j] <= '9' {
			j++
		}
		n, _ := strconv.Atoi(body[i:j])
		return n, j
	}
	put := func(c byte, n int) {
		bits := c - 63
		for k := 0; k < n; k++ {
			for b := 0; b < 6; b++ {
				if bits&(1<<b) != 0 && band+b < h && x < w {
					grid[band+b][x] = cur
				}
			}
			x++
		}
	}
	for i := 0; i < len(body); {
		switch c := body[i]; {
		case c == '#':
			n, j := num(i + 1)
			if j < len(body) && body[j] == ';' { // 颜色定义 #n;2;r;g;b
				var r, g, b int
				fmt.Sscanf(body[j:], ";2;%d;%d;%d", &r, &g, &b)
				colors[n] = [3]int{r, g, b}
				for j < len(body) && (body[j] == ';' || (body[j] >= '0' && body[j] <= '9')) {
					j++
				}
			}
			cur, i = n, j
		case c == '!':
			n, j := num(i + 1)
			put(body[j], n)
			i = j + 1
		case c == '$':
			x, i = 0, i+1
		case c == '-':
			x, band, i = 0, band+6, i+1
		case c >= 63 && c <= 126:
			put(c, 1)
			i++
		default:
			t.Fatalf("Sixel 里有不认识的字节 %q（位置 %d）", c, i)
		}
	}
	return grid, colors
}

// Sixel 往返：编码后解码，每个像素的有无与颜色都对得上（跨两个 6 行带、有透明、有连续像素压缩）。
func TestSixelRoundTrip(t *testing.T) {
	red, blue := color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255}
	img := image.NewRGBA(image.Rect(0, 0, 10, 9))
	for y := 0; y < 9; y++ {
		for x := 0; x < 10; x++ {
			switch {
			case x < 5:
				img.Set(x, y, red)
			case y%2 == 0:
				img.Set(x, y, blue)
			} // 其余透明
		}
	}
	grid, colors := decodeSixel(t, Sixel(img, 10, 9))
	for y := 0; y < 9; y++ {
		for x := 0; x < 10; x++ {
			got := grid[y][x]
			switch {
			case x < 5:
				if got < 0 || colors[got] != [3]int{100, 0, 0} {
					t.Fatalf("(%d,%d) 应是红色：%d %v", x, y, got, colors[got])
				}
			case y%2 == 0:
				if got < 0 || colors[got] != [3]int{0, 0, 100} {
					t.Fatalf("(%d,%d) 应是蓝色：%d %v", x, y, got, colors[got])
				}
			default:
				if got != -1 {
					t.Fatalf("(%d,%d) 透明处不应画：%d", x, y, got)
				}
			}
		}
	}
	if !strings.Contains(Sixel(img, 10, 9), "!5") {
		t.Fatal("连续 5 个相同的列应压缩成 !5")
	}
}

// 半格色块：上下两个像素分别成前景、背景色；只有一半有色时用 ▀/▄ 且不设背景；全透明是空格。
func TestBlocks(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	img.Set(0, 1, color.RGBA{0, 255, 0, 255})
	img.Set(1, 0, color.RGBA{0, 0, 255, 255})
	img.Set(2, 1, color.RGBA{255, 255, 255, 255})
	got := Blocks(img, 3, 1)[0]
	want := "\x1b[38;2;255;0;0m\x1b[48;2;0;255;0m▀\x1b[0m" + "\x1b[38;2;0;0;255m▀\x1b[0m" + "\x1b[38;2;255;255;255m▄\x1b[0m"
	if got != want {
		t.Fatalf("色块不对：\n%q\n%q", got, want)
	}
	empty := image.NewRGBA(image.Rect(0, 0, 2, 2))
	if Blocks(empty, 2, 1)[0] != "  " {
		t.Fatal("全透明应是空格")
	}
}

// 五家素材都在，且透明处保持透明（OpenAI 的角落）。
func TestAllLogosLoad(t *testing.T) {
	for _, tool := range []string{"claude", "codex", "grok", "opencode", "kimi"} {
		im := For(tool)
		if im == nil || im.Bounds().Dx() != 64 || im.Bounds().Dy() != 64 {
			t.Fatalf("%s 的 logo 应是 64px：%v", tool, im)
		}
		if len(Sixel(im, 20, 20)) < 50 || len(Blocks(im, 4, 2)) != 2 {
			t.Fatalf("%s 画不出来", tool)
		}
	}
	if For("shell") != nil {
		t.Fatal("没有素材的工具应返回 nil")
	}
	if _, _, _, a := For("codex").At(0, 0).RGBA(); a != 0 {
		t.Fatal("OpenAI logo 的角落应透明")
	}
	// 4×2 格时每格覆盖 16×16 像素，角落那格也盖到了圆形标志，所以在 16×8 格下检查最角落的一格
	if !strings.HasPrefix(Blocks(For("codex"), 16, 8)[0], " ") {
		t.Fatal("透明的角落在色块里应是空格，不应画出方块")
	}
}

// n056：没设 DECK_GRAPHICS 时默认品牌色字母徽章（none），不再默认 4×2 格的半格色块。
func TestModeDefaultIsNone(t *testing.T) {
	t.Setenv("DECK_GRAPHICS", "")
	if m := Mode(); m != "none" {
		t.Fatalf("默认画法：%q", m)
	}
	t.Setenv("DECK_GRAPHICS", "blocks")
	if Mode() != "blocks" {
		t.Fatal("显式指定 blocks 仍可用")
	}
	t.Setenv("DECK_GRAPHICS", "sixel")
	if Mode() != "sixel" {
		t.Fatal("sixel")
	}
}

// n056：deck 自己的 logo 素材嵌在二进制里，能画成 sixel 与色块。
func TestDeckLogoEmbedded(t *testing.T) {
	im := Deck()
	if im == nil || len(Sixel(im, 20, 20)) < 50 || len(Blocks(im, 16, 8)) != 8 {
		t.Fatal("deck logo 素材")
	}
}

// n056 评审 1 B2：布局当前画法写在状态目录的文件里，已在运行的窗格据此切换；文件没有或内容不认识时按环境变量。
func TestCurrentModeFollowsFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DECK_GRAPHICS", "blocks")
	if CurrentMode(dir) != "blocks" {
		t.Fatal("没有文件：按环境变量")
	}
	if err := WriteMode(dir, "sixel"); err != nil || CurrentMode(dir) != "sixel" {
		t.Fatal("文件：sixel", err)
	}
	WriteMode(dir, "none")
	if CurrentMode(dir) != "none" {
		t.Fatal("文件：none")
	}
	WriteMode(dir, "bogus")
	if CurrentMode(dir) != "blocks" {
		t.Fatal("不认识的内容：按环境变量")
	}
}

// n056 attempt 2 自查：画法文件先写临时文件再改名，不留临时文件。
func TestWriteModeAtomic(t *testing.T) {
	dir := t.TempDir()
	if err := WriteMode(dir, "sixel"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ModeFile(dir) + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("临时文件应已改名")
	}
	if CurrentMode(dir) != "sixel" {
		t.Fatal("sixel")
	}
}
