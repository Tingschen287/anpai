// Package ui 把一次快照画成一屏文字。
//
// 渲染是纯函数：给同一份快照，永远画出同样的字。这样它既能进测试，
// 也能直接输出成证据文本，不必去截终端的图。
package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/Tingschen287/terminal-deck/work/internal/dispatch"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

var (
	styleTitle   = lipgloss.NewStyle().Bold(true)
	styleSection = lipgloss.NewStyle().Bold(true)
	styleDim     = lipgloss.NewStyle().Faint(true)
	styleUnknown = lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
	styleAlert   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	styleOK      = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
)

// stateMark 给每个状态一个记号，灰掉的是已经不用管的。
var stateMark = map[projectctl.State]string{
	projectctl.StateRunning:    "▶",
	projectctl.StateSubmitted:  "⏸",
	projectctl.StateReady:      "○",
	projectctl.StateRework:     "↺",
	projectctl.StateBlocked:    "■",
	projectctl.StateFailed:     "✗",
	projectctl.StatePlanned:    "·",
	projectctl.StateAccepted:   "✓",
	projectctl.StateSuperseded: "~",
}

// Screen 是渲染时要知道的界面状态。零值就是平时那副样子。
type Screen struct {
	// 光标停在第几个节点上，负数表示不显示光标。
	Cursor int
	// 是不是正在挑模型。
	Picking bool
	// 可选的模型与当前挑中的那个。
	Candidates []dispatch.Model
	Pick       int
	// 这次挑模型是为了派评审。
	PickingReviewer bool
	// 上一次动作的结果，成败都显示。
	Notice   string
	NoticeOK bool
	// 会话画面的只读预览，以及它来自哪个会话。
	Preview     string
	PreviewFrom string
	// 展开的完整理由，以及它属于哪个节点。
	Detail   string
	DetailOf string
	// 是否把已验收的节点也列出来。
	ShowAll bool
}

// Render 把快照画成一屏。width 是终端宽度。
func Render(s projectctl.Snapshot, width int) string {
	return RenderScreen(s, width, Screen{Cursor: -1})
}

// RenderScreen 按给定的界面状态画一屏。
func RenderScreen(s projectctl.Snapshot, width int, sc Screen) string {
	return renderWith(s, width, sc)
}

func renderWith(s projectctl.Snapshot, width int, sc Screen) string {
	if width < 60 {
		width = 60
	}
	var b strings.Builder
	w := func(format string, args ...any) {
		fmt.Fprintf(&b, format, args...)
		b.WriteByte('\n')
	}

	// 抬头：项目、版本、账本体检。
	name := s.ProjectID
	if name == "" {
		name = "未知项目"
	}
	w("%s", styleTitle.Render("Work Deck — "+name))
	w("%s", styleDim.Render(headline(s)))
	w("%s", strings.Repeat("─", width))

	// 预算：三个维度加 token，未知的明着说未知。
	w("%s", styleSection.Render("预算"))
	for _, line := range s.Budgets {
		w("  %s", budgetRow(line))
	}
	for _, line := range s.Budgets {
		if !line.Used.IsKnown() {
			w("  %s", styleDim.Render(fmt.Sprintf("└ %s 已用为未知：%s", line.Label, line.Used.Reason())))
		}
	}
	w("")

	// 等 owner：这是唯一需要人立刻动手的一段，放在节点表之前。
	//
	// 一条一行。账本里的理由动辄几百字，整段铺开会把这一段变成一堵墙，
	// 反而看不出谁在等你。完整内容按 Enter 展开。
	waiting := s.WaitingOnOwner()
	var todo, idle []projectctl.Node
	for _, n := range waiting {
		if needsNoAction(n.RequiredAction) {
			idle = append(idle, n)
		} else {
			todo = append(todo, n)
		}
	}
	w("%s", styleSection.Render(fmt.Sprintf("等你处理（%d）", len(todo))))
	if len(todo) == 0 {
		w("  %s", styleDim.Render("没有等你的条目"))
	}
	for _, n := range todo {
		st, _ := n.State.Get()
		hint := actionHint(n.RequiredAction, 46)
		if hint == "" {
			hint = summarize(n.Reason, 46)
		}
		if hint == "" {
			// 账本没写要做什么时，按状态给一句有用的，别甩个空。
			switch st {
			case projectctl.StateSubmitted:
				hint = "等评审"
			case projectctl.StateRework:
				hint = "被打回，等重做"
			default:
				hint = styleDim.Render("账本里没写要做什么")
			}
		}
		w("  %s %-38s %s", stateBadge(st), clip(n.ID, 38), hint)
	}
	if len(idle) > 0 {
		w("  %s", styleDim.Render(fmt.Sprintf("另有 %d 条已归档终结，不用管：%s",
			len(idle), strings.Join(idsOf(idle), "、"))))
	}
	w("  %s", styleDim.Render("Enter 展开选中项的完整理由"))
	w("")

	// 运行中：attempt 与已耗时。
	running := s.Running()
	if len(running) > 0 {
		w("%s", styleSection.Render(fmt.Sprintf("运行中（%d）", len(running))))
		for _, n := range running {
			w("  %-38s attempt %-6s 已跑 %-10s %s",
				clip(n.ID, 38), n.Attempt.String(), durationText(n.Elapsed), n.Actor.String())
		}
		w("")
	}

	// 全部节点。
	//
	// 默认只列还需要留意的：已验收的折叠成一行。43 行平铺等于没有重点，
	// 而这块表盘的用处恰恰是让人一眼看出该看哪儿。按 Tab 全展开。
	counts, unknown := s.Counts()
	var done []projectctl.Node
	keep := map[string]bool{}
	for _, n := range s.Nodes {
		st, known := n.State.Get()
		if known && st == projectctl.StateAccepted && !sc.ShowAll {
			done = append(done, n)
			continue
		}
		keep[n.ID] = true
	}
	w("%s", styleSection.Render(fmt.Sprintf("节点（%d）%s", len(s.Nodes), countsText(counts, unknown))))
	for i, n := range s.Nodes {
		if !keep[n.ID] {
			continue
		}
		marker := "  "
		row := nodeRow(n, width)
		if i == sc.Cursor {
			marker = styleTitle.Render("▸ ")
			row = styleTitle.Render(row)
		}
		w("%s%s", marker, row)
	}
	if len(done) > 0 {
		w("  %s", styleDim.Render(fmt.Sprintf("另有 %d 个已验收，已折叠（Tab 展开）", len(done))))
	}
	w("")

	// 正在挑模型时，把候选摆出来。
	if sc.Picking {
		what := "挑一个模型派工"
		if sc.PickingReviewer {
			what = "挑一个模型派评审"
		}
		w("%s", styleSection.Render(what))
		if len(sc.Candidates) == 0 {
			w("  %s", styleAlert.Render("档位表里没有满足这个节点的模型"))
		}
		for i, c := range sc.Candidates {
			mark := "  "
			line := fmt.Sprintf("%-28s %-10s %s", c.ID, c.Class, c.Tool)
			if i == sc.Pick {
				mark = styleTitle.Render("▸ ")
				line = styleTitle.Render(line)
			}
			w("%s%s", mark, line)
		}
		w("  %s", styleDim.Render("↑↓ 选，回车派工，Esc 取消"))
		w("")
	}

	// 展开的完整理由。
	if sc.Detail != "" {
		w("%s", styleSection.Render("详情　"+styleDim.Render(sc.DetailOf)))
		for _, ln := range wrapText(sc.Detail, width-4) {
			w("  %s", ln)
		}
		w("  %s", styleDim.Render("Enter 收起"))
		w("")
	}

	// 会话画面的只读预览。
	if sc.Preview != "" {
		w("%s", styleSection.Render("会话画面预览　"+styleDim.Render(sc.PreviewFrom)))
		w("  %s", styleDim.Render("只读，不接管，不改会话状态。Esc 关掉。"))
		lines := strings.Split(strings.TrimRight(sc.Preview, "\n"), "\n")
		// 只画得下的部分，末尾几行才是最新的。
		const maxPreview = 12
		if len(lines) > maxPreview {
			w("  %s", styleDim.Render(fmt.Sprintf("（省略前 %d 行）", len(lines)-maxPreview)))
			lines = lines[len(lines)-maxPreview:]
		}
		for _, ln := range lines {
			w("  │ %s", clip(ln, width-4))
		}
		w("")
	}

	// 上一次动作的结果。
	if sc.Notice != "" {
		if sc.NoticeOK {
			w("%s", styleOK.Render(sc.Notice))
		} else {
			w("%s", styleAlert.Render(sc.Notice))
		}
	}

	// 脚注：新鲜度与问题。
	if s.Fresh.Stale {
		w("%s", styleUnknown.Render("投影已过期："+s.Fresh.Note))
	} else if s.Fresh.Note != "" {
		w("%s", styleDim.Render(s.Fresh.Note))
	}
	for _, p := range s.Problems {
		w("%s", styleAlert.Render("问题："+p))
	}
	w("%s", styleDim.Render("↑↓ 选　Enter 详情　Tab 展开已验收　d 派工　a 进入　T 直连　p 预览　r 刷新　q 退出"))
	// 边界声明不能因为要腾地方就不说：它是这块表盘对人的承诺。
	w("%s", styleDim.Render("本界面不写 Graph 账本，验收与状态变更一律由控制器负责。"))
	return b.String()
}

// stateBadge 是状态的短标记，带颜色，宽度统一，好让各行对齐。
// wrapText 按宽度折行，中文按两格算。
func wrapText(s string, width int) []string {
	if width < 20 {
		width = 20
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		line, w := "", 0
		for _, r := range para {
			rw := 1
			if r > 0x2000 {
				rw = 2
			}
			if w+rw > width {
				out = append(out, line)
				line, w = "", 0
			}
			line += string(r)
			w += rw
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func stateBadge(st projectctl.State) string {
	label := fmt.Sprintf("%-9s", st)
	switch st {
	case projectctl.StateBlocked, projectctl.StateFailed:
		return styleAlert.Render(label)
	case projectctl.StateSubmitted, projectctl.StateRework:
		return styleTitle.Render(label)
	}
	return label
}

func idsOf(nodes []projectctl.Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.ID)
	}
	return out
}

func headline(s projectctl.Snapshot) string {
	parts := []string{"graph rev " + s.GraphRevision.String()}
	parts = append(parts, fmt.Sprintf("账本 %d 条事件", s.Chain.Events))
	if s.Chain.ChainOK {
		parts = append(parts, "哈希链完好")
	} else {
		parts = append(parts, "哈希链有问题")
	}
	if s.Chain.ErrorCount > 0 {
		parts = append(parts, fmt.Sprintf("体检 %d 项不通过", s.Chain.ErrorCount))
	}
	parts = append(parts, "健康度 "+s.Health.String())
	return strings.Join(parts, " · ")
}

func budgetRow(l projectctl.BudgetLine) string {
	used := numberText(l.Used)
	limit := numberText(l.Limit)
	if !l.Limit.IsKnown() {
		limit = styleUnknown.Render("未设上限")
	}
	unit := l.Unit
	if unit != "" {
		unit = " " + unit
	}
	return fmt.Sprintf("%-16s %s / %s%s", l.Label, used, limit, unit)
}

func nodeRow(n projectctl.Node, width int) string {
	st, ok := n.State.Get()
	mark := "?"
	label := styleUnknown.Render("未知")
	if ok {
		mark = stateMark[st]
		label = colorState(st)
	}
	row := fmt.Sprintf("%s %-10s %-38s", mark, label, clip(n.ID, 38))
	switch {
	case !ok:
		row += styleDim.Render(clip(n.State.Reason(), 40))
	case st == projectctl.StateRunning:
		row += fmt.Sprintf("attempt %s · 已跑 %s", n.Attempt.String(), durationText(n.Elapsed))
	default:
		row = strings.TrimRight(row, " ")
	}
	return row
}

func colorState(s projectctl.State) string {
	switch s {
	case projectctl.StateBlocked, projectctl.StateFailed:
		return styleAlert.Render(string(s))
	case projectctl.StateAccepted:
		return styleOK.Render(string(s))
	case projectctl.StateRunning, projectctl.StateSubmitted:
		return styleTitle.Render(string(s))
	}
	return string(s)
}

func countsText(counts map[projectctl.State]int, unknown int) string {
	var parts []string
	for _, st := range projectctl.AllStates {
		if c := counts[st]; c > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", st, c))
		}
	}
	if unknown > 0 {
		parts = append(parts, styleUnknown.Render(fmt.Sprintf("状态未知 %d", unknown)))
	}
	if len(parts) == 0 {
		return ""
	}
	return "　" + styleDim.Render(strings.Join(parts, " · "))
}

// durationText 把耗时写成人看的样子；拿不到就是未知，不填 0。
func durationText(v projectctl.Value[time.Duration]) string {
	d, ok := v.Get()
	if !ok {
		return styleUnknown.Render("未知")
	}
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	sec := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%02ds", m, sec)
	}
	return fmt.Sprintf("%ds", sec)
}

// numberText 把预算数字写成人读得下去的样子。
// 大数走科学计数法会让人看不出量级，所以整数原样印，只在超过十万时用 k 收一收。
func numberText(v projectctl.Value[float64]) string {
	f, ok := v.Get()
	if !ok {
		return styleUnknown.Render("未知")
	}
	if f == float64(int64(f)) {
		n := int64(f)
		if n >= 100000 {
			return fmt.Sprintf("%dk", n/1000)
		}
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1f", f)
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}
