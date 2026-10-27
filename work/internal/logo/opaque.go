package logo

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// n069：iTerm2（3.7.6 真机，诊断样张 2）把 sixel 里「没画」的像素（透明处，P2=1）涂成图里最后定义的那种颜色：
// 只用一种颜色的 A4 logo 成了实心方块，带抗锯齿边的也只剩模糊的影子，测试图的透明洞变成白色。
// 对这样的终端不再留任何没画的像素：把图合成到终端背景色上，每个像素都明确画出来，背景用单独的、不量化的颜色寄存器，
// 与终端背景一致，所以看上去仍然是透明的。其余终端（Windows Terminal 等）走原来的透明画法，一个字节都不变。

var (
	bgMu  sync.Mutex
	bgNow *color.RGBA // nil：透明画法
)

// SetBackground 设定 Sixel 合成用的终端背景色；nil 表示用原来的透明画法。
func SetBackground(c *color.RGBA) {
	bgMu.Lock()
	defer bgMu.Unlock()
	if c == nil {
		bgNow = nil
		return
	}
	v := *c
	v.A = 255
	bgNow = &v
}

// Background 返回当前的合成背景色，nil 表示透明画法。
func Background() *color.RGBA {
	bgMu.Lock()
	defer bgMu.Unlock()
	if bgNow == nil {
		return nil
	}
	v := *bgNow
	return &v
}

// BackgroundKey 是背景色的缓存键（画出来的 sixel 随背景变，缓存要分开）。
func BackgroundKey() string {
	if b := Background(); b != nil {
		return fmt.Sprintf("%02x%02x%02x", b.R, b.G, b.B)
	}
	return "-"
}

// BackgroundFile 是布局当前合成背景色的文件（deck 状态目录下，与 ModeFile 并列）。
func BackgroundFile(stateDir string) string { return filepath.Join(stateDir, "graphics-bg") }

// WriteBackground 记下布局当前的合成背景色；nil 删掉这个文件（透明画法）。
func WriteBackground(stateDir string, c *color.RGBA) error {
	if c == nil {
		err := os.Remove(BackgroundFile(stateDir))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(stateDir, "graphics-bg.*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := fmt.Fprintf(f, "%02x%02x%02x\n", c.R, c.G, c.B); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, BackgroundFile(stateDir)); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// CurrentBackground 读布局当前的合成背景色；文件没有或内容不认识就是 nil（透明画法）。
func CurrentBackground(stateDir string) *color.RGBA {
	b, err := os.ReadFile(BackgroundFile(stateDir))
	if err != nil {
		return nil
	}
	s := strings.TrimSpace(string(b))
	if len(s) != 6 {
		return nil
	}
	var r, g, bl uint8
	if _, err := fmt.Sscanf(s, "%02x%02x%02x", &r, &g, &bl); err != nil {
		return nil
	}
	return &color.RGBA{r, g, bl, 255}
}

// Apply 读布局当前的画法与合成背景色：背景色设进本进程，返回画法。侧栏、额度面板每次取画法时都走它，deck up 改了会跟着变。
func Apply(stateDir string) string {
	SetBackground(CurrentBackground(stateDir))
	return CurrentMode(stateDir)
}

const maxRegisters = 256 // iTerm2 报告的颜色寄存器数（XTSMGRAPHICS，诊断样张 1）

// SixelOn 把 img 缩到 w×h，合成到背景 bg 上，编成不留空洞的 Sixel（P2=0，每个像素都画）。
// 颜色少（不超过 255 种）就用原色，不量化；否则量化到 6×6×6 色立方，背景色仍单独占一个寄存器、保持精确。
func SixelOn(img image.Image, w, h int, bg color.RGBA) string {
	px := sample(img, w, h)
	flat := make([]color.RGBA, w*h)
	set := map[color.RGBA]bool{}
	bg.A = 255
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := px[y][x]
			a := uint32(c.A)
			mix := func(f, b uint8) uint8 { return uint8((uint32(f)*a + uint32(b)*(255-a) + 127) / 255) }
			o := color.RGBA{mix(c.R, bg.R), mix(c.G, bg.G), mix(c.B, bg.B), 255}
			flat[y*w+x] = o
			set[o] = true
		}
	}
	set[bg] = true
	// 寄存器：背景色排第 0 号，其余按颜色值排序（输出稳定）
	cols := make([]color.RGBA, 0, len(set))
	for c := range set {
		if c != bg {
			cols = append(cols, c)
		}
	}
	sort.Slice(cols, func(i, j int) bool {
		a, b := cols[i], cols[j]
		if a.R != b.R {
			return a.R < b.R
		}
		if a.G != b.G {
			return a.G < b.G
		}
		return a.B < b.B
	})
	index := map[color.RGBA]int{bg: 0}
	palette := []color.RGBA{bg}
	if len(cols) <= maxRegisters-1 {
		for _, c := range cols {
			index[c] = len(palette)
			palette = append(palette, c)
		}
	} else {
		// 颜色太多：非背景色量化到色立方，只给用到的立方色开寄存器
		cube := map[int]int{}
		var ids []int
		for _, c := range cols {
			i := cubeOf(c)
			if _, ok := cube[i]; !ok {
				cube[i] = -1
				ids = append(ids, i)
			}
		}
		sort.Ints(ids)
		for _, i := range ids {
			r, g, b := cubeRGB(i)
			cube[i] = len(palette)
			palette = append(palette, color.RGBA{r, g, b, 255})
		}
		for _, c := range cols {
			index[c] = cube[cubeOf(c)]
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\x1bP0;0;0q\"1;1;%d;%d", w, h)
	for i, c := range palette {
		fmt.Fprintf(&b, "#%d;2;%d;%d;%d", i, pct(c.R), pct(c.G), pct(c.B))
	}
	for band := 0; band < h; band += 6 {
		used := map[int]bool{}
		for k := 0; k < 6 && band+k < h; k++ {
			for x := 0; x < w; x++ {
				used[index[flat[(band+k)*w+x]]] = true
			}
		}
		first := true
		for i := range palette {
			if !used[i] {
				continue
			}
			row := make([]byte, w)
			for x := 0; x < w; x++ {
				var bits byte
				for k := 0; k < 6 && band+k < h; k++ {
					if index[flat[(band+k)*w+x]] == i {
						bits |= 1 << k
					}
				}
				row[x] = 63 + bits
			}
			if !first {
				b.WriteByte('$')
			}
			first = false
			fmt.Fprintf(&b, "#%d", i)
			writeRLE(&b, row)
		}
		if band+6 < h {
			b.WriteByte('-')
		}
	}
	b.WriteString("\x1b\\")
	return b.String()
}

func cubeOf(c color.RGBA) int { return cube(c) }

// SelectedRowBG 是侧栏选中会话行的底色（xterm 256 色 236，sidebar.go 里的 \x1b[48;5;236m）。
var SelectedRowBG = color.RGBA{48, 48, 48, 255}

// SixelOver 同 Sixel，但合成背景换成 over（图盖在有底色的行上时用）；透明画法（没设合成背景）时与 Sixel 一样。
func SixelOver(img image.Image, w, h int, over color.RGBA) string {
	if Background() == nil {
		return SixelTransparent(img, w, h)
	}
	return SixelOn(img, w, h, over)
}
