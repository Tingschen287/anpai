// Package logo 画各家 agent 的 logo（n049，架构稿 §8）。
//
// 素材是预先栅格化的 64px PNG（嵌在二进制里，运行时零依赖）：Anthropic、OpenAI、xAI、OpenCode、Kimi。
// 两种画法：
//   - Sixel：真图。只画在外层布局 server 的窗格里（侧栏、标签条），不进中间嵌套窗格。
//   - 半格色块：每个字符格画上下两个像素（▀ 前景 + 背景色），任何真彩色终端都能显示。
//
// DECK_GRAPHICS=sixel|blocks|none 选择画法（deck up 按终端判断后写进布局 server 的环境）。
package logo

import (
	"bytes"
	"embed"
	"fmt"
	"image"
	"image/color"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

//go:embed assets/*.png
var assets embed.FS

// 工具（agent-deck 的工具名）到素材的对应。
var toolAsset = map[string]string{
	"claude":   "claude",
	"codex":    "openai",
	"grok":     "xai",
	"opencode": "opencode",
	"kimi":     "kimi",
	// Antigravity 的 logo 取自 owner 的 Token 看板 brand.js（n056）
	"antigravity": "antigravity",
}

// Brand 是没有素材时的品牌色与字母（色块徽章用）。
var Brand = map[string]struct {
	Letter string
	Color  color.RGBA
}{
	"claude":      {"A", color.RGBA{0xd9, 0x77, 0x57, 0xff}},
	"codex":       {"O", color.RGBA{0x10, 0xa3, 0x7f, 0xff}},
	"grok":        {"X", color.RGBA{0xe5, 0xe5, 0xe5, 0xff}},
	"opencode":    {"O", color.RGBA{0x8a, 0x8a, 0x8a, 0xff}},
	"kimi":        {"K", color.RGBA{0x2b, 0x7f, 0xff, 0xff}},
	"antigravity": {"A", color.RGBA{0x30, 0x80, 0xf0, 0xff}},
}

var (
	cacheMu sync.Mutex
	cache   = map[string]image.Image{}
)

// AccentRGB 是 deck 的品牌色青柠 #b4f546（adr_031；原取 owner 个人网站的 #cdf545，2026-10-14 owner 要「再绿点」），用于终端真彩色转义。
const AccentRGB = "180;245;70"

// Accent 是品牌色的前景转义。
const Accent = "\x1b[38;2;" + AccentRGB + "m"

// Deck 返回 deck 自己的 logo（暂用 A4「横梁接光标」，透明底青柠，1.design/brand/agent-deck-logo）。
func Deck() image.Image { return load("deck") }

// For 返回某个工具的 logo，没有素材返回 nil。
func For(tool string) image.Image {
	name, ok := toolAsset[tool]
	if !ok {
		return nil
	}
	return load(name)
}

func load(name string) image.Image {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if im, ok := cache[name]; ok {
		return im
	}
	data, err := assets.ReadFile("assets/" + name + ".png")
	if err != nil {
		return nil
	}
	im, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	cache[name] = im
	return im
}

// ModeFile 是布局当前画法的文件（deck 状态目录下）：deck up 每次接上前写入本次判出的画法，
// 侧栏、额度面板等已在运行的窗格据此切换（n056 评审 1 B2：复用布局时画法要跟着本次探测走）。
func ModeFile(stateDir string) string { return filepath.Join(stateDir, "graphics") }

// WriteMode 记下布局当前的画法。
func WriteMode(stateDir, mode string) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(ModeFile(stateDir), []byte(mode+"\n"), 0o600)
}

// CurrentMode 读布局当前的画法；文件没有或内容不认识时按 Mode()（DECK_GRAPHICS 环境变量）。
func CurrentMode(stateDir string) string {
	if b, err := os.ReadFile(ModeFile(stateDir)); err == nil {
		switch v := strings.TrimSpace(string(b)); v {
		case "sixel", "blocks", "none":
			return v
		}
	}
	return Mode()
}

// Mode 是画法：sixel、blocks、none。
func Mode() string {
	switch v := strings.ToLower(os.Getenv("DECK_GRAPHICS")); v {
	case "sixel", "blocks":
		return v
	}
	return "none" // 默认品牌色字母徽章：4×2 格的半格色块只有 4×4 像素，认不出（n056，n052 owner 反馈）
}

// sample 把 img 缩到 w×h（按区域平均，带透明度）。
func sample(img image.Image, w, h int) [][]color.RGBA {
	b := img.Bounds()
	out := make([][]color.RGBA, h)
	for y := 0; y < h; y++ {
		out[y] = make([]color.RGBA, w)
		y0 := b.Min.Y + y*b.Dy()/h
		y1 := max(y0+1, b.Min.Y+(y+1)*b.Dy()/h)
		for x := 0; x < w; x++ {
			x0 := b.Min.X + x*b.Dx()/w
			x1 := max(x0+1, b.Min.X+(x+1)*b.Dx()/w)
			var r, g, bl, a, n uint32
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					c := color.NRGBAModel.Convert(img.At(xx, yy)).(color.NRGBA)
					// 预乘：透明像素不把颜色带进平均
					r += uint32(c.R) * uint32(c.A)
					g += uint32(c.G) * uint32(c.A)
					bl += uint32(c.B) * uint32(c.A)
					a += uint32(c.A)
					n++
				}
			}
			if a == 0 {
				out[y][x] = color.RGBA{}
				continue
			}
			out[y][x] = color.RGBA{uint8(r / a), uint8(g / a), uint8(bl / a), uint8(a / n)}
		}
	}
	return out
}

// opaque 说明一个采样像素算不算「画」：半透明以上算。
func opaque(c color.RGBA) bool { return c.A >= 128 }

// Blocks 把 img 画成 cols×rows 个字符格的半格色块（每格上下两个像素），每行一个带真彩色转义的字符串。
// 透明像素用终端默认背景（不设背景色），所以 logo 外的地方不会出现方块。
func Blocks(img image.Image, cols, rows int) []string {
	px := sample(img, cols, rows*2)
	lines := make([]string, rows)
	for r := 0; r < rows; r++ {
		var b strings.Builder
		for c := 0; c < cols; c++ {
			top, bot := px[2*r][c], px[2*r+1][c]
			switch {
			case opaque(top) && opaque(bot):
				fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%dm\x1b[48;2;%d;%d;%dm▀\x1b[0m", top.R, top.G, top.B, bot.R, bot.G, bot.B)
			case opaque(top):
				fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%dm▀\x1b[0m", top.R, top.G, top.B)
			case opaque(bot):
				fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%dm▄\x1b[0m", bot.R, bot.G, bot.B)
			default:
				b.WriteByte(' ')
			}
		}
		lines[r] = b.String()
	}
	return lines
}

// Sixel 把 img 缩到 w×h 像素，编成一段 Sixel（DCS … ST）。透明像素不画（P2=1：保留原有背景）。
// 颜色量化到 6×6×6 色立方，Sixel 的颜色分量是 0–100 的百分比。
func Sixel(img image.Image, w, h int) string {
	px := sample(img, w, h)
	idx := make([][]int, h) // -1 表示不画
	used := map[int]bool{}
	for y := 0; y < h; y++ {
		idx[y] = make([]int, w)
		for x := 0; x < w; x++ {
			c := px[y][x]
			if !opaque(c) {
				idx[y][x] = -1
				continue
			}
			i := cube(c)
			idx[y][x] = i
			used[i] = true
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\x1bP0;1;0q\"1;1;%d;%d", w, h)
	for i := 0; i < 216; i++ {
		if used[i] {
			r, g, bl := cubeRGB(i)
			fmt.Fprintf(&b, "#%d;2;%d;%d;%d", i, pct(r), pct(g), pct(bl))
		}
	}
	for band := 0; band < h; band += 6 {
		first := true
		for i := 0; i < 216; i++ {
			if !used[i] {
				continue
			}
			row := make([]byte, w)
			any := false
			for x := 0; x < w; x++ {
				var bits byte
				for k := 0; k < 6 && band+k < h; k++ {
					if idx[band+k][x] == i {
						bits |= 1 << k
					}
				}
				row[x] = 63 + bits
				if bits != 0 {
					any = true
				}
			}
			if !any {
				continue
			}
			if !first {
				b.WriteByte('$') // 回到本带行首，叠画下一种颜色
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

func writeRLE(b *strings.Builder, row []byte) {
	for i := 0; i < len(row); {
		j := i
		for j < len(row) && row[j] == row[i] {
			j++
		}
		if n := j - i; n > 3 {
			fmt.Fprintf(b, "!%d%c", n, row[i])
		} else {
			b.Write(row[i:j])
		}
		i = j
	}
}

func cube(c color.RGBA) int {
	q := func(v uint8) int { return (int(v)*5 + 127) / 255 }
	return q(c.R)*36 + q(c.G)*6 + q(c.B)
}

func cubeRGB(i int) (r, g, b uint8) {
	v := func(n int) uint8 { return uint8(n * 255 / 5) }
	return v(i / 36), v(i / 6 % 6), v(i % 6)
}

func pct(v uint8) int { return (int(v)*100 + 127) / 255 }
