// Package termprobe 问外层终端「一格多少像素、支不支持 sixel」，并在 Linux 报给程序的像素尺寸为 0 时补上（n056）。
//
// 背景（n052 试用，4.eval/results/n052_owner_deck_trial/trial-notes.md 第 2、5 条）：WSL 经 ConPTY 接 Windows Terminal 时，
// TIOCGWINSZ 的像素字段永远是 0，每次改窗口大小还会重新清零；tmux 3.4 只从这里拿像素，为 0 就把 sixel 画成占位文字。
// Windows Terminal 自己会回答 CSI 16 t（一格 10×20，是它给 sixel 用的虚拟格）。deck up 先问到格子大小，
// 接上布局后把像素写进自己的终端，窗口每次改大小后再写一次；tmux 客户端收到 SIGWINCH 重新读取，就能画图。
package termprobe

import (
	"os"
	"regexp"
	"strconv"
	"time"

	"golang.org/x/sys/unix"

	"github.com/charmbracelet/x/term"
)

// Result 是终端的回答。
type Result struct {
	CellW, CellH int  // 一格的像素（CSI 16 t）；0 表示没回答
	Sixel        bool // DA1 里声明了 4（sixel）
	DA1          bool // 回答了 DA1（几乎所有终端都会；没回答说明不是交互终端或被吞了）
}

var (
	cellRe = regexp.MustCompile(`\x1b\[6;(\d+);(\d+)t`)
	da1Re  = regexp.MustCompile(`\x1b\[\?([\d;]*)c`)
)

// Parse 从终端回送的字节里取出 CSI 16 t 与 DA1 的回答。
func Parse(resp string) Result {
	var r Result
	if m := cellRe.FindStringSubmatch(resp); m != nil {
		h, _ := strconv.Atoi(m[1])
		w, _ := strconv.Atoi(m[2])
		if w > 0 && h > 0 && w < 1000 && h < 1000 {
			r.CellW, r.CellH = w, h
		}
	}
	if m := da1Re.FindStringSubmatch(resp); m != nil {
		r.DA1 = true
		for _, p := range splitSemi(m[1]) {
			if p == "4" {
				r.Sixel = true
			}
		}
	}
	return r
}

func splitSemi(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ';' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}

// Query 在 tty 上发 CSI 16 t 与 DA1，最多等 timeout 收回答（收到 DA1 就停：终端按顺序回答，DA1 在后）。
// 不是终端、切不到原始模式时返回空结果，不报错——调用方按「没回答」处理。
func Query(tty *os.File, timeout time.Duration) Result {
	fd := tty.Fd()
	if !term.IsTerminal(fd) {
		return Result{}
	}
	old, err := term.MakeRaw(fd)
	if err != nil {
		return Result{}
	}
	defer term.Restore(fd, old)
	if _, err := tty.Write([]byte("\x1b[16t\x1b[c")); err != nil {
		return Result{}
	}
	var buf []byte
	deadline := time.Now().Add(timeout)
	tmp := make([]byte, 256)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			break
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, int(left/time.Millisecond)+1)
		if err == unix.EINTR {
			continue
		}
		if err != nil || n == 0 {
			break
		}
		k, err := unix.Read(int(fd), tmp)
		if err != nil || k <= 0 {
			break
		}
		buf = append(buf, tmp[:k]...)
		if da1Re.Match(buf) {
			break
		}
	}
	if !da1Re.Match(buf) {
		// 超时：终端的回答可能还在路上，清掉输入缓冲，免得迟到的回答被 tmux 当成按键送进窗格（n056 自查）
		flushInput(int(fd))
	}
	return Parse(string(buf))
}

// Winsize 读 fd 的窗口尺寸。
func Winsize(fd int) (*unix.Winsize, error) { return unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ) }

// FixPixels 在像素字段为 0 时按格子大小补上（行列不动），返回是否写了。像素已有（终端自己报了）就不碰。
// 写入会让内核给前台进程组发 SIGWINCH，tmux 客户端借此重新读取并把像素报给 server。
func FixPixels(fd int, cellW, cellH int) (bool, error) {
	if cellW <= 0 || cellH <= 0 {
		return false, nil
	}
	ws, err := Winsize(fd)
	if err != nil {
		return false, err
	}
	if ws.Col == 0 || ws.Row == 0 || ws.Xpixel != 0 && ws.Ypixel != 0 {
		return false, nil
	}
	nw := *ws
	nw.Xpixel = uint16(min(int(ws.Col)*cellW, 0xffff))
	nw.Ypixel = uint16(min(int(ws.Row)*cellH, 0xffff))
	if err := unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &nw); err != nil {
		return false, err
	}
	return true, nil
}

// Mode 决定 logo 画法（sixel / none / blocks）：
//   - forced 非空就用它（DECK_GRAPHICS 或 -graphics）；
//   - 终端声明 sixel，且知道格子像素（回答了 CSI 16 t，或 TIOCGWINSZ 自己就有像素）：sixel；
//   - 其余：none（品牌色字母徽章）。不再自动选半格色块：4×2 格只有 4×4 像素，认不出（n052 owner 反馈）。
func Mode(forced string, r Result, haveNativePixels bool) string {
	switch forced {
	case "sixel", "blocks", "none":
		return forced
	}
	if r.Sixel && (r.CellW > 0 || haveNativePixels) {
		return "sixel"
	}
	return "none"
}
