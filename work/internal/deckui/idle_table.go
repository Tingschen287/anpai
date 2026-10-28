package deckui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/Tingschen287/terminal-deck/work/internal/layout"
)

// n071：空闲页的版式（owner 确认的草图 4.eval/results/n071_deck_ui_tidy/sketch.md，A「整块居中，块内左对齐」）。
// logo、品牌名、提示句各自水平居中；快捷键说明是一张两列表格：整块水平居中（按整块最宽一行算左边距），
// 块内两列各自左对齐——左列按键（最长按键加两格），右列说明，说明放不下就折行，续行从说明列的左边界开始。
// 窄到放不下按键列加最短的说明，就退回 IdleLines 的逐行居中写法。

// IdleRow 是空闲页上的一行：Text 从第 Col 列（从 1 起）开始画。
type IdleRow struct {
	Text string
	Col  int
}

// idleMinDesc 是说明列最窄的列数；再窄就退回逐行写法（说明折成太多行比整行居中更难读）。
const idleMinDesc = 16

// idleCenterCol 是整行居中时的起始列（从 1 起；左右边距相差不超过 1 列。n058 以来的旧公式少算了 1，行宽接近窗口宽时会贴左边缘）。
func idleCenterCol(w, textW int) int { return max(1, (w-textW)/2+1) }

// IdleRows 是空闲页的全部文字行及各自的起始列。w 是终端的列数（<=0 表示不知道，不折行）。
// 前四行（产品名、空行、提示句、空行）各自居中；其后是快捷键表格。
// 高度不够不改版式：宽度够就一直是两列表格，画不下的行由 IdlePlace 在底部裁掉（n071 attempt 2，Codex 审查 R2）。
func IdleRows(w int) []IdleRow {
	flat := IdleLines(w)
	limit := w - 2 // 两侧各留 1 列；窗口窄到 10 列以下就不留边（R4）：没有一行会超过窗口宽度，必要时词也按显示宽度拆开
	if w < 10 {
		limit = w
	}
	// centered 把每一行各自居中；这一行比窗口宽就折行，每一段各自居中
	centered := func(lines []string) []IdleRow {
		var out []IdleRow
		for _, l := range lines {
			pieces := []string{l}
			if limit >= 1 {
				pieces = wrapText(l, limit)
			}
			for _, p := range pieces {
				out = append(out, IdleRow{Text: p, Col: idleCenterCol(w, lipgloss.Width(p))})
			}
		}
		return out
	}
	const head = 4 // IdleLines 的前四行：产品名、空行、提示句、空行
	keys := layout.Keys()
	if len(flat) < head || len(flat) != head+len(keys) {
		return centered(flat)
	}
	keyW, shortest, longest := 0, 1<<30, 0
	for _, k := range keys {
		keyW = max(keyW, lipgloss.Width(k.Key))
		d := lipgloss.Width(k.What)
		shortest, longest = min(shortest, d), max(longest, d)
	}
	avail := limit - keyW - 2 // 扣掉按键列与它后面的两格
	if avail < max(idleMinDesc, shortest) {
		return centered(flat)
	}
	descW := min(longest, avail)
	var table []string
	tableW := 0
	for _, k := range keys {
		for i, l := range wrapText(k.What, descW) {
			key := ""
			if i == 0 {
				key = k.Key
			}
			row := strings.TrimRight(padKey(key, keyW)+"  "+l, " ")
			table = append(table, row)
			tableW = max(tableW, lipgloss.Width(row))
		}
	}
	out := centered(flat[:head])
	col := idleCenterCol(w, tableW)
	for _, l := range table {
		out = append(out, IdleRow{Text: l, Col: col})
	}
	return out
}

func padKey(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// IdlePut 是空闲页上要画的一行：画在屏幕第 Row 行、第 Col 列（都从 1 起）；Brand 为真表示这是产品名那一行（用品牌色）。
type IdlePut struct {
	Row, Col int
	Text     string
	Brand    bool
}

// IdlePlace 把各行摆到屏幕上：第一行画在第 top 行，之后依次往下；h>0 时画到窗外的行直接丢掉
// （窗外的行会被终端钳在最后一行，叠成一团）。cmd/deck 的 idle() 只负责把结果写到终端。
func IdlePlace(rows []IdleRow, top, h int) []IdlePut {
	var out []IdlePut
	for i, r := range rows {
		if h > 0 && top+i > h {
			break
		}
		out = append(out, IdlePut{Row: top + i, Col: r.Col, Text: r.Text, Brand: i == 0})
	}
	return out
}
