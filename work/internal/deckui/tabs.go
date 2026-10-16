package deckui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/logo"
)

// Tabs 是中间上方一行的标签条：打开集合里的每个会话一个标签，焦点高亮；点标签换焦点、点 ✕ 放回后台；
// F2（布局 server 把它转给这里）接管当前显示的子 agent（架构稿 §2、§7）。
type Tabs struct {
	Data Data
	hits []tabHit
}

type tabHit struct {
	from, to int // 列范围 [from, to)
	close    int // ✕ 所在的列
	id       string
}

// Hit 返回点在第 x 列的是哪个标签、是否点在 ✕ 上。
func (t *Tabs) Hit(x int) (id string, close bool, ok bool) {
	for _, h := range t.hits {
		if x >= h.from && x < h.to {
			return h.id, x >= h.close, true
		}
	}
	return "", false, false
}

func (t *Tabs) find(id string) (SessionInfo, bool) {
	for _, x := range t.Data.Sessions {
		if x.AgentDeckID == id {
			return x, true
		}
	}
	return SessionInfo{}, false
}

// Focused 是中间正显示的会话（没有返回 false）。
func (t *Tabs) Focused() (SessionInfo, bool) {
	if t.Data.View.Focus == "" {
		return SessionInfo{}, false
	}
	return t.find(t.Data.View.Focus)
}

// Hint 是标签条右边的提示：接管键能做什么、现在是不是你在操作。
func (t *Tabs) Hint() string {
	x, ok := t.Focused()
	switch {
	case len(t.Data.View.Open) == 0:
		return "\x1b[38;5;242m" + T("tabs.empty") + "\x1b[0m"
	case !ok:
		return ""
	case t.Data.IsPending(x):
		return "\x1b[38;5;214m" + T("tabs.pending") + "\x1b[0m"
	case t.Data.IsTaken(x):
		return "\x1b[38;5;214m" + T("tabs.taken") + "\x1b[0m"
	case x.Role == "sub":
		return "\x1b[38;5;242m" + T("tabs.readonly") + "\x1b[0m"
	}
	return ""
}

func badge(tool string) string {
	b, ok := logo.Brand[tool]
	if !ok {
		return "\x1b[38;5;245m·\x1b[0m"
	}
	return fmt.Sprintf("\x1b[1;38;2;%d;%d;%dm%s\x1b[0m", b.Color.R, b.Color.G, b.Color.B, b.Letter)
}

// Render 画一行（宽 w）。
func (t *Tabs) Render(w int) Frame {
	t.hits = nil
	hint := t.Hint()
	room := w - Width(hint) - 1
	var b strings.Builder
	col := 0
	for _, id := range t.Data.View.Open {
		x, ok := t.find(id)
		title, tool := sanitize(id), ""
		if ok {
			title, tool = sanitize(x.Title), x.Tool
		}
		mark := ""
		if ok && t.Data.IsTaken(x) {
			mark = "✋"
		} else if ok && t.Data.IsPending(x) {
			mark = "⏸"
		}
		label := Fit(title, 14)
		if mark != "" {
			label = Fit(ansi.Truncate(title, 11, "…")+" "+mark, 14) // 标记紧跟标题，再补齐
		}
		seg := " " + badge(tool) + " " + label + " "
		segW := Width(seg) + 2 // 加 ✕ 与空格
		if col+segW > room {
			b.WriteString("\x1b[38;5;242m…\x1b[0m")
			col++
			break
		}
		style := "\x1b[38;5;250m"
		if id == t.Data.View.Focus {
			style = "\x1b[1;48;5;238m"
		}
		b.WriteString(style + seg + "\x1b[38;5;242m✕\x1b[0m" + style + " \x1b[0m")
		t.hits = append(t.hits, tabHit{from: col, to: col + segW, close: col + segW - 2, id: id})
		col += segW
	}
	line := b.String()
	if pad := w - col - Width(hint); pad > 0 {
		line += strings.Repeat(" ", pad) + hint
	}
	return Frame{Lines: []string{line}}
}
