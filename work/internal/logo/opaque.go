package logo

import (
	"fmt"
	"image"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
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
	b, err := readSmall(BackgroundFile(stateDir)) // 合法内容 7 字节；侧栏每秒读一次，不为怪文件读到底（也不被命名管道卡住）
	if err != nil {
		return nil
	}
	c, ok := parseHex6(strings.TrimSpace(string(b)))
	if !ok {
		return nil
	}
	return &c
}

// parseHex6 严格解析六位十六进制颜色：Sscanf 会放过符号、空格、NUL 与 0x 前缀，所以先逐字符校验。
func parseHex6(s string) (color.RGBA, bool) {
	if len(s) != 6 {
		return color.RGBA{}, false
	}
	for i := 0; i < 6; i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return color.RGBA{}, false
		}
	}
	var r, g, bl uint8
	if _, err := fmt.Sscanf(s, "%02x%02x%02x", &r, &g, &bl); err != nil {
		return color.RGBA{}, false
	}
	return color.RGBA{r, g, bl, 255}, true
}

// 画法与合成背景色是一个决策，只写在一个文件里（Codex 复审 2 的 R2-1，owner 选根治方案）：
//
//	graphics-decision   一行：「<画法> <背景色六位十六进制 或 ->」，例如「sixel 1e1e1e」「sixel -」「none -」
//
// 写者写临时文件再 rename（POSIX 原子），读者只读这一个文件：读到的一定是某个写者写下的完整决策，不依赖任何锁，
// 写者写到一半被暂停也不会让读者看到半份。「最后写完整决策的接入者说了算」。
// 旧版本的两个文件（graphics、graphics-bg）只在这个文件不存在时才读（升级迁移）：读到「sixel 但没有背景」按字母徽章处理。

// DecisionFile 是画法与背景色的决策文件（状态目录下）。
func DecisionFile(stateDir string) string { return filepath.Join(stateDir, "graphics-decision") }

func validMode(m string) bool { return m == "sixel" || m == "blocks" || m == "none" }

// 决策文件的读取结果：不存在 / 读到了完整决策 / 在却读不了或读不懂。
const (
	decAbsent = iota
	decOK
	decBad
)

// loadDecision 只打开一次决策文件、按这一次的结果分流（不先问「在不在」再读：两步之间写者可能刚好 rename 落盘，
// 读者就会把刚写好的完整决策当成读不懂；对抗自查 D1）。只有「不存在」算 decAbsent；打开用 O_NONBLOCK 并要求是普通文件，
// 命名管道之类不会让读者永远阻塞（对抗自查 D2）。
func loadDecision(stateDir string) (mode string, bg *color.RGBA, st int) {
	f, err := os.OpenFile(DecisionFile(stateDir), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, decAbsent
		}
		return "", nil, decBad
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return "", nil, decBad
	}
	b, err := io.ReadAll(io.LimitReader(f, 128))
	if err != nil {
		return "", nil, decBad
	}
	fields := strings.Fields(string(b))
	if len(fields) != 2 || !validMode(fields[0]) {
		return "", nil, decBad
	}
	if fields[1] == "-" {
		return fields[0], nil, decOK
	}
	c, ok := parseHex6(fields[1])
	if !ok {
		return "", nil, decBad
	}
	return fields[0], &c, decOK
}

// ReadDecision 读决策文件：返回画法与背景色（nil 是透明画法）；文件没有、过长或内容不合格式返回 ok=false。
func ReadDecision(stateDir string) (mode string, bg *color.RGBA, ok bool) {
	mode, bg, st := loadDecision(stateDir)
	return mode, bg, st == decOK
}

// WriteDecision 把画法与合成背景色作为一个整体原子地写下（临时文件加 rename）。
// 画法是 sixel 且有背景色、但没写成时返回 effective=none（面板读不到背景色会按透明画法画 sixel，iTerm2 上是实心方块）。
func WriteDecision(stateDir, mode string, bg *color.RGBA) (effective string, err error) {
	effective = mode
	fail := func(e error) (string, error) {
		if bg != nil && mode == "sixel" {
			effective = "none"
		}
		return effective, e
	}
	if !validMode(mode) {
		return fail(fmt.Errorf("unknown graphics mode %q", mode))
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fail(err)
	}
	f, err := os.CreateTemp(stateDir, "graphics-decision.*.tmp")
	if err != nil {
		return fail(err)
	}
	tmp := f.Name()
	bgText := "-"
	if bg != nil {
		bgText = fmt.Sprintf("%02x%02x%02x", bg.R, bg.G, bg.B)
	}
	if _, err := fmt.Fprintf(f, "%s %s\n", mode, bgText); err != nil {
		f.Close()
		os.Remove(tmp)
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fail(err)
	}
	if err := os.Rename(tmp, DecisionFile(stateDir)); err != nil {
		os.Remove(tmp)
		return fail(err)
	}
	return mode, nil
}

// Apply 读布局当前的画法与合成背景色：背景色设进本进程，返回画法。侧栏、额度面板每次取画法时都走它，deck up 改了会跟着变。
// 优先读决策文件（一个文件，原子）；没有它才读旧版本的两个文件，读到「sixel 但没有背景」当字母徽章（升级迁移：
// 旧版 deck up 在 iTerm2 上可能留下这种混合状态，按透明画法画就是实心方块；Windows Terminal 重新 deck up 一次就写成明确的「sixel -」）。
func Apply(stateDir string) string {
	switch mode, bg, st := loadDecision(stateDir); st {
	case decOK:
		SetBackground(bg)
		return mode
	case decBad:
		// 决策文件在、却读不懂（手改坏了、被截断、将来的新格式、不是普通文件）：不退到旧文件——旧文件是更早的决策，
		// 可能是 sixel 加背景，会把当前的决定（比如 none）盖回去。按最保守的字母徽章画，等下一次 deck up 重写。
		SetBackground(nil)
		return "none"
	}
	bg := CurrentBackground(stateDir)
	SetBackground(bg)
	mode := legacyMode(stateDir)
	if b, err := readSmall(ModeFile(stateDir)); err == nil && strings.TrimSpace(string(b)) == "sixel" && bg == nil {
		return "none"
	}
	return mode
}

const maxRegisters = 256 // iTerm2 报告的颜色寄存器数（XTSMGRAPHICS，诊断样张 1）

// SixelOn 把 img 缩到 w×h，合成到背景 bg 上，编成不留空洞的 Sixel（P2=0，每个像素都画）。
// 颜色少（不超过 255 种）就用原色，不量化；否则量化到 6×6×6 色立方，背景色仍单独占一个寄存器、保持精确。
func SixelOn(img image.Image, w, h int, bg color.RGBA) string {
	if w <= 0 || h <= 0 {
		return ""
	}
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
	// 寄存器：先按背景色排第 0 号分配，其余按颜色值排序（输出稳定）；写出前把背景色挪到最后一个寄存器
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
			if nearerBackground(c, bg) { // 几乎透明的边缘像素：吸到色立方会比背景亮/暗一档，在 logo 周围出现光晕
				continue
			}
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
			if nearerBackground(c, bg) {
				index[c] = 0
				continue
			}
			index[c] = cube[cubeOf(c)]
		}
	}
	// 背景色排在最后一个寄存器（n069 经 tmux 的真机样张 9）：tmux 把图再编码时按寄存器号从小到大定义颜色，并把图补到整格、
	// 补出的像素没画；iTerm2 把没画的涂成「最后定义的颜色」。背景色是最后一个，补出来的区域就是背景色，看不见
	n := len(palette)
	remapped := make([]color.RGBA, 0, n)
	remapped = append(remapped, palette[1:]...)
	remapped = append(remapped, palette[0])
	palette = remapped
	for k, v := range index {
		if v == 0 {
			index[k] = n - 1
		} else {
			index[k] = v - 1
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

// nearerBackground：c 离背景色比离它自己的色立方色更近（平方距离）。这样的像素归到精确的背景寄存器 0。
func nearerBackground(c, bg color.RGBA) bool {
	d := func(a, b color.RGBA) int {
		dr, dg, db := int(a.R)-int(b.R), int(a.G)-int(b.G), int(a.B)-int(b.B)
		return dr*dr + dg*dg + db*db
	}
	r, g, b := cubeRGB(cubeOf(c))
	return d(c, bg) <= d(c, color.RGBA{r, g, b, 255})
}

// SelectedRowBG 是侧栏选中会话行的底色（xterm 256 色 236，sidebar.go 里的 \x1b[48;5;236m）。
var SelectedRowBG = color.RGBA{48, 48, 48, 255}

// SixelOver 同 Sixel，但合成背景换成 over（图盖在有底色的行上时用）；透明画法（没设合成背景）时与 Sixel 一样。
func SixelOver(img image.Image, w, h int, over color.RGBA) string {
	if Background() == nil {
		return SixelTransparent(img, w, h)
	}
	return SixelOn(img, w, h, over)
}

// FitHeight 在合成画法（iTerm2）下把图的边长取成 6 的整数倍且不超过 n（n 是这几行的总像素）；透明画法原样返回 n（n056 验收的尺寸不变）。
//
// 原因（n069 经 tmux 的真机样张 9，隔离 tmux 3.4 复现）：sixel 的数据按 6 行一段，32 像素高的图数据是 36 行；
// tmux 按它自己的格子（没问到终端像素时是 16×32）数，36 行超过一格，于是把图占成两格并补齐、多出一整行的区域（再编码后声明 60 行）。
// 取 30 行就恰好一格（声明 30 行），不再多占下面一行。补出的零头像素颜色是背景（SixelOn 把背景色放在最后一个寄存器）。
//
// 不足 6 像素（一个 sixel 段都放不下）时返回 0，表示「这里不画图」：SixelOn 对 0 边长返回空串（上层按没有图处理）。
// 以前返回 6，会比预留的像素还高（与「不超过」的承诺相反）。
func FitHeight(n int) int {
	if Background() == nil {
		return n
	}
	return n / 6 * 6
}
