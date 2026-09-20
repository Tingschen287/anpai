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

// Render 把快照画成一屏。width 是终端宽度。
func Render(s projectctl.Snapshot, width int) string {
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
	waiting := s.WaitingOnOwner()
	w("%s", styleSection.Render(fmt.Sprintf("等 owner 处理（%d）", len(waiting))))
	if len(waiting) == 0 {
		w("  %s", styleDim.Render("没有等你的条目"))
	}
	for _, n := range waiting {
		st, _ := n.State.Get()
		w("  %-10s %s", colorState(st), n.ID)
		if n.Reason != "" {
			w("             %s", styleDim.Render("因为："+clip(n.Reason, width-15)))
		}
		if n.RequiredAction != "" {
			w("             %s", styleAlert.Render("要你做："+clip(n.RequiredAction, width-15)))
		}
	}
	w("")

	// 运行中：attempt 与已耗时。
	running := s.Running()
	w("%s", styleSection.Render(fmt.Sprintf("运行中（%d）", len(running))))
	if len(running) == 0 {
		w("  %s", styleDim.Render("没有节点在跑"))
	}
	for _, n := range running {
		w("  %-34s attempt %-8s 已跑 %-10s %s",
			clip(n.ID, 34), n.Attempt.String(), durationText(n.Elapsed), n.Actor.String())
	}
	w("")

	// 全部节点。
	counts, unknown := s.Counts()
	w("%s", styleSection.Render(fmt.Sprintf("全部节点（%d）%s", len(s.Nodes), countsText(counts, unknown))))
	for _, n := range s.Nodes {
		w("  %s", nodeRow(n, width))
	}
	w("")

	// 脚注：新鲜度与问题。
	if s.Fresh.Stale {
		w("%s", styleUnknown.Render("投影已过期："+s.Fresh.Note))
	} else if s.Fresh.Note != "" {
		w("%s", styleDim.Render(s.Fresh.Note))
	}
	for _, p := range s.Problems {
		w("%s", styleAlert.Render("问题："+p))
	}
	w("%s", styleDim.Render("只读界面，不派工、不接管、不写账本。r 刷新，q 退出。"))
	return b.String()
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
