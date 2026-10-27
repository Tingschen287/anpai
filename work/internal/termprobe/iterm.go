package termprobe

import (
	"image/color"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/unix"
)

// n069：iTerm2 把 sixel 里没画的像素涂上颜色（诊断样张 2），要把 logo 合成到终端背景色上再画（logo.SixelOn）。
// 这需要知道背景色：向终端问 OSC 11。只在认出是 iTerm2 时才问，别的终端（Windows Terminal 等）的查询字节与行为不变。

var (
	osc11Re = regexp.MustCompile(`\x1b\]11;rgba?:([0-9a-fA-F]{1,4})/([0-9a-fA-F]{1,4})/([0-9a-fA-F]{1,4})(?:/[0-9a-fA-F]{1,4})?(?:\x07|\x1b\\)`)
	xtverRe = regexp.MustCompile(`\x1bP>\|([^\x1b\x07]*)(?:\x07|\x1b\\)`)
)

// BGResult 是 iTerm2 专用查询（XTVERSION 与 OSC 11）的回答。
type BGResult struct {
	Term  string      // XTVERSION 回答，例如「iTerm2 3.7.6」；没回答为空
	BG    *color.RGBA // OSC 11 回答的背景色；没回答为 nil
	Asked bool        // 收到了 DA1 哨兵（终端按顺序回答完了）
}

// ParseBG 从终端回送的字节里取出 XTVERSION 与 OSC 11 的回答。
func ParseBG(resp string) BGResult {
	var r BGResult
	if m := xtverRe.FindStringSubmatch(resp); m != nil {
		r.Term = strings.TrimSpace(m[1])
	}
	if m := osc11Re.FindStringSubmatch(resp); m != nil {
		comp := func(s string) (uint8, bool) {
			v, err := strconv.ParseUint(s, 16, 32)
			if err != nil {
				return 0, false
			}
			max := uint64(1)<<(4*uint(len(s))) - 1 // 1～4 位十六进制：按位数换算到 0..255
			return uint8((v*255 + max/2) / max), true
		}
		rr, ok1 := comp(m[1])
		gg, ok2 := comp(m[2])
		bb, ok3 := comp(m[3])
		if ok1 && ok2 && ok3 {
			r.BG = &color.RGBA{rr, gg, bb, 255}
		}
	}
	r.Asked = da1Re.MatchString(resp)
	return r
}

// QueryBackground 问 XTVERSION 与 OSC 11（后面跟 DA1 当哨兵），最多等 timeout。
// 不是终端、切不到原始模式时返回空结果。调用前由调用方判断是 iTerm2（IsITerm2Env）。
func QueryBackground(tty *os.File, timeout time.Duration) BGResult {
	fd := tty.Fd()
	if !term.IsTerminal(fd) {
		return BGResult{}
	}
	old, err := term.MakeRaw(fd)
	if err != nil {
		return BGResult{}
	}
	defer term.Restore(fd, old)
	if _, err := tty.Write([]byte("\x1b[>0q\x1b]11;?\x07\x1b[c")); err != nil {
		return BGResult{}
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
		flushInput(int(fd)) // 同 Query：迟到的回答不能留在输入缓冲里
	}
	return ParseBG(string(buf))
}

// IsITerm2Env 按环境变量认 iTerm2（它自己设 TERM_PROGRAM=iTerm.app 与 LC_TERMINAL=iTerm2）。
func IsITerm2Env(getenv func(string) string) bool {
	return getenv("TERM_PROGRAM") == "iTerm.app" || getenv("LC_TERMINAL") == "iTerm2"
}

// Decision 是画法加合成背景。
type Decision struct {
	Mode       string      // sixel、blocks、none
	Background *color.RGBA // 非 nil：sixel 合成到这个背景上（iTerm2）；nil：透明画法
}

// Decide 在 Mode 的基础上处理 iTerm2：它的 sixel 不能留透明像素，要合成到背景色上。
//   - 不是 iTerm2：与 Mode 完全一样，Background 为 nil（Windows Terminal 等不变）；
//   - iTerm2 且问到了背景色：sixel 带上背景色；
//   - iTerm2 但没问到背景色：自动判断时退回字母徽章（画出来是实心方块比没有图更糟）；
//     owner 用 -graphics sixel 强制时尊重，仍用透明画法。
func Decide(forced string, r Result, native bool, iterm bool, bg BGResult) Decision {
	d := Decision{Mode: Mode(forced, r, native)}
	if d.Mode != "sixel" || !iterm {
		return d
	}
	if bg.BG != nil {
		d.Background = bg.BG
		return d
	}
	if forced == "" {
		d.Mode = "none"
	}
	return d
}
