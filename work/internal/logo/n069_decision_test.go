package logo

import (
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// 画法与合成背景色是一个决策，只写在一个文件里（graphics-decision），原子 rename 写、读者只读它：
// 读到的一定是某个写者写下的完整决策，不依赖任何锁（Codex 审查 1 的 R2 与复审 2 的 R2-1，owner 选根治方案）。

type decision struct {
	mode string
	bg   *color.RGBA
}

func (d decision) String() string {
	if d.bg == nil {
		return d.mode + "/-"
	}
	return d.mode + "/" + hex6(*d.bg)
}

func hex6(c color.RGBA) string {
	const h = "0123456789abcdef"
	return string([]byte{h[c.R>>4], h[c.R&15], h[c.G>>4], h[c.G&15], h[c.B>>4], h[c.B&15]})
}

func readPair(d string) decision {
	mode := Apply(d)
	return decision{mode, Background()}
}

// 两个写者交错：最终状态必须是其中一个的完整决策，不会是谁都没决定过的 (sixel, 无背景)。
func TestWriteDecisionInterleavedWritersNeverMix(t *testing.T) {
	t.Cleanup(func() { SetBackground(nil) })
	bg := color.RGBA{16, 16, 16, 255}
	A, B := decision{"none", nil}, decision{"sixel", &bg}
	for round := 0; round < 200; round++ {
		d := t.TempDir()
		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, x := range []decision{A, B} {
			wg.Add(1)
			go func(x decision) { defer wg.Done(); <-start; WriteDecision(d, x.mode, x.bg) }(x)
		}
		close(start)
		wg.Wait()
		got := readPair(d)
		if got.String() != A.String() && got.String() != B.String() {
			t.Fatalf("第 %d 轮：最终状态 %v 不是任何一个写者的完整决策", round, got)
		}
	}
}

// 写者与读者并发：读者读到的每一对都是某个写者写的完整决策。
func TestApplyAlwaysSeesACompleteDecision(t *testing.T) {
	t.Cleanup(func() { SetBackground(nil) })
	d := t.TempDir()
	c1, c2 := color.RGBA{16, 16, 16, 255}, color.RGBA{255, 255, 255, 255}
	set := map[string]bool{}
	// 最后一个是 Windows Terminal：sixel 加透明画法，是合法决策。故意不放 none/-：读到半份文件时读者会退到 none/-，
	// 它若也在合法集合里，非原子的写法（先截断再写）就测不出来
	decs := []decision{{"sixel", &c1}, {"sixel", &c2}, {"blocks", nil}, {"sixel", nil}}
	for _, x := range decs {
		set[x.String()] = true
	}
	WriteDecision(d, "blocks", nil)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				x := decs[(i+w)%len(decs)]
				WriteDecision(d, x.mode, x.bg)
			}
		}(w)
	}
	bad := ""
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) && bad == "" {
		if got := readPair(d); !set[got.String()] {
			bad = got.String()
		}
	}
	close(stop)
	wg.Wait()
	if bad != "" {
		t.Fatalf("读到了不属于任何写者的一对：%s", bad)
	}
}

// Codex 复审 2 的两个反例换成新设计下的等价场景：
// 写者写到一半被暂停（临时文件已建、内容只写了一半、还没 rename）：读者读到的仍是上一个完整决策，另一个写者照常写完。
// 暂停的写者醒来 rename：最终是它那份完整的决策，不是混合。
func TestPausedWriterNeverExposesHalfADecision(t *testing.T) {
	t.Cleanup(func() { SetBackground(nil) })
	d := t.TempDir()
	bg := color.RGBA{16, 16, 16, 255}
	if _, err := WriteDecision(d, "sixel", &bg); err != nil {
		t.Fatal(err)
	}
	// A（none，无背景）暂停在 rename 之前：临时文件只写了一半
	tmp := filepath.Join(d, "graphics-decision.paused.tmp")
	if err := os.WriteFile(tmp, []byte("no"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readPair(d); got.String() != "sixel/101010" {
		t.Fatalf("写者暂停时读者应读到上一个完整决策：%v", got)
	}
	// B（blocks）在 A 暂停期间完整写下
	if _, err := WriteDecision(d, "blocks", nil); err != nil {
		t.Fatal(err)
	}
	if got := readPair(d); got.String() != "blocks/-" {
		t.Fatalf("%v", got)
	}
	// A 醒来：补全临时文件、rename——最终是 A 那一份完整决策
	if err := os.WriteFile(tmp, []byte("none -\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, DecisionFile(d)); err != nil {
		t.Fatal(err)
	}
	if got := readPair(d); got.String() != "none/-" {
		t.Fatalf("%v", got)
	}
}

// 决策文件坏了（截断、乱码、多余字段、过长）：当作没有，退回旧文件或环境变量，不当成合法决策。
func TestCorruptDecisionFileIsIgnored(t *testing.T) {
	t.Cleanup(func() { SetBackground(nil) })
	t.Setenv("DECK_GRAPHICS", "")
	for _, c := range []string{"", "si", "sixel", "sixel zzzzzz\n", "sixel 12345\n", "sixel 1234567\n", "bogus -\n", "sixel - extra\n", "sixel 1e1e1e\x00\n", strings.Repeat("x", 4096)} {
		d := t.TempDir()
		os.WriteFile(DecisionFile(d), []byte(c), 0o600)
		if _, _, ok := ReadDecision(d); ok {
			t.Fatalf("%q 不该算合法决策", c)
		}
		if got := readPair(d); got.mode != "none" || got.bg != nil { // 没有别的文件、环境变量也没设：Mode() 的默认
			t.Fatalf("%q：应退回默认 none：%v", c, got)
		}
		// 旧版本留下的更早的决策（sixel 加背景）不能把它盖回去：决策文件在、读不懂就按字母徽章，不退到旧文件
		d2 := t.TempDir()
		bg := color.RGBA{16, 16, 16, 255}
		WriteMode(d2, "sixel")
		WriteBackground(d2, &bg)
		os.WriteFile(DecisionFile(d2), []byte(c), 0o600)
		if got := readPair(d2); got.mode != "none" || got.bg != nil {
			t.Fatalf("%q：决策文件在但读不懂，不该退到旧文件：%v", c, got)
		}
		t.Setenv("DECK_GRAPHICS", "sixel") // owner 的强制也不能让读不懂的决策画成无背景的 sixel（iTerm2 上是实心方块）
		if got := readPair(d2); got.mode != "none" || got.bg != nil {
			t.Fatalf("%q：强制 sixel 时：%v", c, got)
		}
		t.Setenv("DECK_GRAPHICS", "")
	}
	// 决策文件是个目录（读不了）
	d3 := t.TempDir()
	os.Mkdir(DecisionFile(d3), 0o700)
	WriteMode(d3, "sixel")
	if got := readPair(d3); got.mode != "none" {
		t.Fatalf("目录：%v", got)
	}
}

// Windows Terminal：sixel 加透明画法是明确的合法决策；新决策文件盖过旧版留下的背景文件。
func TestTransparentSixelIsALegalDecisionAndBeatsLegacyFiles(t *testing.T) {
	t.Cleanup(func() { SetBackground(nil) })
	d := t.TempDir()
	old := color.RGBA{1, 2, 3, 255}
	WriteMode(d, "sixel") // 旧版留下的两个文件
	WriteBackground(d, &old)
	if got := readPair(d); got.String() != "sixel/010203" {
		t.Fatalf("前提（旧文件）：%v", got)
	}
	if eff, err := WriteDecision(d, "sixel", nil); err != nil || eff != "sixel" {
		t.Fatalf("%q %v", eff, err)
	}
	if got := readPair(d); got.mode != "sixel" || got.bg != nil {
		t.Fatalf("WT：sixel 加透明画法，不被旧背景文件改写：%v", got)
	}
}

// 升级迁移：没有决策文件时读旧的两个文件；读到「sixel 但没有背景」当字母徽章（旧版 deck up 可能留下的混合状态），
// 有背景就照旧，其余画法照旧；一个文件都没有时按环境变量（owner 强制 sixel 仍然是 sixel）。
func TestLegacyFilesAreReadOnlyWhenThereIsNoDecisionFile(t *testing.T) {
	t.Cleanup(func() { SetBackground(nil) })
	t.Setenv("DECK_GRAPHICS", "")
	bg := color.RGBA{16, 16, 16, 255}
	cases := []struct {
		name  string
		mode  string
		bg    *color.RGBA
		wantM string
		wantB string
	}{
		{"旧版 sixel 加背景", "sixel", &bg, "sixel", "101010"},
		{"旧版 sixel 没有背景（混合状态）", "sixel", nil, "none", "-"},
		{"旧版 none", "none", nil, "none", "-"},
		{"旧版 blocks", "blocks", nil, "blocks", "-"},
	}
	for _, c := range cases {
		d := t.TempDir()
		WriteMode(d, c.mode)
		WriteBackground(d, c.bg)
		got := readPair(d)
		gb := "-"
		if got.bg != nil {
			gb = hex6(*got.bg)
		}
		if got.mode != c.wantM || gb != c.wantB {
			t.Errorf("%s：得 %v，应为 %s/%s", c.name, got, c.wantM, c.wantB)
		}
	}
	// 一个文件都没有：环境变量
	t.Setenv("DECK_GRAPHICS", "sixel")
	if got := readPair(t.TempDir()); got.mode != "sixel" || got.bg != nil {
		t.Fatalf("没有任何文件时按环境变量（owner 强制）：%v", got)
	}
	// 有了新决策文件就不再看旧文件
	d := t.TempDir()
	WriteMode(d, "sixel")
	WriteDecision(d, "none", nil)
	if got := readPair(d); got.mode != "none" {
		t.Fatalf("%v", got)
	}
}

// 写不下：sixel 加背景退回 none；没有背景色的决策照原样返回；不留临时文件。
func TestWriteDecisionFailureAndNoTempFiles(t *testing.T) {
	t.Cleanup(func() { SetBackground(nil) })
	d := t.TempDir()
	if err := os.Mkdir(DecisionFile(d), 0o700); err != nil { // 目标是个目录：rename 过不去
		t.Fatal(err)
	}
	bg := color.RGBA{16, 16, 16, 255}
	if eff, err := WriteDecision(d, "sixel", &bg); err == nil || eff != "none" {
		t.Fatalf("应报错并退回 none：%q %v", eff, err)
	}
	if eff, err := WriteDecision(d, "sixel", nil); err == nil || eff != "sixel" {
		t.Fatalf("没有背景色的决策写不下也不改画法：%q %v", eff, err)
	}
	if eff, err := WriteDecision(d, "bogus", nil); err == nil || eff != "bogus" {
		t.Fatalf("不认识的画法不写：%q %v", eff, err)
	}
	if left, _ := filepath.Glob(filepath.Join(d, "graphics-decision.*.tmp")); len(left) != 0 {
		t.Fatalf("临时文件应清掉：%v", left)
	}
	// 状态目录不存在时建出来
	d2 := filepath.Join(t.TempDir(), "state", "deck")
	if _, err := WriteDecision(d2, "sixel", &bg); err != nil {
		t.Fatal(err)
	}
	if got := readPair(d2); got.String() != "sixel/101010" {
		t.Fatalf("%v", got)
	}
}

// 决策文件只有一行、权限 0600。
func TestDecisionFileFormat(t *testing.T) {
	d := t.TempDir()
	bg := color.RGBA{0x1e, 0x20, 0x24, 255}
	WriteDecision(d, "sixel", &bg)
	b, _ := os.ReadFile(DecisionFile(d))
	if string(b) != "sixel 1e2024\n" {
		t.Fatalf("%q", b)
	}
	fi, _ := os.Stat(DecisionFile(d))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("权限 %v", fi.Mode().Perm())
	}
	WriteDecision(d, "sixel", nil)
	if b, _ := os.ReadFile(DecisionFile(d)); string(b) != "sixel -\n" {
		t.Fatalf("%q", b)
	}
}

// 样张 11 里 Kimi 的 K 下面那条细灰线（复审 2 的核对项）：不是补齐残留，是素材自己的。
// 每个工具的图在 iTerm2 常见的两行格子（60×60）和一行（30×30）上合成：每个像素都必须画出来（没有没画的），
// 且颜色与「素材缩放后合成到背景」的差不超过色立方的半格（10%）；kimi.png 是完全不透明的 64×64 图（alpha 全为 255），最下面十几行从 y=48 的 RGB 24 一路渐变到 y=63 的 RGB 113（灰），
// 所以这条线在素材里就有，输出与素材一致，没有多出来的东西。
func TestEveryToolLogoMatchesItsCompositedSourceNoPaddingResidue(t *testing.T) {
	t.Cleanup(func() { SetBackground(nil) })
	bg := color.RGBA{30, 30, 30, 255}
	for _, tool := range []string{"claude", "codex", "antigravity", "grok", "kimi", "opencode"} {
		im := For(tool)
		if im == nil {
			t.Fatalf("%s 没有素材", tool)
		}
		for _, side := range []int{30, 60, 72} {
			grid, cs, _ := decodeAny(t, SixelOn(im, side, side, bg))
			if len(grid) != side {
				t.Fatalf("%s/%d：行数 %d", tool, side, len(grid))
			}
			src := sample(im, side, side)
			worst := 0
			for y := 0; y < side; y++ {
				for x := 0; x < side; x++ {
					reg := grid[y][x]
					if reg < 0 {
						t.Fatalf("%s/%d (%d,%d) 没画：iTerm2 会涂成最后定义的颜色", tool, side, x, y)
					}
					c := src[y][x]
					a := uint32(c.A)
					mix := func(f, b uint8) int { return pct(uint8((uint32(f)*a + uint32(b)*(255-a) + 127) / 255)) }
					want := [3]int{mix(c.R, bg.R), mix(c.G, bg.G), mix(c.B, bg.B)}
					for k := 0; k < 3; k++ {
						d := cs[reg][k] - want[k]
						if d < 0 {
							d = -d
						}
						worst = max(worst, d)
					}
				}
			}
			if worst > 11 {
				t.Fatalf("%s/%d：有像素与素材合成后的颜色差 %d%%（量化最多差 10%%）", tool, side, worst)
			}
		}
	}
}

// 对抗自查 D1：决策文件第一次出现的瞬间，读者不能把刚写好的完整决策当成「在却读不懂」。
// 旧版留下 sixel 加背景；新版写同样的 sixel 加背景。写的过程中，读者每一次读到的都必须是 sixel/同一个背景（旧或新都是它），
// 不能闪一下 none。
func TestApplyNeverFlashesWhenTheDecisionFileAppears(t *testing.T) {
	t.Cleanup(func() { SetBackground(nil) })
	t.Setenv("DECK_GRAPHICS", "")
	bg := color.RGBA{30, 30, 30, 255}
	for round := 0; round < 1500; round++ {
		d := t.TempDir()
		WriteMode(d, "sixel")
		WriteBackground(d, &bg)
		done := make(chan struct{})
		go func() { WriteDecision(d, "sixel", &bg); close(done) }()
		for finished := false; !finished; {
			select {
			case <-done:
				finished = true
			default:
			}
			if got := readPair(d); got.String() != "sixel/1e1e1e" {
				t.Fatalf("第 %d 轮：决策文件出现的瞬间读到 %v（应一直是 sixel/1e1e1e）", round, got)
			}
		}
	}
}

// 对抗自查 D2：决策文件、旧文件是命名管道时读者不能永远阻塞，按读不懂处理。
func TestApplyDoesNotBlockOnNamedPipes(t *testing.T) {
	t.Cleanup(func() { SetBackground(nil) })
	t.Setenv("DECK_GRAPHICS", "")
	run := func(setup func(d string)) decision {
		d := t.TempDir()
		setup(d)
		ch := make(chan decision, 1)
		go func() { ch <- readPair(d) }()
		select {
		case got := <-ch:
			return got
		case <-time.After(3 * time.Second):
			t.Fatal("读者被命名管道卡住了")
			return decision{}
		}
	}
	if got := run(func(d string) { syscall.Mkfifo(DecisionFile(d), 0o600) }); got.mode != "none" || got.bg != nil {
		t.Fatalf("决策文件是命名管道：%v", got)
	}
	if got := run(func(d string) { syscall.Mkfifo(ModeFile(d), 0o600) }); got.mode != "none" || got.bg != nil { // Mode() 的默认
		t.Fatalf("旧画法文件是命名管道：%v", got)
	}
	if got := run(func(d string) { WriteMode(d, "sixel"); syscall.Mkfifo(BackgroundFile(d), 0o600) }); got.mode != "none" || got.bg != nil {
		t.Fatalf("旧背景文件是命名管道（sixel 没有背景退回字母徽章）：%v", got)
	}
}
