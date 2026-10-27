package termprobe

import (
	"image/color"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/unix"
)

// A1：DECK_GRAPHICS / -graphics 写成不认识的值（auto、Auto、default、1……）时，Mode 当作没指定（自动判断），
// 但 Decide 只看 forced == ""，把它当成「owner 强制」：iTerm2 没问到背景色也不退回字母徽章，透明画法的 sixel = 实心方块。
func TestADV_DecideUnknownForcedValueIsTreatedAsForced(t *testing.T) {
	it := Parse(itermAnswer)
	for _, forced := range []string{"auto", "default", "yes", "1"} {
		d := Decide(forced, it, true, true, BGResult{}) // iTerm2，没问到背景色
		t.Logf("forced=%q → %+v", forced, d)
		if d.Mode == "sixel" && d.Background == nil {
			t.Errorf("forced=%q 不是合法值（Mode 当自动），却在 iTerm2 无背景色时仍选透明画法 sixel（实心方块）", forced)
		}
	}
}

// A2：ParseBG 的正则回溯：Go 的 regexp 是 RE2（线性，不会指数爆炸）。实测：耗时随长度线性增长（倍数 ≤3 算线性）。
func TestADV_ParseBGLongInputIsLinear(t *testing.T) {
	shapes := map[string]func(n int) string{
		"重复前缀无结尾":      func(n int) string { return strings.Repeat("\x1b]11;rgb:ffff/ffff/", n) },
		"重复 ESC":       func(n int) string { return strings.Repeat("\x1b", n) },
		"超长 XTVERSION": func(n int) string { return "\x1bP>|" + strings.Repeat("a", n) },
		"重复 DA1 前缀":    func(n int) string { return strings.Repeat("\x1b[?1;", n) },
	}
	for name, mk := range shapes {
		var d1, d2 time.Duration
		for k, n := range []int{1 << 18, 1 << 19} {
			in := mk(n)
			t0 := time.Now()
			ParseBG(in)
			if k == 0 {
				d1 = time.Since(t0)
			} else {
				d2 = time.Since(t0)
			}
		}
		t.Logf("%-14s n=256K %-10v n=512K %-10v 倍数 %.1f", name, d1, d2, float64(d2)/float64(d1+1))
		if float64(d2) > 3.5*float64(d1)+float64(5*time.Millisecond) {
			t.Errorf("%s：长度翻倍耗时涨了 %.1f 倍，不是线性", name, float64(d2)/float64(d1+1))
		}
	}
}

// A3：颜色位数、多个回答、哨兵缺失/重复/混序时 ParseBG 的取值。
func TestADV_ParseBGMalformed(t *testing.T) {
	cases := []struct {
		name, in string
		want     *color.RGBA
	}{
		{"五位十六进制", "\x1b]11;rgb:12345/12345/12345\x07\x1b[?1c", nil},
		{"空分量", "\x1b]11;rgb://\x07", nil},
		{"三种位数混用", "\x1b]11;rgb:f/ff/ffff\x07", &color.RGBA{255, 255, 255, 255}},
		{"两个回答取第一个", "\x1b]11;rgb:0000/0000/0000\x07\x1b]11;rgb:ffff/ffff/ffff\x07\x1b[?1c", &color.RGBA{0, 0, 0, 255}},
		{"回答中间混入转义", "\x1b]11;rgb:0000/\x1b[?1c0000/0000\x07", nil},
		{"OSC 10（前景）不当背景", "\x1b]10;rgb:ffff/ffff/ffff\x07\x1b[?1c", nil},
		{"没有终止符", "\x1b]11;rgb:0000/0000/0000", nil},
		{"终止符是 CR", "\x1b]11;rgb:0000/0000/0000\r", nil},
		{"#rrggbb 形式（有些终端这么答）", "\x1b]11;#1e1e1e\x07", nil},
		{"rgb 分量 3 位", "\x1b]11;rgb:fff/000/800\x07", &color.RGBA{255, 0, 128, 255}},
	}
	for _, c := range cases {
		r := ParseBG(c.in)
		switch {
		case c.want == nil && r.BG != nil:
			t.Errorf("%s：不该有颜色，得 %v", c.name, *r.BG)
		case c.want != nil && (r.BG == nil || *r.BG != *c.want):
			t.Errorf("%s：得 %v，应为 %v", c.name, r.BG, *c.want)
		}
	}
	// 哨兵：混在 OSC 回答里的 DA1 样字节也算哨兵
	if !ParseBG("\x1b]11;rgb:00/00/\x1b[?1c").Asked {
		t.Log("DA1 样字节出现在别的序列里不算哨兵")
	} else {
		t.Log("（已知）哨兵正则不区分上下文：被截断的 OSC 11 后面跟 DA1 也算 Asked")
	}
}

// A4：DA1 先于 OSC 11 到达（终端不按序、或中间有 tmux / ssh 重排）。QueryBackground 看到 DA1 就返回，
// 迟到的 OSC 11 回答留在输入缓冲里，之后会被 tmux 当按键敲进窗格。
func TestADV_QueryBackgroundLateOSC11AfterDA1(t *testing.T) {
	ptmx, tty, err := openPty()
	if err != nil {
		t.Skip(err)
	}
	defer ptmx.Close()
	defer tty.Close()
	go func() {
		buf := make([]byte, 64)
		got := ""
		for len(got) < len("\x1b[>0q\x1b]11;?\x07\x1b[c") {
			n, err := ptmx.Read(buf)
			if err != nil {
				return
			}
			got += string(buf[:n])
		}
		ptmx.Write([]byte("\x1b[?64;4c")) // 先答 DA1
		time.Sleep(150 * time.Millisecond)
		ptmx.Write([]byte("\x1b]11;rgb:1e1e/1e1e/1e1e\x1b\\")) // 再答 OSC 11
	}()
	r := QueryBackground(tty, 2*time.Second)
	t.Logf("返回 BG=%v Asked=%v", r.BG, r.Asked)
	time.Sleep(400 * time.Millisecond)
	// 从端现在还能读到什么？（切原始模式，非阻塞地读）
	fd := int(tty.Fd())
	old, _ := term.MakeRaw(uintptr(fd))
	defer term.Restore(uintptr(fd), old)
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, _ := unix.Poll(fds, 100)
	if n > 0 {
		b := make([]byte, 128)
		k, _ := unix.Read(fd, b)
		t.Errorf("QueryBackground 返回后输入缓冲里有迟到的回答 %q；背景色也没拿到（%v）", b[:k], r.BG)
	}
}

// A5：超时（终端慢/不回答 DA1）：flush 只清「已经到了的」，flush 之后才到的回答仍会漏进输入。
func TestADV_QueryBackgroundTimeoutThenLateAnswer(t *testing.T) {
	ptmx, tty, err := openPty()
	if err != nil {
		t.Skip(err)
	}
	defer ptmx.Close()
	defer tty.Close()
	go func() {
		time.Sleep(250 * time.Millisecond) // 超时（200ms）之后才答
		ptmx.Write([]byte("\x1bP>|iTerm2 3.7.6\x1b\\\x1b]11;rgb:1e1e/1e1e/1e1e\x1b\\\x1b[?64;4c"))
	}()
	t0 := time.Now()
	r := QueryBackground(tty, 200*time.Millisecond)
	t.Logf("返回用时 %v BG=%v", time.Since(t0), r.BG)
	time.Sleep(300 * time.Millisecond)
	fd := int(tty.Fd())
	old, _ := term.MakeRaw(uintptr(fd))
	defer term.Restore(uintptr(fd), old)
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	if n, _ := unix.Poll(fds, 100); n > 0 {
		b := make([]byte, 256)
		k, _ := unix.Read(fd, b)
		t.Errorf("超时后迟到的回答留在了输入缓冲：%q（接上 tmux 后会变成按键）", b[:k])
	}
}

// A6：终端狂灌数据：QueryBackground 每读 256 字节就对整个缓冲重跑 DA1 正则，缓冲无上限 → O(n²)，且超过 deadline 才发现。
func TestADV_QueryBackgroundFloodOverrunsDeadline(t *testing.T) {
	ptmx, tty, err := openPty()
	if err != nil {
		t.Skip(err)
	}
	defer ptmx.Close()
	defer tty.Close()
	var stop atomic.Bool
	go func() {
		junk := []byte(strings.Repeat("x", 4096))
		for !stop.Load() {
			if _, err := ptmx.Write(junk); err != nil {
				return
			}
		}
	}()
	t0 := time.Now()
	QueryBackground(tty, 300*time.Millisecond)
	d := time.Since(t0)
	stop.Store(true)
	t.Logf("超时设 300ms，实际用时 %v", d)
	if d > 600*time.Millisecond {
		t.Errorf("被灌数据时超出期限很多：%v", d)
	}
}

// A7：iTerm2 判断只看环境变量；XTVERSION 的回答（Term）被解析了却从不核对。
func TestADV_DecideIgnoresXTVERSION(t *testing.T) {
	it := Parse(itermAnswer)
	bg := BGResult{Term: "tmux 3.4", BG: &color.RGBA{0, 0, 0, 255}}
	d := Decide("", it, true, true, bg)
	t.Logf("环境说是 iTerm2、终端回答 XTVERSION=%q → %+v", bg.Term, d)
	if d.Background != nil && !strings.Contains(bg.Term, "iTerm2") {
		t.Log("（观察）XTVERSION 不是 iTerm2 也照样合成背景；多数情况下无害（合成画法对任何终端都能显示），但没有用它做二次确认")
	}
}
