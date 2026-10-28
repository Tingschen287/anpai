package deckui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
)

// n071：按显示宽度折行，中英混排都能用。
// 一行里能断开的地方：空格、两个汉字之间。不断开的：一串英文数字符号（agent、Ctrl-Q）、
// 不能放在行首的收尾标点（，。）」等）、不能放在行尾的起首标点（（「等）。
// 单个不可分的整体比 limit 还宽时，才在中间硬断（避免死循环、也避免超出窗口）。

// 起首标点：全角括号、直角引号、书名号、方头括号、弯引号（用码点写：文字表之外不留中文字面量，n060 的扫描会查）。
var wrapOpening = string([]rune{0xFF08, 0x300C, 0x300E, 0x3008, 0x300A, 0x3010, 0x201C, 0x2018, '(', '['})

// 收尾标点：对应的右括号与引号、逗号句号顿号分号冒号叹号问号、省略号；「/」也并到前一个字后面（「显示/隐藏」不在斜线前断开）。
var wrapClosing = string([]rune{0xFF09, 0x300D, 0x300F, 0x3009, 0x300B, 0x3011, 0x201D, 0x2019,
	0xFF0C, 0x3002, 0x3001, 0xFF1B, 0xFF1A, 0xFF01, 0xFF1F, 0x2026, ')', ']', ',', '.', ';', ':', '!', '?', '/'})

// wrapUnits 把文字切成不可再分的单元（空格单独成一个「 」单元）。
func wrapUnits(s string) []string {
	var units []string
	var cur []rune // 正在攒的一串英文数字符号
	glue := false  // 上一个字是起首标点：下一个字要并到它后面
	last := func() int { return len(units) - 1 }
	canJoin := func() bool { return len(units) > 0 && units[last()] != " " }
	flush := func() {
		if len(cur) > 0 {
			units = append(units, string(cur))
			cur = nil
		}
	}
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			flush()
			units = append(units, " ")
			glue = false
		case lipgloss.Width(string(r)) >= 2: // 汉字、全角符号：自成一个单元，除非要并到前一个
			flush()
			if canJoin() && (glue || strings.ContainsRune(wrapClosing, r)) {
				units[last()] += string(r)
			} else {
				units = append(units, string(r))
			}
			glue = strings.ContainsRune(wrapOpening, r)
		default: // 英文数字符号：连成一串
			if len(cur) == 0 && canJoin() {
				if glue { // 「（」后面接英文：并成一个单元
					cur = []rune(units[last()])
					units = units[:last()]
				} else if strings.ContainsRune(wrapClosing, r) { // 汉字后面的「/」之类：并到汉字后面
					units[last()] += string(r)
					continue
				}
			}
			cur = append(cur, r)
			glue = false
		}
	}
	flush()
	return units
}

// wrapText 把 s 折成每行不超过 limit 列的若干行（行首行尾不留空格）。limit<1 或 s 本来就放得下时原样一行。
func wrapText(s string, limit int) []string {
	if limit < 1 || lipgloss.Width(s) <= limit {
		return []string{s}
	}
	var lines []string
	var line strings.Builder
	lineW := 0
	emit := func() {
		lines = append(lines, strings.TrimRight(line.String(), " "))
		line.Reset()
		lineW = 0
	}
	for _, u := range wrapUnits(s) {
		w := lipgloss.Width(u)
		if u == " " {
			if lineW > 0 && lineW+1 <= limit {
				line.WriteString(" ")
				lineW++
			}
			continue
		}
		if lineW+w > limit && lineW > 0 {
			emit()
		}
		for w > limit { // 单元自己就比一行宽：硬断
			cut, cw := 0, 0
			for i, r := range u {
				rw := lipgloss.Width(string(r))
				if cw+rw > limit {
					break
				}
				cw += rw
				cut = i + len(string(r))
			}
			if cut == 0 { // limit 比一个字还窄：至少放一个
				for _, r := range u {
					cut = len(string(r))
					break
				}
			}
			line.WriteString(u[:cut])
			lineW += lipgloss.Width(u[:cut])
			emit()
			u = u[cut:]
			w = lipgloss.Width(u)
		}
		line.WriteString(u)
		lineW += w
	}
	if lineW > 0 || len(lines) == 0 {
		emit()
	}
	return lines
}
