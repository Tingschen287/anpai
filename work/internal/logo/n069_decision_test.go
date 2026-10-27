package logo

import (
	"image/color"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Codex 审查 1 的 R2：画法（graphics）与合成背景（graphics-bg）是两个文件，各自原子 rename 并不让这一对原子。
// 两个 deck up 交错时可能留下谁都没决定过的「sixel + 没有背景」（iTerm2 上是实心方块），重读也恢复不了。
// WriteDecision 持跨进程的决策锁整体写一对，Apply 读时持同一把锁。

type decision struct {
	mode string
	bg   *color.RGBA
}

func (d decision) String() string {
	if d.bg == nil {
		return d.mode + "/-"
	}
	return d.mode + "/" + BackgroundKeyOf(*d.bg)
}

// BackgroundKeyOf 给测试用：六位十六进制。
func BackgroundKeyOf(c color.RGBA) string {
	const hex = "0123456789abcdef"
	return string([]byte{hex[c.R>>4], hex[c.R&15], hex[c.G>>4], hex[c.G&15], hex[c.B>>4], hex[c.B&15]})
}

func readPair(d string) decision {
	mode := Apply(d)
	return decision{mode, Background()}
}

// 复现 Codex 的反例顺序：A（none，没背景）与 B（sixel，101010）交错；最后的状态必须是其中一个完整的决策，不是 (sixel, 无背景)。
func TestWriteDecisionInterleavedWritersNeverMix(t *testing.T) {
	t.Cleanup(func() { SetBackground(nil) })
	bg := color.RGBA{16, 16, 16, 255}
	A, B := decision{"none", nil}, decision{"sixel", &bg}
	for round := 0; round < 200; round++ {
		d := t.TempDir()
		var wg sync.WaitGroup
		wg.Add(2)
		start := make(chan struct{})
		go func() { defer wg.Done(); <-start; WriteDecision(d, A.mode, A.bg) }()
		go func() { defer wg.Done(); <-start; WriteDecision(d, B.mode, B.bg) }()
		close(start)
		wg.Wait()
		got := readPair(d)
		if got.mode == "sixel" && got.bg == nil {
			t.Fatalf("第 %d 轮：留下了谁都没决定过的 (sixel, 无背景)", round)
		}
		if got.String() != A.String() && got.String() != B.String() {
			t.Fatalf("第 %d 轮：最终状态 %v 不是任何一个写者的完整决策", round, got)
		}
	}
}

// 多个写者不断写、读者不断读：读者读到的每一对都是某个写者写的完整决策。
func TestApplyAlwaysSeesACompleteDecision(t *testing.T) {
	t.Cleanup(func() { SetBackground(nil) })
	d := t.TempDir()
	c1, c2 := color.RGBA{16, 16, 16, 255}, color.RGBA{255, 255, 255, 255}
	set := map[string]bool{}
	decs := []decision{{"none", nil}, {"sixel", &c1}, {"sixel", &c2}, {"blocks", nil}, {"sixel", nil}} // 最后一个是 Windows Terminal：sixel 且没有背景，本来就是合法决策
	for _, x := range decs {
		set[x.String()] = true
	}
	WriteDecision(d, "none", nil)
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

// Windows Terminal：sixel 且没有背景是合法的完整决策（透明画法），WriteDecision 把旧背景文件删掉。
func TestWriteDecisionTransparentRemovesOldBackground(t *testing.T) {
	t.Cleanup(func() { SetBackground(nil) })
	d := t.TempDir()
	bg := color.RGBA{1, 2, 3, 255}
	WriteDecision(d, "sixel", &bg)
	if got := readPair(d); got.bg == nil {
		t.Fatal("前提")
	}
	if eff, err := WriteDecision(d, "sixel", nil); err != nil || eff != "sixel" {
		t.Fatalf("%q %v", eff, err)
	}
	if got := readPair(d); got.mode != "sixel" || got.bg != nil {
		t.Fatalf("WT：sixel 加透明画法：%v", got)
	}
}

// 背景色写不下：画法退回 none，不留下「sixel + 旧背景/无背景」（C-1）。
func TestWriteDecisionBackgroundWriteFailureFallsBackToNone(t *testing.T) {
	t.Cleanup(func() { SetBackground(nil) })
	d := t.TempDir()
	if err := os.Mkdir(BackgroundFile(d), 0o700); err != nil { // 目标是个目录：rename 过不去
		t.Fatal(err)
	}
	bg := color.RGBA{16, 16, 16, 255}
	eff, err := WriteDecision(d, "sixel", &bg)
	if err == nil || eff != "none" {
		t.Fatalf("应报错并退回 none：%q %v", eff, err)
	}
	if got := readPair(d); got.mode != "none" {
		t.Fatalf("文件里应是 none：%v", got)
	}
	// 没有背景色要写（强制 sixel、WT）时，背景文件写不下不影响画法：删除一个不存在的文件不算错
	d2 := t.TempDir()
	if eff, err := WriteDecision(d2, "sixel", nil); err != nil || eff != "sixel" {
		t.Fatalf("%q %v", eff, err)
	}
}

// 决策锁是跨进程的 flock：别的进程（这里用另一个文件句柄）占着锁时，写者等它放开再写。
func TestWriteDecisionWaitsForTheDecisionLock(t *testing.T) {
	t.Cleanup(func() { SetBackground(nil) })
	d := t.TempDir()
	hold := lockDecision(d, true)
	done := make(chan struct{})
	go func() { WriteDecision(d, "blocks", nil); close(done) }()
	select {
	case <-done:
		t.Fatal("锁被占着，写者不该写完")
	case <-time.After(150 * time.Millisecond):
	}
	if _, err := os.Stat(filepath.Join(d, "graphics")); err == nil {
		t.Fatal("锁被占着，画法文件不该已经写出")
	}
	hold()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("放锁后写者应写完")
	}
	if readPair(d).mode != "blocks" {
		t.Fatal("放锁后应写成功")
	}
}
