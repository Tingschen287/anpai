// Package panels 是「安排 Anpai」的内置面板（n050，架构稿 §7）：需要你决策、额度、项目进度、通知、agent 在干嘛。
//
// 每个面板是布局 server 某个窗格里的一个独立程序，只经 deckd 读数据、做动作。这里的 Render* 是纯函数，
// 便于测试；Run 把它们包成 bubbletea 程序，每 2 秒向 deckd 取一次。取不到就把原因显示出来，不显示旧数据冒充新数据。
package panels

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/dashquota"
	"github.com/Tingschen287/terminal-deck/work/internal/deckd"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/logo"
)

var (
	title  = lipgloss.NewStyle().Bold(true)
	dim    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	warn   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	bad    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	good   = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	picked = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("221"))
)

var toolName = map[string]string{"claude": "Claude Code", "codex": "Codex", "grok": "Grok Build", "opencode": "OpenCode", "kimi": "Kimi Code", "antigravity": "Antigravity"}

// thinBar 画额度条：细横线，已用部分用状态色、未用部分暗灰。上下两根条之间留出间隙（n056，owner：「额度条不粘在一起，要有间隙」）。
func thinBar(pct, n int, used lipgloss.Style) string {
	pct = max(0, min(100, pct))
	full := pct * n / 100
	return used.Render(strings.Repeat("━", full)) + barRest.Render(strings.Repeat("━", n-full))
}

var barRest = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))

// bar 画一条长度为 n 的进度条。
func bar(pct, n int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	full := pct * n / 100
	return strings.Repeat("█", full) + strings.Repeat("░", n-full)
}

// QuotaLogo 是额度面板里一个来源的 logo 位置：这一块从第 Row 行起共 Rows 行、从第 1 列起 4 格宽；
// logo 本身两行高，画在这一块的垂直中间（n056，owner：「logo和旁边的额度区域没有水平居中」）。
type QuotaLogo struct {
	Row  int
	Rows int
	Tool string
}

// QuotaLogoCols、QuotaLogoRows 是 logo 占的格子（与侧栏一致）。
const (
	QuotaLogoCols = 4
	QuotaLogoRows = 2
)

// quotaNow 是额度面板判断「今天」用的时钟；测试换掉。
var quotaNow = time.Now

// resetText 是重置时间：今天之内只写时刻，否则写月/日加时刻；没有就空。
func resetText(r, now time.Time) string {
	if r.IsZero() {
		return ""
	}
	l, n := r.Local(), now.Local()
	if !l.After(n) {
		return i18n.T("quota.reset") // 看板的窗口数据停在上一个周期（例如 Codex 一阵没用）：时刻已过，旁边的百分比是旧的（n056 自查）
	}
	if l.Year() == n.Year() && l.YearDay() == n.YearDay() {
		return "↻ " + l.Format("15:04")
	}
	return "↻ " + l.Format("1/2 15:04")
}

// quotaBadge 是没有 sixel 时 logo 位置上的字（rows 行，各 4 格宽）：品牌色字母徽章（none，字母在中间那一行）
// 或半格色块（blocks，两行，居中）。sixel 时全留空，图另画。
func quotaBadge(tool, mode string, rows int) []string {
	out := make([]string, rows)
	for i := range out {
		out[i] = "    "
	}
	if mode == "sixel" && logo.For(tool) != nil {
		return out
	}
	if mode == "blocks" {
		if im := logo.For(tool); im != nil {
			b := logo.Blocks(im, QuotaLogoCols, QuotaLogoRows)
			top := max(0, (rows-QuotaLogoRows)/2)
			for i := 0; i < QuotaLogoRows && top+i < rows; i++ {
				out[top+i] = b[i]
			}
			return out
		}
	}
	if br, ok := logo.Brand[tool]; ok {
		out[(rows-1)/2] = fmt.Sprintf(" \x1b[1;38;2;%d;%d;%dm%s\x1b[0m  ", br.Color.R, br.Color.G, br.Color.B, br.Letter)
	}
	return out
}

// windowLabel 是窗口在面板上的名字：认识的写中文，其余（看板给的键）清洗后原样。
func windowLabel(key string) string {
	switch key {
	case "5h":
		return "5h"
	case "week":
		return i18n.T("quota.week")
	case "day":
		return i18n.T("quota.day")
	}
	return strings.Join(strings.Fields(deckstore.Clean(key, 12)), " ")
}

// QuotaLines 画额度面板（n056 按 owner 2026-10-14 的要求）：每个来源前有它的 logo；每个窗口带重置时间；
// 没有窗口或取不到来源（未知）的不显示；不再写「来自 Token 看板」。mode 是 logo 画法（sixel / blocks / none）；
// sixel 时返回各 logo 的位置，图由调用方另画。
func QuotaLines(qs []dashquota.ToolQuota, width int, mode string) ([]string, []QuotaLogo) {
	now := quotaNow()
	lines := []string{title.Render(i18n.T("quota.title"))}
	var logos []QuotaLogo
	var shown []dashquota.ToolQuota
	resetW := 0
	for _, q := range qs {
		if q.Status == dashquota.Unknown || len(q.Windows) == 0 {
			continue
		}
		shown = append(shown, q)
		for _, w := range q.Windows {
			resetW = max(resetW, lipgloss.Width(resetText(w.Reset, now)))
		}
	}
	if len(shown) == 0 {
		return append(lines, dim.Render(i18n.T("quota.empty"))), nil
	}
	// 一行：前缀 6 格（空格 + logo 4 格 + 空格）、标签 labelW 格 + 空格、条、空格 + 百分比 4 格、两格 + 重置时间
	labelW := 3
	for _, q := range shown {
		for _, w := range q.Windows {
			labelW = max(labelW, lipgloss.Width(windowLabel(w.Key)))
		}
	}
	// 封顶：最多 8 格，且给百分比留够地方（前缀 6 + 空格 1 + 百分比 4 + 标签 ≤ 宽度，即标签 ≤ 宽度 − 12）；
	// 更长的标签自己截短，不挤掉别的行的百分比（n056 评审 2 R2）
	labelW = min(labelW, 8, max(3, width-12))
	gap := 0
	if resetW > 0 {
		gap = 2
	}
	n := width - 6 - (labelW + 1) - 5 - gap - resetW
	if n < 6 && resetW > 0 {
		// 太窄放不下重置时间：不写它，免得被硬截成错的数（n056 自查）
		resetW = 0
		n = width - 6 - (labelW + 1) - 5
	}
	if n < 3 {
		// 再窄就不画条：先保住完整的百分比（n056 评审 1 B3：原先条至少 6 格，19 列时 100% 被截成「10」）
		n = 0
	}
	for _, q := range shown {
		// Label 来自看板 JSON（Antigravity 的池名）：显示前清洗、压成一行（同 n050 评审 B7；n056 自查）
		name := strings.Join(strings.Fields(deckstore.Clean(q.Label, 40)), " ")
		if name == "" {
			name = toolName[q.Tool]
		}
		if name == "" {
			name = q.Tool
		}
		style, mark := good, ""
		switch q.Status {
		case dashquota.Exhausted:
			style, mark = bad, i18n.T("quota.full")
		case dashquota.Warn:
			style, mark = warn, i18n.T("quota.low")
		}
		// 太窄时截名字、保住「用尽」「告急」（n056 attempt 2 自查）
		if room := width - 6 - lipgloss.Width(mark); room > 0 && lipgloss.Width(name) > room {
			name = ansi.Truncate(name, room, "…")
		}
		head := title.Render(name)
		if mark != "" {
			head = style.Render(name + mark)
		}
		rows := 1 + len(q.Windows)
		badge := quotaBadge(q.Tool, mode, rows)
		if mode == "sixel" && logo.For(q.Tool) != nil {
			logos = append(logos, QuotaLogo{Row: len(lines), Rows: rows, Tool: q.Tool})
		}
		lines = append(lines, " "+badge[0]+" "+head)
		for i, w := range q.Windows {
			label := windowLabel(w.Key)
			if lipgloss.Width(label) > labelW {
				label = ansi.Truncate(label, labelW, "…")
			}
			// 按显示宽度补齐：「周」「日」是全角，占两格，%-3s 按字节数补会错位
			pad := max(0, labelW-lipgloss.Width(label))
			prefix := " " + badge[1+i] + " "
			line := fmt.Sprintf("%s%s%s %s %3d%%", prefix, label, strings.Repeat(" ", pad), thinBar(w.Pct, n, style), w.Pct)
			if n == 0 {
				line = fmt.Sprintf("%s%s%s %s", prefix, label, strings.Repeat(" ", pad), style.Render(fmt.Sprintf("%3d%%", w.Pct)))
			}
			if r := resetText(w.Reset, now); r != "" && resetW > 0 {
				line += "  " + dim.Render(r)
			}
			lines = append(lines, line)
		}
	}
	return lines, logos
}

// RenderQuota 画额度面板（字母徽章或色块画法，不画图；sixel 由 deckui.RunQuota 画）。
func RenderQuota(qs []dashquota.ToolQuota, width int) string {
	mode := logo.Mode()
	if mode == "sixel" {
		mode = "none"
	}
	lines, _ := QuotaLines(qs, width, mode)
	return strings.Join(lines, "\n") + "\n"
}

// OpenDecisions 只留还没答的决定，按创建时间。
func OpenDecisions(ds []deckstore.Decision) []deckstore.Decision {
	var out []deckstore.Decision
	for _, d := range ds {
		if d.State == deckstore.StateOpen {
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

var kindKey = map[string]i18n.Key{
	deckstore.KindCost: "decide.kind.cost", deckstore.KindConfig: "decide.kind.config", deckstore.KindPush: "decide.kind.push",
	deckstore.KindDisagree: "decide.kind.disagree", deckstore.KindDeviation: "decide.kind.deviation",
	deckstore.KindCodexTrust: "decide.kind.codex", deckstore.KindGraphGate: "decide.kind.gate", deckstore.KindSessionWait: "decide.kind.wait",
}

var optionKey = map[string]i18n.Key{deckstore.OptApprove: "decide.opt.approve", deckstore.OptReject: "decide.opt.reject", deckstore.OptStop: "decide.opt.stop"}

// activityStatusKey 把活动面板的会话状态对到文字键。
// 启动中、在跑和侧栏共用 side.status.*。其余用 act.status.*。
// 不认识的状态不在表里，查出来是空串，画面留空。
var activityStatusKey = map[string]i18n.Key{
	deckstore.StatusLaunching: "side.status.launching", deckstore.StatusRunning: "side.status.running",
	deckstore.StatusUncertain: "act.status.uncertain", deckstore.StatusDone: "act.status.done",
	deckstore.StatusFailed: "act.status.failed", deckstore.StatusStopped: "act.status.stopped",
}

// progressCountKey 把进度计数的状态对到文字键。
var progressCountKey = map[string]i18n.Key{
	"accepted": "progress.accepted", "running": "progress.running", "submitted": "progress.submitted",
	"ready": "progress.ready", "rework": "progress.rework", "blocked": "progress.blocked",
	"failed": "progress.failed", "planned": "progress.planned", "superseded": "progress.superseded",
}

// TextKeys 返回本包里经 map 传给 i18n.T 的键。调用处不是字符串字面量，扫描器看不见。
func TextKeys() []string {
	out := make([]string, 0, len(kindKey)+len(optionKey)+len(activityStatusKey)+len(progressCountKey))
	for _, m := range []map[string]i18n.Key{kindKey, optionKey, activityStatusKey, progressCountKey} {
		for _, k := range m {
			out = append(out, string(k))
		}
	}
	sort.Strings(out)
	return out
}

// RenderDecisions 画「需要你决策」面板。sel 是选中的那一条。
func RenderDecisions(ds []deckstore.Decision, sel int, note string) string {
	open := OpenDecisions(ds)
	var b strings.Builder
	b.WriteString(title.Render(i18n.T("decide.title", len(open))) + "\n")
	b.WriteString(dim.Render(i18n.T("decide.about")) + "\n")
	if len(open) == 0 {
		b.WriteString(dim.Render(i18n.T("decide.empty")) + "\n")
	}
	for i, d := range open {
		mark := "  "
		if i == sel {
			mark = picked.Render("▸ ")
		}
		k := kindLabel(d.Kind)
		if k == "" {
			k = d.Kind
		}
		b.WriteString(mark + warn.Render("["+k+"]") + " " + dim.Render(d.Project) + "\n")
		b.WriteString("  " + deckstore.Clean(d.Summary, 800) + "\n")
		if d.Recommend != "" {
			b.WriteString(dim.Render(i18n.T("decide.recommend")+label(d.Recommend)) + "\n")
		}
		if i == sel {
			var opts []string
			for j, o := range d.Options {
				opts = append(opts, fmt.Sprintf("%d %s", j+1, label(o)))
			}
			b.WriteString("  " + picked.Render(strings.Join(opts, "  ")) + "\n")
		}
	}
	b.WriteString(dim.Render(i18n.T("decide.keys")) + "\n")
	if note != "" {
		b.WriteString(note + "\n")
	}
	return b.String()
}

func kindLabel(k string) string {
	// 写成 kindKey[k]。先放进局部变量再传入，检查器会拒绝。
	if _, ok := kindKey[k]; ok {
		return i18n.T(i18n.Key(kindKey[k]))
	}
	return k
}

func label(opt string) string {
	if _, ok := optionKey[opt]; ok {
		return i18n.T(i18n.Key(optionKey[opt]))
	}
	return opt
}

// RenderProgress 画项目进度面板。
func RenderProgress(p deckd.Progress) string {
	var b strings.Builder
	b.WriteString(title.Render(i18n.T("progress.title")+p.Project) + "\n")
	if !p.Graph {
		b.WriteString(dim.Render(i18n.T("progress.nograph")) + "\n")
		return b.String()
	}
	order := []string{"accepted", "running", "submitted", "ready", "rework", "blocked", "failed", "planned", "superseded"}
	var parts []string
	for _, k := range order {
		if n := p.Counts[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", i18n.T(i18n.Key(progressCountKey[k])), n))
		}
	}
	if p.Unknown > 0 {
		parts = append(parts, i18n.T("progress.unknown", p.Unknown))
	}
	b.WriteString(strings.Join(parts, " · ") + "\n")
	for _, bl := range p.Budgets {
		switch {
		case bl.Used == nil && bl.Limit != nil:
			b.WriteString(i18n.T("progress.budget.unknown", bl.Label, *bl.Limit, bl.Unit))
		case bl.Used != nil && bl.Limit != nil:
			b.WriteString(fmt.Sprintf("%s %g / %g%s\n", bl.Label, *bl.Used, *bl.Limit, bl.Unit))
		}
	}
	for _, pr := range p.Problems {
		b.WriteString(warn.Render("! "+pr) + "\n")
	}
	return b.String()
}

// RenderNotifications 画下栏通知，最新的在下面，最多 height 行。
func RenderNotifications(ns []deckstore.Notification, height int) string {
	var b strings.Builder
	b.WriteString(title.Render(i18n.T("notify.title")) + dim.Render(i18n.T("notify.about")) + "\n")
	if len(ns) == 0 {
		b.WriteString(dim.Render(i18n.T("notify.empty")) + "\n")
	}
	if height > 1 && len(ns) > height-1 {
		ns = ns[len(ns)-(height-1):]
	}
	for _, n := range ns {
		line := dim.Render(n.At.Format("15:04")) + " "
		if n.Project != "" {
			line += dim.Render(n.Project + " · ")
		}
		text := n.Text
		if n.Kind == "decision" {
			text = warn.Render(text)
		}
		b.WriteString(line + text + "\n")
	}
	return b.String()
}

// RenderActivity 画「agent 在干嘛」：每个项目的会话与状态。
func RenderActivity(ss []deckstore.Session, now time.Time) string {
	var b strings.Builder
	b.WriteString(title.Render(i18n.T("act.title")) + "\n")
	if len(ss) == 0 {
		b.WriteString(dim.Render(i18n.T("act.empty")) + "\n")
	}
	for _, x := range ss {
		role := i18n.T("act.role.sub")
		if x.Role == "main" {
			role = i18n.T("act.role.main")
		}
		st := i18n.T(i18n.Key(activityStatusKey[x.Status]))
		if x.Status == deckstore.StatusUncertain || x.Status == deckstore.StatusFailed {
			st = warn.Render(st)
		}
		name := toolName[x.Agent]
		if name == "" {
			name = x.Agent
		}
		b.WriteString(fmt.Sprintf("%s %s %s · %s · %s\n", dim.Render(x.Project), role, deckstore.Clean(x.Title, 80), name, st))
	}
	return b.String()
}
