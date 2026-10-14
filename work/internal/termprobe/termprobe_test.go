package termprobe

import (
	"fmt"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// openPty 开一对 pty（主端、从端），不引新依赖。
func openPty() (*os.File, *os.File, error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		m.Close()
		return nil, nil, err
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		m.Close()
		return nil, nil, err
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		return nil, nil, err
	}
	return m, s, nil
}

// Windows Terminal 的真实回答（n052 探针，owner 机器上记录）。
const wtAnswer = "\x1b[6;20;10t\x1b[?61;4;6;7;14;21;22;23;24;28;32;42;52c"

func TestParseWindowsTerminal(t *testing.T) {
	r := Parse(wtAnswer)
	if r.CellW != 10 || r.CellH != 20 || !r.Sixel || !r.DA1 {
		t.Fatalf("%+v", r)
	}
}

func TestParseNoSixelNoCell(t *testing.T) {
	r := Parse("\x1b[?62;1;22c")
	if r.Sixel || r.CellW != 0 || !r.DA1 {
		t.Fatalf("没声明 4、没回答 16t：%+v", r)
	}
	if r := Parse("\x1b[?64;14c"); r.Sixel {
		t.Fatal("14 不是 4")
	}
	if r := Parse(""); r.DA1 || r.Sixel {
		t.Fatal("没回答")
	}
}

func TestMode(t *testing.T) {
	wt := Parse(wtAnswer)
	cases := []struct {
		forced string
		r      Result
		native bool
		want   string
	}{
		{"", wt, false, "sixel"},
		{"", Result{Sixel: true, DA1: true}, false, "none"}, // 声明 sixel 但不知道格子像素：tmux 画不出
		{"", Result{Sixel: true, DA1: true}, true, "sixel"}, // 终端自己报了像素
		{"", Result{DA1: true, CellW: 10, CellH: 20}, false, "none"},
		{"", Result{}, false, "none"}, // 不再自动选色块
		{"blocks", wt, false, "blocks"},
		{"none", wt, false, "none"},
	}
	for _, c := range cases {
		if got := Mode(c.forced, c.r, c.native); got != c.want {
			t.Errorf("Mode(%q, %+v, %v) = %q，应为 %q", c.forced, c.r, c.native, got, c.want)
		}
	}
}

// 在 pty 上：像素为 0 时补写、行列不动；已有像素不碰；格子未知不写。
func TestFixPixels(t *testing.T) {
	ptmx, tty, err := openPty()
	if err != nil {
		t.Skip("开不了 pty：", err)
	}
	defer ptmx.Close()
	defer tty.Close()
	fd := int(tty.Fd())
	set := func(ws unix.Winsize) {
		if err := unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &ws); err != nil {
			t.Fatal(err)
		}
	}
	set(unix.Winsize{Row: 60, Col: 236})
	if done, err := FixPixels(fd, 0, 0); done || err != nil {
		t.Fatal("格子未知不写")
	}
	if done, err := FixPixels(fd, 10, 20); !done || err != nil {
		t.Fatal(done, err)
	}
	ws, _ := Winsize(fd)
	if ws.Row != 60 || ws.Col != 236 || ws.Xpixel != 2360 || ws.Ypixel != 1200 {
		t.Fatalf("%+v", ws)
	}
	set(unix.Winsize{Row: 60, Col: 236, Xpixel: 1888, Ypixel: 1080})
	if done, _ := FixPixels(fd, 10, 20); done {
		t.Fatal("终端自己报了像素就不碰")
	}
	if ws, _ := Winsize(fd); ws.Xpixel != 1888 {
		t.Fatalf("%+v", ws)
	}
}

// Query 在 pty 上发问并收回答（模拟终端：从主端读到问题就回 Windows Terminal 的回答）。
func TestQueryOverPty(t *testing.T) {
	ptmx, tty, err := openPty()
	if err != nil {
		t.Skip("开不了 pty：", err)
	}
	defer ptmx.Close()
	defer tty.Close()
	go func() {
		buf := make([]byte, 64)
		got := ""
		for len(got) < len("\x1b[16t\x1b[c") {
			n, err := ptmx.Read(buf)
			if err != nil {
				return
			}
			got += string(buf[:n])
		}
		ptmx.Write([]byte(wtAnswer))
	}()
	r := Query(tty, 2*time.Second)
	if r.CellW != 10 || r.CellH != 20 || !r.Sixel {
		t.Fatalf("%+v", r)
	}
	// 不是终端：空结果
	f, _ := os.CreateTemp(t.TempDir(), "x")
	if r := Query(f, 100*time.Millisecond); r.DA1 {
		t.Fatal("文件不是终端")
	}
}
