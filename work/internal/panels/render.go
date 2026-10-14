// Package panels 是 Agent Deck 的内置面板（n050，架构稿 §7）：需要你决策、额度、项目进度、通知、agent 在干嘛。
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

	"github.com/Tingschen287/terminal-deck/work/internal/dashquota"
	"github.com/Tingschen287/terminal-deck/work/internal/deckd"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
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

var toolName = map[string]string{"claude": "Claude Code", "codex": "Codex", "grok": "Grok Build", "opencode": "OpenCode", "kimi": "Kimi Code"}

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

// QuotaLogo 是额度面板里一个来源的 logo 位置：从第 Row 行起两行高、从第 1 列起 4 格宽（n056）。
type QuotaLogo struct {
	Row  int
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
		return "↻ 已重置" // 看板的窗口数据停在上一个周期（例如 Codex 一阵没用）：时刻已过，旁边的百分比是旧的（n056 自查）
	}
	if l.Year() == n.Year() && l.YearDay() == n.YearDay() {
		return "↻ " + l.Format("15:04")
	}
	return "↻ " + l.Format("1/2 15:04")
}

// quotaBadge 是没有 sixel 时 logo 位置上的字：品牌色字母徽章（none）或半格色块（blocks）。两行，各 4 格宽。
func quotaBadge(tool, mode string) [2]string {
	if mode == "blocks" {
		if im := logo.For(tool); im != nil {
			b := logo.Blocks(im, QuotaLogoCols, QuotaLogoRows)
			return [2]string{b[0], b[1]}
		}
	}
	if mode == "sixel" && logo.For(tool) != nil {
		return [2]string{"    ", "    "} // 留出位置，图另画
	}
	if br, ok := logo.Brand[tool]; ok {
		return [2]string{fmt.Sprintf(" \x1b[1;38;2;%d;%d;%dm%s\x1b[0m  ", br.Color.R, br.Color.G, br.Color.B, br.Letter), "    "}
	}
	return [2]string{"    ", "    "}
}

// QuotaLines 画额度面板（n056 按 owner 2026-10-14 的要求）：每个来源前有它的 logo；每个窗口带重置时间；
// 没有窗口或取不到来源（未知）的不显示；不再写「来自 Token 看板」。mode 是 logo 画法（sixel / blocks / none）；
// sixel 时返回各 logo 的位置，图由调用方另画。
func QuotaLines(qs []dashquota.ToolQuota, width int, mode string) ([]string, []QuotaLogo) {
	now := quotaNow()
	lines := []string{title.Render("额度")}
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
		return append(lines, dim.Render("暂时没有可显示的额度")), nil
	}
	// 一行：前缀 6 格（空格 + logo 4 格 + 空格）、标签 3 格 + 空格、条、空格 + 百分比 4 格、两格 + 重置时间
	n := width - 6 - 4 - 5 - 2 - resetW
	if n < 6 && resetW > 0 {
		// 太窄放不下重置时间：不写它，免得被硬截成错的数（n056 自查）
		resetW = 0
		n = width - 6 - 4 - 5
	}
	if n < 6 {
		n = 6
	}
	for _, q := range shown {
		name := toolName[q.Tool]
		if name == "" {
			name = q.Tool
		}
		head := title.Render(name)
		style := good
		switch q.Status {
		case dashquota.Exhausted:
			head, style = bad.Render(name+" 用尽"), bad
		case dashquota.Warn:
			head, style = warn.Render(name+" 告急"), warn
		}
		badge := quotaBadge(q.Tool, mode)
		if mode == "sixel" && logo.For(q.Tool) != nil {
			logos = append(logos, QuotaLogo{Row: len(lines), Tool: q.Tool})
		}
		lines = append(lines, " "+badge[0]+" "+head)
		for i, w := range q.Windows {
			label := map[string]string{"5h": "5h", "week": "周", "day": "日"}[w.Key]
			if label == "" {
				label = w.Key
			}
			// 按显示宽度补齐：「周」「日」是全角，占两格，%-3s 按字节数补会错位
			pad := max(0, 3-lipgloss.Width(label))
			prefix := "      "
			if i == 0 {
				prefix = " " + badge[1] + " "
			}
			line := fmt.Sprintf("%s%s%s %s %3d%%", prefix, label, strings.Repeat(" ", pad), style.Render(bar(w.Pct, n)), w.Pct)
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

var kindName = map[string]string{
	deckstore.KindCost: "要花额度", deckstore.KindConfig: "要改电脑设置", deckstore.KindPush: "要推送代码",
	deckstore.KindDisagree: "协作者意见不一", deckstore.KindDeviation: "偏离你的决策",
	deckstore.KindCodexTrust: "Codex 会改写你的配置", deckstore.KindGraphGate: "项目计划的门", deckstore.KindSessionWait: "会话在等你",
}

var optionName = map[string]string{deckstore.OptApprove: "同意", deckstore.OptReject: "不同意", deckstore.OptStop: "停止"}

// RenderDecisions 画「需要你决策」面板。sel 是选中的那一条。
func RenderDecisions(ds []deckstore.Decision, sel int, note string) string {
	open := OpenDecisions(ds)
	var b strings.Builder
	b.WriteString(title.Render(fmt.Sprintf("需要你决策（%d）", len(open))) + "\n")
	b.WriteString(dim.Render("只有主 agent 过滤后的事项才会出现在这里") + "\n")
	if len(open) == 0 {
		b.WriteString(dim.Render("现在没有要你决定的事") + "\n")
	}
	for i, d := range open {
		mark := "  "
		if i == sel {
			mark = picked.Render("▸ ")
		}
		k := kindName[d.Kind]
		if k == "" {
			k = d.Kind
		}
		b.WriteString(mark + warn.Render("["+k+"]") + " " + dim.Render(d.Project) + "\n")
		b.WriteString("  " + deckstore.Clean(d.Summary, 800) + "\n")
		if d.Recommend != "" {
			b.WriteString(dim.Render("  推荐："+label(d.Recommend)) + "\n")
		}
		if i == sel {
			var opts []string
			for j, o := range d.Options {
				opts = append(opts, fmt.Sprintf("%d %s", j+1, label(o)))
			}
			b.WriteString("  " + picked.Render(strings.Join(opts, "  ")) + "\n")
		}
	}
	b.WriteString(dim.Render("↑↓ 选  数字键作答") + "\n")
	if note != "" {
		b.WriteString(note + "\n")
	}
	return b.String()
}

func label(opt string) string {
	if n, ok := optionName[opt]; ok {
		return n
	}
	return opt
}

// RenderProgress 画项目进度面板。
func RenderProgress(p deckd.Progress) string {
	var b strings.Builder
	b.WriteString(title.Render("项目进度 · "+p.Project) + "\n")
	if !p.Graph {
		b.WriteString(dim.Render("这个项目没有接 Graph") + "\n")
		return b.String()
	}
	order := []string{"accepted", "running", "submitted", "ready", "rework", "blocked", "failed", "planned", "superseded"}
	zh := map[string]string{"accepted": "已验收", "running": "进行中", "submitted": "待评审", "ready": "可开工", "rework": "返工",
		"blocked": "阻塞", "failed": "失败", "planned": "计划中", "superseded": "已取代"}
	var parts []string
	for _, k := range order {
		if n := p.Counts[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", zh[k], n))
		}
	}
	if p.Unknown > 0 {
		parts = append(parts, fmt.Sprintf("状态未知 %d", p.Unknown))
	}
	b.WriteString(strings.Join(parts, " · ") + "\n")
	for _, bl := range p.Budgets {
		switch {
		case bl.Used == nil && bl.Limit != nil:
			b.WriteString(fmt.Sprintf("%s 未知 / %g%s\n", bl.Label, *bl.Limit, bl.Unit))
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
	b.WriteString(title.Render("通知") + dim.Render("  主 agent 开了子 agent、需要你决定时都会出现在这里，不打断你") + "\n")
	if len(ns) == 0 {
		b.WriteString(dim.Render("还没有通知") + "\n")
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
	b.WriteString(title.Render("agent 在干嘛") + "\n")
	if len(ss) == 0 {
		b.WriteString(dim.Render("还没有会话") + "\n")
	}
	zh := map[string]string{deckstore.StatusLaunching: "启动中", deckstore.StatusRunning: "在跑", deckstore.StatusUncertain: "结果不明",
		deckstore.StatusDone: "完成", deckstore.StatusFailed: "失败", deckstore.StatusStopped: "已停"}
	for _, x := range ss {
		role := "协作"
		if x.Role == "main" {
			role = "★主"
		}
		st := zh[x.Status]
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
