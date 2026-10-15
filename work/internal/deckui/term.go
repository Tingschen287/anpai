// Package deckui 是布局里的 owner 界面：左侧会话栏（deck sidebar）与中间上方的标签条（deck tabs）（n049，架构稿 §7）。
//
// 不用 Bubble Tea：侧栏要在指定位置画 Sixel 图，而 Bubble Tea 按行比对重绘，图会被覆盖又不重画。
// 这里每次数据变化就整屏重画（先文字、后图），窗格很小，代价可以忽略。读数据、做动作都走 deckd 的 API。
package deckui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Event 是一次输入：按键或鼠标。
type Event struct {
	Key   string // up、down、left、right、enter、esc、tab、backspace、f1…f12、pgup、pgdn、home、end、ctrl-c，或一个字符
	Mouse *Mouse
}

// Mouse 是一次鼠标事件（坐标从 0 起）。
type Mouse struct {
	X, Y   int
	Button int  // 0 左键、1 中键、2 右键
	Press  bool // 按下（松开为 false）
	Wheel  int  // -1 上滚、1 下滚、0 不是滚轮
}

var csiTilde = map[string]string{"1": "home", "4": "end", "5": "pgup", "6": "pgdn", "15": "f5", "17": "f6", "18": "f7", "19": "f8",
	"20": "f9", "21": "f10", "23": "f11", "24": "f12", "2": "insert", "3": "delete"}

// Parse 把一段输入字节解析成事件；末尾不完整的转义序列留在 rest 里，等下一段输入。
// flush 为 true（等了一会儿没有后续）时，单独一个 ESC 算作 esc 键。
func Parse(buf []byte, flush bool) (events []Event, rest []byte) {
	i := 0
	for i < len(buf) {
		c := buf[i]
		if c != 0x1b {
			switch c {
			case '\r', '\n':
				events = append(events, Event{Key: "enter"})
			case '\t':
				events = append(events, Event{Key: "tab"})
			case 0x7f, 0x08:
				events = append(events, Event{Key: "backspace"})
			case 0x03:
				events = append(events, Event{Key: "ctrl-c"})
			case 0x11:
				events = append(events, Event{Key: "ctrl-q"})
			default:
				if c < 0x20 {
					break
				}
				r, size := decodeRune(buf[i:])
				if size == 0 {
					return events, buf[i:] // UTF-8 不完整
				}
				events = append(events, Event{Key: string(r)})
				i += size
				continue
			}
			i++
			continue
		}
		// 转义序列
		if i+1 >= len(buf) {
			if flush {
				events = append(events, Event{Key: "esc"})
				return events, nil
			}
			return events, buf[i:]
		}
		switch buf[i+1] {
		case 'O': // SS3：方向键与 F1–F4
			if i+2 >= len(buf) {
				return events, buf[i:]
			}
			if k, ok := map[byte]string{'A': "up", 'B': "down", 'C': "right", 'D': "left", 'H': "home", 'F': "end",
				'P': "f1", 'Q': "f2", 'R': "f3", 'S': "f4"}[buf[i+2]]; ok {
				events = append(events, Event{Key: k})
			}
			i += 3
		case '[':
			j := i + 2
			for j < len(buf) && (buf[j] < 0x40 || buf[j] > 0x7e) {
				j++
			}
			if j >= len(buf) {
				return events, buf[i:]
			}
			params, final := string(buf[i+2:j]), buf[j]
			if ev, ok := csi(params, final); ok {
				events = append(events, ev)
			}
			i = j + 1
		default:
			events = append(events, Event{Key: "esc"})
			i++
		}
	}
	return events, nil
}

func csi(params string, final byte) (Event, bool) {
	if strings.HasPrefix(params, "<") && (final == 'M' || final == 'm') { // SGR 鼠标
		f := strings.Split(params[1:], ";")
		if len(f) != 3 {
			return Event{}, false
		}
		b, _ := strconv.Atoi(f[0])
		x, _ := strconv.Atoi(f[1])
		y, _ := strconv.Atoi(f[2])
		m := &Mouse{X: x - 1, Y: y - 1, Button: b & 3, Press: final == 'M'}
		if b&64 != 0 {
			m.Wheel = -1
			if b&1 != 0 {
				m.Wheel = 1
			}
		}
		if b&32 != 0 { // 拖动：不处理
			return Event{}, false
		}
		return Event{Mouse: m}, true
	}
	switch final {
	case 'A':
		return Event{Key: "up"}, true
	case 'B':
		return Event{Key: "down"}, true
	case 'C':
		return Event{Key: "right"}, true
	case 'D':
		return Event{Key: "left"}, true
	case 'H':
		return Event{Key: "home"}, true
	case 'F':
		return Event{Key: "end"}, true
	case 'Z':
		return Event{Key: "backtab"}, true
	case '~':
		if k, ok := csiTilde[strings.Split(params, ";")[0]]; ok {
			return Event{Key: k}, true
		}
	}
	return Event{}, false
}

func decodeRune(b []byte) (rune, int) {
	if !utf8.FullRune(b) {
		return 0, 0
	}
	return utf8.DecodeRune(b)
}

// Image 是叠在文字上的一张 Sixel 图（左上角所在的行、列，从 0 起）。
type Image struct {
	Row, Col int
	Data     string
}

// Frame 是一整屏：文字行与叠加的图。
type Frame struct {
	Lines  []string
	Images []Image
}

// Encode 把一屏编成整屏重画的终端输出：逐行截到宽度、清到行尾，清掉多余的行，最后画图。
func (f Frame) Encode(width, height int) string {
	var b strings.Builder
	b.WriteString("\x1b[?25l\x1b[H")
	for i := 0; i < height; i++ {
		if i > 0 {
			b.WriteString("\r\n")
		}
		// 先擦掉整行再写（n058）：tmux 写双宽字符时不清压在格子上的旧 sixel 图，行首是「安排 Anpai」时，
		// 切成字母徽章后旧 logo 图留着，不支持 sixel 的终端上出「SIXEL IMAGE」占位。用擦行而不是铺满空格：
		// 宽度与窗格对不上（改大小的瞬间）时铺空格会自动折行（n058 自查）
		b.WriteString("\x1b[2K")
		if i < len(f.Lines) {
			b.WriteString(ansi.Truncate(f.Lines[i], width, ""))
		}
		b.WriteString("\x1b[0m\x1b[K")
	}
	for _, im := range f.Images {
		fmt.Fprintf(&b, "\x1b[%d;%dH%s", im.Row+1, im.Col+1, im.Data)
	}
	b.WriteString("\x1b[H")
	return b.String()
}

// Width 是一段带转义的文字在终端里占的列数。
func Width(s string) int { return ansi.StringWidth(s) }

// Fit 把文字截到 n 列，不足的补空格。
func Fit(s string, n int) string {
	if n <= 0 {
		return ""
	}
	s = ansi.Truncate(s, n, "…")
	if w := ansi.StringWidth(s); w < n {
		s += strings.Repeat(" ", n-w)
	}
	return s
}
