package panels

import (
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/deckd"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// n071：进度面板的版式（owner 确认的草图 4.eval/results/n071_deck_ui_tidy/sketch.md，B「一行一项加百分比」）。
// 数字不用 %g；预算每行四段（标签、已用、上限加单位、百分比），各段起止列在所有行里一致；对齐一律按显示宽度（中文 2 列）。

// ---------------- 数字 ----------------

// numScale 是一档缩写：不小于 div 的数除以 div 后写上 suffix。
type numScale struct {
	div    float64
	suffix string
}

// numScales 按当前语言给出缩写档位，从小到大。每档比上一档大 1 千（英文）或 1 万（中文）倍以上。
// 中文：万、亿、万亿；英文：K、M、B、T。
func numScales() []numScale {
	if i18n.Lang() == i18n.EN {
		return []numScale{{1e3, i18n.T("num.k")}, {1e6, i18n.T("num.m")}, {1e9, i18n.T("num.b")}, {1e12, i18n.T("num.t")}}
	}
	return []numScale{{1e4, i18n.T("num.wan")}, {1e8, i18n.T("num.yi")}, {1e12, i18n.T("num.wanyi")}}
}

// numCap 是能写的最大数：再大不写具体数字（也不写科学计数），写「>9999 万亿」或「>9999T」。
const numCap = 1e16

// fixed 把 v 按 digits 位小数写成十进制（永远不是科学计数），多余的尾零和小数点去掉。
func fixed(v float64, digits int) string {
	s := strconv.FormatFloat(v, 'f', digits, 64)
	if digits > 0 && strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s == "-0" {
		s = "0"
	}
	return s
}

// roundQ 按「3 位有效数字上下」的规则取整：小于 100 保留一位小数（去掉多余的 .0），不小于 100 取整数。
func roundQ(q float64) (float64, string) {
	if q < 100 {
		r := math.Round(q*10) / 10
		return r, fixed(r, 1)
	}
	r := math.Round(q)
	return r, fixed(r, 0)
}

// formatCount 把预算里的数字写成人读的样子（n071）。
// 中文：整数照写，不小于 1 万写「986 万」、不小于 1 亿写「1.2 亿」（至多一位小数，去掉多余的 .0）；
// 英文：不小于 1000 写 K、不小于 100 万写 M（9.9M、15M）。
// NaN、±Inf、负数写成「未知」，不会 panic，也不会出现科学计数（e+06）。
func formatCount(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return i18n.T("progress.val.unknown")
	}
	scales := numScales()
	if v >= numCap {
		return ">9999" + scales[len(scales)-1].suffix
	}
	if v < scales[0].div {
		if r := math.Round(v*10) / 10; r < scales[0].div {
			if v == math.Trunc(v) {
				return fixed(v, 0)
			}
			return fixed(r, 1)
		}
		// 9999.96 之类：写一位小数会凑成「10000」，改走缩写
	}
	i := 0
	for i+1 < len(scales) && v >= scales[i+1].div {
		i++
	}
	for {
		r, s := roundQ(v / scales[i].div)
		// 四舍五入后凑满了下一档（例如 999.9996 K）：升一档重算
		if i+1 < len(scales) && r*scales[i].div >= scales[i+1].div {
			i++
			continue
		}
		if i+1 == len(scales) && r >= 10000 { // 最大一档四舍五入后凑成五位数：同样写「>9999」
			return ">9999" + scales[i].suffix
		}
		return s + scales[i].suffix
	}
}

// ---------------- 百分比 ----------------

// pctText 是已用占上限的百分比。任何一边未知、上限不大于 0（除零）、数值不是有限的，写「—」。
func pctText(used, limit *float64) string {
	if used == nil || limit == nil {
		return "—"
	}
	u, l := *used, *limit
	if math.IsNaN(u) || math.IsInf(u, 0) || math.IsNaN(l) || math.IsInf(l, 0) || u < 0 || l <= 0 {
		return "—"
	}
	p := u / l * 100
	if math.IsNaN(p) || math.IsInf(p, 0) || math.Round(p) >= 1000 {
		return ">999%"
	}
	r := int(math.Round(p))
	if r >= 100 && u < l {
		r = 99 // 还没用到上限，不写成 100%（99.5% 四舍五入会虚报用尽）
	}
	return strconv.Itoa(r) + "%"
}

// ---------------- 宽度小工具 ----------------

func padRight(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func padLeft(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}

// oneLine 把外部文字清洗成一行：去控制序列、限长、折空白（同其他面板的出口）。
//
// 顺序（n071 attempt 2，Codex 审查 R1）：先删格式字符，再走打码出口。反过来的话，夹在 sk- 中间的零宽字符会躲过打码、
// 删掉之后拼回一个完整的疑似密钥。最后把折成一行的结果再过一次出口——折空白不会拼出新的密钥，这一遍只是保证
// 「显示出去的字，再走一次出口不会再变」。
func oneLine(s string, limit int) string {
	s = deckstore.Display(stripFormat(s), limit)
	s = strings.Join(strings.Fields(stripFormat(s)), " ")
	return deckstore.Display(s, limit)
}

// stripFormat 删掉 Unicode 格式控制符（Cf：从右到左的 RLO/RLI、零宽空格、BOM 等）：它们宽度为 0，却能把后面的数字整段反排。
func stripFormat(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

// ---------------- 状态计数网格 ----------------

type countCell struct {
	label string
	n     int
}

// countGrid 把计数格排成两列网格：每格「标签 + 数字」，标签左对齐、数字右对齐到格内固定位置；
// 两列的左边界各自对齐，明细行用同样的格宽。放不下两列时逐格叠成一列。
func countGrid(cells []countCell, width int) []string {
	labW, numW := 0, 1
	for _, c := range cells {
		labW = max(labW, lipgloss.Width(c.label))
		numW = max(numW, len(strconv.Itoa(c.n)))
	}
	if room := width - 1 - numW; labW > room && room >= 2 {
		// 窄到连一格都放不下：标签用「…」截短，数字不动
		labW = room
		for i := range cells {
			if lipgloss.Width(cells[i].label) > labW {
				cells[i].label = ansi.Truncate(cells[i].label, labW, "…")
			}
		}
	}
	natural := labW + 1 + numW
	cols := 2
	if 2*natural+2 > width {
		cols = 1
	}
	cw := natural
	if cols == 2 {
		cw = min(max(natural, (width-2)/2), natural+4)
	}
	cell := func(c countCell) string {
		return padRight(c.label, cw-numW) + padLeft(strconv.Itoa(c.n), numW)
	}
	var rows []string
	for i := 0; i < len(cells); i += cols {
		line := cell(cells[i])
		if cols == 2 && i+1 < len(cells) {
			line += "  " + cell(cells[i+1])
		}
		rows = append(rows, strings.TrimRight(line, " "))
	}
	return rows
}

// ---------------- 预算行 ----------------

// budgetLabelKey 把 projectctl 给的预算名（英文原名）对到文字键；认不出的不在表里，原样经清洗出口显示。
var budgetLabelKey = map[string]i18n.Key{
	"worker rounds": "progress.budget.worker", "worker_rounds": "progress.budget.worker",
	"frontier calls": "progress.budget.frontier", "frontier_calls": "progress.budget.frontier",
	"elapsed": "progress.budget.elapsed",
	"tokens":  "progress.budget.tokens", "token": "progress.budget.tokens",
}

func budgetLabel(raw string) string {
	if k, ok := budgetLabelKey[strings.ToLower(strings.TrimSpace(raw))]; ok {
		return i18n.T(k)
	}
	if l := oneLine(raw, 40); l != "" {
		return l
	}
	return "—" // 标签是空的（或清洗后什么都不剩）：写一个破折号，不留空行
}

type budgetRow struct{ label, used, limit, pct, limNum, unit string }

// budgetRows 把预算按显示端要的四段拼好。已用、上限都读不到的维度不显示（和从前一样）；
// 只缺上限的写「已用 / —」，只缺已用的写「未知 / 上限」。
func budgetRows(bs []deckd.BudgetView) []budgetRow {
	var out []budgetRow
	for _, bl := range bs {
		if bl.Used == nil && bl.Limit == nil {
			continue
		}
		r := budgetRow{label: budgetLabel(bl.Label), pct: pctText(bl.Used, bl.Limit)}
		r.used = i18n.T("progress.val.unknown")
		if bl.Used != nil {
			r.used = formatCount(*bl.Used)
		}
		r.limNum, r.limit = "—", "—"
		if bl.Limit != nil {
			r.limNum = formatCount(*bl.Limit)
			r.limit = r.limNum
			if r.unit = ansi.Truncate(oneLine(unitText(bl), 12), 12, "…"); r.unit != "" {
				r.limit += " " + r.unit
			}
		}
		out = append(out, r)
	}
	return out
}

// budgetLabelCap 是标签列最宽（外部标签更长就用「…」截短）：已知标签最长是「Frontier calls」14 列。
const budgetLabelCap = 14

// layoutBudget 排预算行。四段的起止列在所有行里一致：标签左对齐、已用右对齐、「 / 上限单位」左对齐、百分比右对齐。
// 宽度不够时先省百分比；再不够整行折成两行（标签一行，数字缩进一行）；再不够已用与上限各占一行。数字从不截断。
func layoutBudget(rows []budgetRow, width int) []string {
	if len(rows) == 0 {
		return nil
	}
	labW, usedW, limW, pctW := 0, 0, 0, 0
	for i := range rows {
		if lipgloss.Width(rows[i].label) > budgetLabelCap {
			rows[i].label = ansi.Truncate(rows[i].label, budgetLabelCap, "…")
		}
		labW = max(labW, lipgloss.Width(rows[i].label))
		usedW = max(usedW, lipgloss.Width(rows[i].used))
		limW = max(limW, lipgloss.Width(rows[i].limit))
		pctW = max(pctW, lipgloss.Width(rows[i].pct))
	}
	var out []string
	switch {
	case labW+1+usedW+3+limW+2+pctW <= width: // 一行，带百分比
		for _, r := range rows {
			out = append(out, padRight(r.label, labW)+" "+padLeft(r.used, usedW)+" / "+padRight(r.limit, limW)+"  "+padLeft(r.pct, pctW))
		}
	case labW+1+usedW+3+limW <= width: // 一行，省百分比
		for _, r := range rows {
			out = append(out, strings.TrimRight(padRight(r.label, labW)+" "+padLeft(r.used, usedW)+" / "+r.limit, " "))
		}
	case 2+usedW+3+limW <= width: // 两行：标签一行，数字缩进一行（放得下就带百分比）
		withPct := 2+usedW+3+limW+2+pctW <= width
		for _, r := range rows {
			out = append(out, fitLabel(r.label, width))
			line := "  " + padLeft(r.used, usedW) + " / " + r.limit
			if withPct {
				line = "  " + padLeft(r.used, usedW) + " / " + padRight(r.limit, limW) + "  " + padLeft(r.pct, pctW)
			}
			out = append(out, strings.TrimRight(line, " "))
		}
	default: // 已用、上限、单位各占一行（这时连「/ 上限 单位」都放不下）；缩进按剩余宽度缩，仍放不下就按宽度拆开（n071 attempt 2，R3）
		for _, r := range rows {
			out = append(out, fitLabel(r.label, width))
			out = append(out, hangLines(2, r.used, width)...)
			out = append(out, hangLines(2, "/ "+r.limNum, width)...)
			if r.unit != "" {
				out = append(out, hangLines(4, r.unit, width)...)
			}
		}
	}
	return out
}

// hangLines 把 text 缩进 indent 列写出来，一行绝不超过 width 列：缩进放不下就缩小（直到 0），
// 文字比整行还宽就按显示宽度拆成几行。拆开总比超宽被终端截断好；width 比一个字还窄（<2）时每个字单独一行，宽字符仍会超宽，无法再窄。
func hangLines(indent int, text string, width int) []string {
	if width < 1 {
		return []string{text}
	}
	tw := lipgloss.Width(text)
	for indent > 0 && indent+tw > width {
		indent--
	}
	room := max(1, width-indent)
	var out []string
	var cur strings.Builder
	cw := 0
	for _, r := range text {
		rw := lipgloss.Width(string(r))
		if cw > 0 && cw+rw > room {
			out = append(out, strings.Repeat(" ", indent)+cur.String())
			cur.Reset()
			cw = 0
		}
		cur.WriteRune(r)
		cw += rw
	}
	if cw > 0 || len(out) == 0 {
		out = append(out, strings.Repeat(" ", indent)+cur.String())
	}
	return out
}

// fitLabel 在窗口比标签还窄时把标签截短（数字不截）。
func fitLabel(label string, width int) string {
	if width >= 2 && lipgloss.Width(label) > width {
		return ansi.Truncate(label, width, "…")
	}
	return label
}
