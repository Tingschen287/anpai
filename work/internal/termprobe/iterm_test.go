package termprobe

import (
	"image/color"
	"os"
	"testing"
	"time"
)

// iTerm2 3.7.6 真机的回答（n069 诊断样张 1，owner 机器上记录）：CSI 16 t 是点（8x19），TIOCGWINSZ 是设备像素（16x38）。
const itermAnswer = "\x1b[6;19;8t\x1b[?64;1;2;4;6;17;18;21;22;52c"

func TestParseITerm2RealAnswer(t *testing.T) {
	r := Parse(itermAnswer)
	if r.CellW != 8 || r.CellH != 19 || !r.Sixel || !r.DA1 {
		t.Fatalf("%+v", r)
	}
	// 自动判断：声明了 sixel 且（TIOCGWINSZ 自己有像素或问到了格子）→ sixel
	if m := Mode("", r, true); m != "sixel" {
		t.Fatalf("iTerm2 应判 sixel：%s", m)
	}
}

func TestParseBGAnswers(t *testing.T) {
	cases := []struct {
		name, in string
		term     string
		bg       *color.RGBA
	}{
		{"iTerm2 的 XTVERSION 与 OSC 11（四位十六进制，ST 结尾）", "\x1bP>|iTerm2 3.7.6\x1b\\\x1b]11;rgb:1e1e/2020/2424\x1b\\\x1b[?64;1;2;4c", "iTerm2 3.7.6", &color.RGBA{0x1e, 0x20, 0x24, 255}},
		{"BEL 结尾", "\x1b]11;rgb:0000/0000/0000\x07\x1b[?64c", "", &color.RGBA{0, 0, 0, 255}},
		{"两位十六进制", "\x1b]11;rgb:ff/80/00\x07\x1b[?1c", "", &color.RGBA{255, 128, 0, 255}},
		{"一位十六进制", "\x1b]11;rgb:f/0/8\x07\x1b[?1c", "", &color.RGBA{255, 0, 136, 255}},
		{"rgba 形式", "\x1b]11;rgba:ffff/0000/0000/ffff\x07\x1b[?1c", "", &color.RGBA{255, 0, 0, 255}},
		{"没回答 OSC 11", "\x1b[?64;4c", "", nil},
		{"颜色不是十六进制", "\x1b]11;rgb:zz/00/00\x07\x1b[?1c", "", nil},
		{"空", "", "", nil},
	}
	for _, c := range cases {
		r := ParseBG(c.in)
		if r.Term != c.term {
			t.Errorf("%s：Term %q，应为 %q", c.name, r.Term, c.term)
		}
		switch {
		case c.bg == nil && r.BG != nil:
			t.Errorf("%s：不该有背景色：%v", c.name, *r.BG)
		case c.bg != nil && (r.BG == nil || *r.BG != *c.bg):
			t.Errorf("%s：背景色 %v，应为 %v", c.name, r.BG, *c.bg)
		}
	}
	if !ParseBG("\x1b[?64c").Asked || ParseBG("x").Asked {
		t.Error("Asked 应看 DA1 哨兵")
	}
}

func TestIsITerm2Env(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if !IsITerm2Env(env(map[string]string{"TERM_PROGRAM": "iTerm.app"})) || !IsITerm2Env(env(map[string]string{"LC_TERMINAL": "iTerm2"})) {
		t.Fatal("两个环境变量都该认")
	}
	for _, m := range []map[string]string{{}, {"TERM_PROGRAM": "Apple_Terminal"}, {"WT_SESSION": "x"}, {"TERM_PROGRAM": "WezTerm"}} {
		if IsITerm2Env(env(m)) {
			t.Fatalf("不是 iTerm2：%v", m)
		}
	}
}

// Decide：Windows Terminal 与别的终端的判断与 Mode 完全一样、没有背景色（n056 验收的透明画法不变）；
// iTerm2 问到背景色就合成；问不到就退回字母徽章（实心方块比没有图更糟），除非 owner 用 -graphics sixel 强制。
func TestDecide(t *testing.T) {
	wt := Parse(wtAnswer)
	it := Parse(itermAnswer)
	bg := BGResult{Term: "iTerm2 3.7.6", BG: &color.RGBA{16, 16, 16, 255}}
	cases := []struct {
		name   string
		forced string
		r      Result
		native bool
		iterm  bool
		bg     BGResult
		mode   string
		hasBG  bool
	}{
		{"Windows Terminal：sixel，透明画法", "", wt, false, false, BGResult{}, "sixel", false},
		{"Windows Terminal 就算有人回了背景色也不合成", "", wt, false, false, bg, "sixel", false},
		{"iTerm2 有背景色：sixel 合成", "", it, true, true, bg, "sixel", true},
		{"iTerm2 没问到背景色：字母徽章", "", it, true, true, BGResult{}, "none", false},
		{"iTerm2 强制 sixel 没背景色：尊重，透明画法", "sixel", it, true, true, BGResult{}, "sixel", false},
		{"iTerm2 强制 sixel 有背景色：合成", "sixel", it, true, true, bg, "sixel", true},
		{"iTerm2 强制 none", "none", it, true, true, bg, "none", false},
		{"iTerm2 强制 blocks", "blocks", it, true, true, bg, "blocks", false},
		{"iTerm2 没声明 sixel", "", Parse("\x1b[?62;1c"), true, true, bg, "none", false},
		{"普通终端没声明 sixel", "", Parse("\x1b[?62;1c"), false, false, BGResult{}, "none", false},
	}
	for _, c := range cases {
		d := Decide(c.forced, c.r, c.native, c.iterm, c.bg)
		if d.Mode != c.mode || (d.Background != nil) != c.hasBG {
			t.Errorf("%s：得 %+v，应为 mode=%s hasBG=%v", c.name, d, c.mode, c.hasBG)
		}
		if !c.iterm && d.Mode != Mode(c.forced, c.r, c.native) {
			t.Errorf("%s：非 iTerm2 必须与 Mode 一致", c.name)
		}
	}
}

// QueryBackground 在 pty 上发问并收回答（模拟 iTerm2：问 XTVERSION 与 OSC 11 都答，最后 DA1）。
func TestQueryBackgroundOverPty(t *testing.T) {
	ptmx, tty, err := openPty()
	if err != nil {
		t.Skip("开不了 pty：", err)
	}
	defer ptmx.Close()
	defer tty.Close()
	asked := make(chan string, 1)
	go func() {
		buf := make([]byte, 128)
		got := ""
		for len(got) < len("\x1b[>0q\x1b]11;?\x07\x1b[c") {
			n, err := ptmx.Read(buf)
			if err != nil {
				return
			}
			got += string(buf[:n])
		}
		asked <- got
		ptmx.Write([]byte("\x1bP>|iTerm2 3.7.6\x1b\\\x1b]11;rgb:1e1e/1e1e/1e1e\x1b\\\x1b[?64;4c"))
	}()
	r := QueryBackground(tty, 2*time.Second)
	if q := <-asked; q != "\x1b[>0q\x1b]11;?\x07\x1b[c" {
		t.Fatalf("发出的查询：%q", q)
	}
	if r.Term != "iTerm2 3.7.6" || r.BG == nil || *r.BG != (color.RGBA{0x1e, 0x1e, 0x1e, 255}) || !r.Asked {
		t.Fatalf("%+v", r)
	}
	// 不是终端：空结果
	if r := QueryBackground(nil2(t), 100*time.Millisecond); r.Asked || r.BG != nil {
		t.Fatal("文件不是终端")
	}
}

func nil2(t *testing.T) *os.File {
	f, err := os.CreateTemp(t.TempDir(), "x")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// 环境变量说是 iTerm2，但终端自己报了别的名字（tmux 或 ssh 转发了 LC_TERMINAL，实际是别的终端）：按非 iTerm2 处理，透明画法不变（对抗自查 F3）。
func TestDecideTrustsXTVERSIONOverEnv(t *testing.T) {
	wt := Parse(wtAnswer)
	bg := BGResult{Term: "WezTerm 20240203", BG: &color.RGBA{16, 16, 16, 255}}
	if d := Decide("", wt, false, true, bg); d.Mode != "sixel" || d.Background != nil {
		t.Fatalf("别的终端不该合成背景：%+v", d)
	}
	// 没报名字（XTVERSION 没回答）才信环境变量
	if d := Decide("", Parse(itermAnswer), true, true, BGResult{BG: &color.RGBA{16, 16, 16, 255}}); d.Background == nil {
		t.Fatal("没报名字时信环境变量")
	}
}
