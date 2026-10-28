package logo

import (
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 抗锯齿圆：alpha 从 0 渐变到 255，颜色很多，强迫走量化分支。
func aaDisc(n int) *image.NRGBA {
	im := image.NewNRGBA(image.Rect(0, 0, n, n))
	c := float64(n) / 2
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			d := math.Hypot(float64(x)+0.5-c, float64(y)+0.5-c)
			a := math.Max(0, math.Min(1, (c*0.8-d)/(c*0.25)+0.5))
			// 颜色也随位置变，保证 >255 种合成色
			im.SetNRGBA(x, y, color.NRGBA{uint8(180 + x%70), uint8(245 - y%90), uint8(70 + (x*y)%150), uint8(a * 255)})
		}
	}
	return im
}

func distinctComposite(img image.Image, w, h int, bg color.RGBA) int {
	px := sample(img, w, h)
	set := map[[3]uint8]bool{}
	for y := range px {
		for x := range px[y] {
			c := px[y][x]
			a := uint32(c.A)
			m := func(f, b uint8) uint8 { return uint8((uint32(f)*a + uint32(b)*(255-a) + 127) / 255) }
			set[[3]uint8{m(c.R, bg.R), m(c.G, bg.G), m(c.B, bg.B)}] = true
		}
	}
	return len(set)
}

// 发现 L1：量化分支里「几乎透明」的边缘像素被吸到色立方，与背景相差很多，logo 周围出现灰框/光晕。
func TestADV_QuantizedBranchHalosNearBackground(t *testing.T) {
	bg := color.RGBA{30, 30, 30, 255}
	img := aaDisc(64)
	if n := distinctComposite(img, 64, 64, bg); n <= 255 {
		t.Fatalf("前提：要走量化分支，实际只有 %d 种颜色", n)
	}
	grid, cs, _ := decodeAny(t, SixelOn(img, 64, 64, bg))
	px := sample(img, 64, 64)
	worst, bad := 0, 0
	for y := range px {
		for x := range px[y] {
			c := px[y][x]
			if c.A > 12 { // 源图 alpha 不到 5%：视觉上应当就是背景
				continue
			}
			got := cs[grid[y][x]]
			d := abs(got[0]*255/100 - int(bg.R)) // 百分比换回 0..255
			if d > worst {
				worst = d
			}
			if d > 8 {
				bad++
			}
		}
	}
	t.Logf("alpha<5%% 的像素里有 %d 个与背景相差 >8/255，最大差 %d", bad, worst)
	if bad > 0 {
		t.Fatalf("几乎透明的像素没有落回精确背景色：%d 个，最大偏差 %d/255", bad, worst)
	}
}

// 真 logo 在常见大小下是否走量化分支、耗时多少。
func TestADV_RealLogosPathAndTiming(t *testing.T) {
	bg := color.RGBA{30, 30, 30, 255}
	for _, tool := range []string{"claude", "codex", "grok", "opencode", "kimi", "antigravity"} {
		im := For(tool)
		if im == nil {
			t.Fatal(tool)
		}
		for _, side := range []int{16, 38, 76, 152} {
			n := distinctComposite(im, side, side, bg)
			t0 := time.Now()
			SixelOn(im, side, side, bg)
			t.Logf("%-12s side=%-4d 合成后颜色 %-4d quant=%v 用时 %v", tool, side, n, n > 255, time.Since(t0))
		}
	}
	for _, side := range []int{16, 38, 76, 152} {
		n := distinctComposite(Deck(), side, side, bg)
		t.Logf("deck         side=%-4d 合成后颜色 %-4d quant=%v", side, n, n > 255)
	}
}

// 尺寸边界：0、1、负数、非 6 倍数、极大；不得 panic，寄存器号不得超过 255 且每个被使用的寄存器都有定义。
func TestADV_SixelOnSizes(t *testing.T) {
	bg := color.RGBA{0, 0, 0, 0} // 故意传 A=0
	for _, sz := range [][2]int{{0, 0}, {0, 5}, {5, 0}, {1, 1}, {1, 7}, {7, 1}, {5, 6}, {5, 7}, {13, 13}, {64, 64}} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%v panic：%v", sz, r)
				}
			}()
			s := SixelOn(Deck(), sz[0], sz[1], bg)
			if sz[0] <= 0 || sz[1] <= 0 {
				// 复审 2 起：边长为 0（FitHeight 对不足 6 像素返回 0）表示不画，返回空串，而不是一个 0×0 的 sixel 头
				if s != "" {
					t.Errorf("%v 应返回空串：%q", sz, s)
				}
				return
			}
			if !strings.HasPrefix(s, "\x1bP0;0;0q\"1;1;") || !strings.HasSuffix(s, "\x1b\\") {
				t.Errorf("%v 头尾不对 %q", sz, s)
			}
			if sz[0] > 0 && sz[1] > 0 {
				g, cs, _ := decodeAny(t, s)
				for y := range g {
					for x := range g[y] {
						if g[y][x] < 0 {
							t.Errorf("%v (%d,%d) 没画", sz, x, y)
							return
						}
						if _, ok := cs[g[y][x]]; !ok {
							t.Errorf("%v 用了没定义的寄存器 %d", sz, g[y][x])
							return
						}
					}
				}
			}
		}()
	}
	if testing.Short() {
		return
	}
}

func TestADV_SixelOnNegativeSizePanics(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Logf("负尺寸会 panic：%v（上游 sidebar/quota 用 <=0 判断挡住了，不可达，仅记录）", r)
		}
	}()
	SixelOn(Deck(), -1, 5, color.RGBA{})
}

// 透明度取值：全透明 / 全不透明 / alpha=1 / alpha=254；背景 0 与 255 边界。
func TestADV_AlphaAndBackgroundBoundaries(t *testing.T) {
	for _, bg := range []color.RGBA{{0, 0, 0, 255}, {255, 255, 255, 255}, {255, 0, 0, 255}, {0, 255, 255, 0}} {
		for _, a := range []uint8{0, 1, 127, 128, 254, 255} {
			im := image.NewNRGBA(image.Rect(0, 0, 4, 4))
			for i := range im.Pix {
				im.Pix[i] = 255
			}
			for i := 3; i < len(im.Pix); i += 4 {
				im.Pix[i] = a
			}
			g, cs, _ := decodeAny(t, SixelOn(im, 4, 4, bg))
			for y := range g {
				for x := range g[y] {
					c, ok := cs[g[y][x]]
					if !ok {
						t.Fatalf("bg=%v a=%d 无定义寄存器", bg, a)
					}
					for _, v := range c {
						if v < 0 || v > 100 {
							t.Fatalf("分量越界 %v", c)
						}
					}
				}
			}
			if a == 0 && cs[g[0][0]] != [3]int{pct(bg.R), pct(bg.G), pct(bg.B)} {
				t.Errorf("alpha=0 应等于背景：%v vs %v", cs[g[0][0]], bg)
			}
		}
	}
}

// pct 把 0..255 压成 0..100：背景在 sixel 里只有 101 级。终端背景 (30,30,30)=11.76% → 12% → 终端实际画 (30.6)?
// 看量化误差：背景寄存器的颜色经 pct 取整后与真实背景色差多少。
func TestADV_BackgroundPercentRoundingIsVisible(t *testing.T) {
	worst := 0.0
	var wv uint8
	for v := 0; v < 256; v++ {
		back := float64(pct(uint8(v))) * 255 / 100
		if d := math.Abs(back - float64(v)); d > worst {
			worst, wv = d, uint8(v)
		}
	}
	t.Logf("sixel 颜色只有 0..100 百分比：背景分量最大偏差 %.2f/255（例如 %d）", worst, wv)
	if worst > 1.5 {
		t.Errorf("背景色在 sixel 里取整后与终端实际背景差 %.2f/255", worst)
	}
}

// graphics-bg 的怪内容。
func TestADV_CurrentBackgroundWeirdContents(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"0x1234":      "0x1234\n",
		"符号":          "+1+2+3\n",
		"负号":          "-1-2-3\n",
		"空格":          " 12345\n",
		"下划线":         "1_2_3_\n",
		"大写":          "AABBCC\n",
		"带#":          "#aabbc\n",
		"多行":          "aabbcc\nddeeff\n",
		"CRLF":        "aabbcc\r\n",
		"6 字节非 ASCII": "ééé\n",
		"NUL":         "aabbc\x00\n",
		"空":           "",
		"七位":          "aabbccd\n",
	}
	for name, content := range cases {
		os.WriteFile(BackgroundFile(dir), []byte(content), 0o600)
		got := CurrentBackground(dir)
		t.Logf("%-14s %q → %v", name, content, got)
	}
	// 只有 TrimSpace 后恰好 6 个十六进制才该认；下面这些不该认
	for _, content := range []string{"0x1234\n", "+1+2+3\n", "-1-2-3\n", "1_2_3_\n", "ééé\n", "aabbc\x00\n"} {
		os.WriteFile(BackgroundFile(dir), []byte(content), 0o600)
		if got := CurrentBackground(dir); got != nil {
			t.Errorf("%q 被当成了颜色 %v", content, *got)
		}
	}
}

// 超大文件、目录、无权限：不能卡住、不能当颜色。
func TestADV_BackgroundFileHostile(t *testing.T) {
	dir := t.TempDir()
	// 目录
	os.MkdirAll(BackgroundFile(dir), 0o700)
	if CurrentBackground(dir) != nil {
		t.Error("目录应是 nil")
	}
	os.Remove(BackgroundFile(dir))
	// 大文件（1GB 稀疏文件）：ReadFile 会把整个读进内存
	f, _ := os.Create(BackgroundFile(dir))
	f.Truncate(256 << 20)
	f.Close()
	t0 := time.Now()
	got := CurrentBackground(dir)
	t.Logf("256MB 稀疏文件：%v，用时 %v", got, time.Since(t0))
	if time.Since(t0) > 200*time.Millisecond {
		t.Errorf("每秒读一次的文件没有大小上限：256MB 读了 %v", time.Since(t0))
	}
	os.Remove(BackgroundFile(dir))
	// 无权限
	os.WriteFile(BackgroundFile(dir), []byte("aabbcc\n"), 0o000)
	if os.Getuid() != 0 {
		if got := CurrentBackground(dir); got != nil {
			t.Error("无权限应 nil")
		} else {
			t.Log("无权限读：返回 nil（等于「透明画法」）；而 graphics 文件仍写着 sixel → iTerm2 上会画实心方块")
		}
	}
}

// 并发写：多个 deck up 同时 WriteBackground，读的一方不能读到半截；不得留下 tmp 文件。
func TestADV_ConcurrentWriteBackground(t *testing.T) {
	dir := t.TempDir()
	cols := []color.RGBA{{1, 2, 3, 255}, {250, 251, 252, 255}}
	done := make(chan struct{})
	bad := make(chan string, 1)
	go func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			b, err := os.ReadFile(BackgroundFile(dir))
			if err == nil && len(b) != 7 {
				select {
				case bad <- string(b):
				default:
				}
			}
		}
	}()
	fin := make(chan struct{})
	for g := 0; g < 4; g++ {
		go func(g int) {
			for i := 0; i < 300; i++ {
				c := cols[(g+i)%2]
				if i%7 == 0 {
					WriteBackground(dir, nil)
				} else {
					WriteBackground(dir, &c)
				}
			}
			fin <- struct{}{}
		}(g)
	}
	for g := 0; g < 4; g++ {
		<-fin
	}
	close(done)
	select {
	case s := <-bad:
		t.Errorf("读到半截内容 %q", s)
	default:
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("残留临时文件 %s", e.Name())
		}
	}
	_ = filepath.Join
}

// 真 logo 上的同一问题：哪些工具在哪些边长下走量化分支，且 alpha<5% 的像素偏离背景多少。
func TestADV_RealLogosHaloInQuantizedBranch(t *testing.T) {
	for _, bg := range []color.RGBA{{30, 30, 30, 255}, {255, 255, 255, 255}, {40, 44, 52, 255}} {
		for _, tool := range []string{"claude", "kimi", "antigravity"} {
			for _, side := range []int{32, 64, 76} {
				im := For(tool)
				if distinctComposite(im, side, side, bg) <= 255 {
					continue
				}
				g, cs, _ := decodeAny(t, SixelOn(im, side, side, bg))
				px := sample(im, side, side)
				worst, bad, tot := 0, 0, 0
				for y := range px {
					for x := range px[y] {
						if px[y][x].A > 12 {
							continue
						}
						tot++
						got := cs[g[y][x]]
						d := max(abs(got[0]*255/100-int(bg.R)), abs(got[1]*255/100-int(bg.G)), abs(got[2]*255/100-int(bg.B)))
						worst = max(worst, d)
						if d > 8 {
							bad++
						}
					}
				}
				t.Logf("bg=%v %-11s side=%-3d 近透明像素 %d 个，偏离背景 >8 的 %d 个，最大差 %d/255", bg, tool, side, tot, bad, worst)
			}
		}
	}
}
