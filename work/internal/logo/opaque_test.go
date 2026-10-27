package logo

import (
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// n069 的 iTerm2 对照：诊断样张 2 里 iTerm2 3.7.6 把 sixel 里没画的像素涂成「最后定义的那种颜色」
// （A4 只有一种颜色 → 实心方块；测试图的透明洞 → 白色，白是最后定义的寄存器）。fillLikeITerm 复现这个行为，
// 用来证明：透明画法在这样的终端上画成一团；合成背景的画法不留空洞，形状保住。

// decodeAny 解码本包两种 sixel（透明画法 P2=1、合成画法 P2=0）；返回像素格子与颜色定义，以及最后定义的寄存器号。
func decodeAny(t *testing.T, s string) (grid [][]int, colors map[int][3]int, lastDefined int) {
	t.Helper()
	s = strings.Replace(s, "\x1bP0;0;0q", "\x1bP0;1;0q", 1)
	grid, colors = decodeSixel(t, s)
	lastDefined = -1
	body := s[strings.Index(s, "#"):]
	for i := 0; i < len(body); i++ {
		if body[i] != '#' {
			continue
		}
		j := i + 1
		n := 0
		for j < len(body) && body[j] >= '0' && body[j] <= '9' {
			n = n*10 + int(body[j]-'0')
			j++
		}
		if j < len(body) && body[j] == ';' {
			lastDefined = n // 颜色定义（#n;2;r;g;b），不是选色
		}
	}
	return grid, colors, lastDefined
}

// fillLikeITerm 按 iTerm2 的实测行为渲染：没画的像素取最后定义的寄存器的颜色。
func fillLikeITerm(grid [][]int, colors map[int][3]int, last int) [][3]int {
	var out [][3]int
	for _, row := range grid {
		for _, c := range row {
			if c < 0 {
				c = last
			}
			out = append(out, colors[c])
		}
	}
	return out
}

func share(px [][3]int, pred func([3]int) bool) float64 {
	n := 0
	for _, p := range px {
		if pred(p) {
			n++
		}
	}
	return float64(n) / float64(len(px))
}

func TestITermFillMakesTransparentLogoASolidSquareButOpaqueKeepsShape(t *testing.T) {
	bg := color.RGBA{16, 16, 16, 255}
	for _, side := range []int{16, 32, 38, 64} {
		// 透明画法 + iTerm2 的填法：几乎全是一种颜色（诊断样张 2 的 A、B）
		g, cs, last := decodeAny(t, SixelTransparent(Deck(), side, side))
		px := fillLikeITerm(g, cs, last)
		// 看不出形状：九成以上的像素与最多的那种颜色相差不到 25（百分比三通道之和），没有可辨认的前景与背景
		counts := map[[3]int]int{}
		for _, p := range px {
			counts[p]++
		}
		var dom [3]int
		for c, n := range counts {
			if n > counts[dom] {
				dom = c
			}
		}
		near := share(px, func(p [3]int) bool {
			return abs(p[0]-dom[0])+abs(p[1]-dom[1])+abs(p[2]-dom[2]) < 25
		})
		if near < 0.9 {
			t.Fatalf("%d：复现不了 iTerm2 的实心方块（接近主色的只占 %.0f%%），对照失效", side, 100*near)
		}
		// 合成画法：没有没画的像素；背景与前景都有相当比例，形状保住
		g2, cs2, last2 := decodeAny(t, SixelOn(Deck(), side, side, bg))
		for y := range g2 {
			for x, c := range g2[y] {
				if c < 0 {
					t.Fatalf("%d：(%d,%d) 没画：合成画法不能留空洞", side, x, y)
				}
			}
		}
		px2 := fillLikeITerm(g2, cs2, last2)
		isBG := func(p [3]int) bool { return p == cs2[last2] }
		isLime := func(p [3]int) bool { return p[1] > 70 && p[0] > 50 && p[2] < 50 }
		if s := share(px2, isBG); s < 0.15 || s > 0.8 {
			t.Fatalf("%d：背景占 %.0f%%，形状不对", side, 100*s)
		}
		if s := share(px2, isLime); s < 0.15 {
			t.Fatalf("%d：前景（青柠）只占 %.0f%%", side, 100*s)
		}
	}
}

// 背景色单独占最后一个寄存器（不是零号寄存器，见 n069 样张 9）、不量化：暗灰 (30,30,30) 在 6×6×6 色立方里会变成 (0,0,0) 或 (51,51,51)，贴在终端背景上就是看得见的方框。
func TestSixelOnBackgroundIsExactLastRegister(t *testing.T) {
	for _, bg := range []color.RGBA{{30, 30, 30, 255}, {0, 0, 0, 255}, {255, 255, 255, 255}, {40, 44, 52, 255}} {
		_, cs, _ := decodeAny(t, SixelOn(Deck(), 32, 32, bg))
		want := [3]int{pct(bg.R), pct(bg.G), pct(bg.B)}
		_, _, last := decodeAny(t, SixelOn(Deck(), 32, 32, bg))
		if cs[last] != want {
			t.Fatalf("背景 %v：最后一个寄存器应是 %v，实际 %v", bg, want, cs[last])
		}
	}
}

// 全透明的像素落在背景寄存器上；不透明的前景像素是原色（颜色少时不量化）：青柠 #b4f546 → (71,96,27)%。
func TestSixelOnKeepsBrandColourAndTransparentIsBackground(t *testing.T) {
	bg := color.RGBA{16, 16, 16, 255}
	g, cs, _ := decodeAny(t, SixelOn(Deck(), 64, 64, bg))
	if cs[g[0][0]] != [3]int{pct(16), pct(16), pct(16)} { // A4 的左上角是透明
		t.Fatalf("透明角应是背景寄存器：%v", cs[g[0][0]])
	}
	want := [3]int{pct(180), pct(245), pct(70)}
	found := false
	for _, c := range cs {
		found = found || c == want
	}
	if !found {
		t.Fatalf("品牌青柠 %v 应原样出现在调色板里：%v", want, cs)
	}
}

// 颜色多到寄存器不够时量化到色立方，背景仍然精确，寄存器总数不超过 256。
func TestSixelOnManyColoursStaysWithinRegisters(t *testing.T) {
	bg := color.RGBA{30, 30, 30, 255}
	_, cs, _ := decodeAny(t, SixelOn(patternGradient(), 64, 64, bg))
	if len(cs) > maxRegisters {
		t.Fatalf("寄存器 %d 个，超过 %d", len(cs), maxRegisters)
	}
	_, _, lastReg := decodeAny(t, SixelOn(patternGradient(), 64, 64, bg))
	if cs[lastReg] != [3]int{pct(30), pct(30), pct(30)} {
		t.Fatalf("量化后背景也要精确：%v", cs[lastReg])
	}
}

// 透明画法一个字节都没变（n056 在 Windows Terminal 上验收的编码）：没设合成背景时 Sixel 就是 SixelTransparent，
// 且仍有没画的像素、头是 P2=1。
func TestTransparentPathIsUnchangedWhenNoBackground(t *testing.T) {
	SetBackground(nil)
	if Sixel(Deck(), 32, 32) != SixelTransparent(Deck(), 32, 32) {
		t.Fatal("没设背景时 Sixel 应与透明画法一致")
	}
	s := SixelTransparent(Deck(), 32, 32)
	if !strings.HasPrefix(s, "\x1bP0;1;0q\"1;1;32;32") {
		t.Fatalf("透明画法的头变了：%q", s[:20])
	}
	g, _ := decodeSixel(t, s)
	unset := 0
	for _, row := range g {
		for _, c := range row {
			if c < 0 {
				unset++
			}
		}
	}
	if unset == 0 {
		t.Fatal("透明画法应保留没画的像素")
	}
}

func TestSixelDispatchesOnBackground(t *testing.T) {
	t.Cleanup(func() { SetBackground(nil) })
	bg := color.RGBA{16, 16, 16, 255}
	SetBackground(&bg)
	if Sixel(Deck(), 32, 32) != SixelOn(Deck(), 32, 32, bg) {
		t.Fatal("设了背景，Sixel 应走合成画法")
	}
	if SixelOver(Deck(), 32, 32, SelectedRowBG) != SixelOn(Deck(), 32, 32, SelectedRowBG) {
		t.Fatal("SixelOver 应用给定的底色")
	}
	SetBackground(nil)
	if SixelOver(Deck(), 32, 32, SelectedRowBG) != SixelTransparent(Deck(), 32, 32) {
		t.Fatal("没设合成背景时 SixelOver 仍是透明画法")
	}
}

func TestBackgroundFileRoundTrip(t *testing.T) {
	t.Cleanup(func() { SetBackground(nil) })
	dir := filepath.Join(t.TempDir(), "state")
	if CurrentBackground(dir) != nil {
		t.Fatal("没有文件应是 nil")
	}
	want := color.RGBA{0x1e, 0x20, 0x24, 255}
	if err := WriteBackground(dir, &want); err != nil {
		t.Fatal(err)
	}
	if got := CurrentBackground(dir); got == nil || *got != want {
		t.Fatalf("读回 %v", got)
	}
	WriteMode(dir, "sixel")
	if Apply(dir) != "sixel" || Background() == nil || *Background() != want {
		t.Fatal("Apply 应设上背景并返回画法")
	}
	if err := WriteBackground(dir, nil); err != nil {
		t.Fatal(err)
	}
	// 旧版两个文件的迁移规则（Codex 复审 2 R2-1）：旧版留下「sixel 但没有背景」当字母徽章（可能是旧版并发写出来的混合状态，
	// 按透明画法画在 iTerm2 上是实心方块）；明确的「sixel 加透明」要由新版 WriteDecision 写（见 n069_decision_test.go）
	if Apply(dir) != "none" || Background() != nil {
		t.Fatal("旧版 sixel 没有背景应按字母徽章")
	}
	// 坏内容不当成颜色
	os.WriteFile(BackgroundFile(dir), []byte("zzzzzz\n"), 0o600)
	if CurrentBackground(dir) != nil {
		t.Fatal("坏内容应是 nil")
	}
	os.WriteFile(BackgroundFile(dir), []byte("12345\n"), 0o600)
	if CurrentBackground(dir) != nil {
		t.Fatal("位数不对应是 nil")
	}
	if err := WriteBackground(dir, nil); err != nil { // 没有文件时删除不报错
		t.Fatal(err)
	}
}

func TestBackgroundKey(t *testing.T) {
	t.Cleanup(func() { SetBackground(nil) })
	SetBackground(nil)
	if BackgroundKey() != "-" {
		t.Fatal(BackgroundKey())
	}
	SetBackground(&color.RGBA{1, 2, 3, 77})
	if BackgroundKey() != "010203" || Background().A != 255 {
		t.Fatalf("%s %v", BackgroundKey(), Background())
	}
}

// patternGradient 是颜色很多的图（每个像素不同），用来逼出量化分支。
func patternGradient() *image.RGBA {
	im := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			im.Set(x, y, color.RGBA{uint8(x * 4), uint8(y * 4), uint8((x + y) * 2), 255})
		}
	}
	return im
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// n056 在 Windows Terminal 上验收的透明编码，一个字节都不能变：7 个 logo × 9 种边长共 63 段 sixel，
// 逐段取 sha256 拼成清单再取 sha256，与改动前（c600412）上同一个程序算出的清单（evidence/wt-sixel-hashes-c600412.txt）一致。
func TestTransparentEncodingByteIdenticalToC600412(t *testing.T) {
	SetBackground(nil)
	var list strings.Builder
	for _, tool := range []string{"deck", "claude", "codex", "grok", "opencode", "kimi", "antigravity"} {
		for _, n := range []int{16, 18, 20, 32, 36, 38, 64, 72, 256} {
			im := Deck()
			if tool != "deck" {
				im = For(tool)
			}
			a, b := Sixel(im, n, n), SixelTransparent(im, n, n)
			if a != b {
				t.Fatalf("%s %d：没设背景时 Sixel 应与 SixelTransparent 一致", tool, n)
			}
			fmt.Fprintf(&list, "%s %d %x\n", tool, n, sha256.Sum256([]byte(a)))
		}
	}
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(list.String()))); got != "bd25018be196a637b78223b21482530ef704f4c25f477978075178867f1db6e6" {
		t.Fatalf("透明编码变了（清单哈希 %s）：Windows Terminal 上验收过的画法不能改", got)
	}
}
