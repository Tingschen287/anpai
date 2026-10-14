package panels

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/dashquota"
	"github.com/Tingschen287/terminal-deck/work/internal/deckd"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

func plain(s string) string { return ansi.Strip(s) }

// n056（owner 2026-10-14）：未知的来源不显示；不写「来自 Token 看板」；有窗口的带重置时间；OpenCode 的「日」窗口。
func TestQuotaPanelHidesUnknownAndShowsReset(t *testing.T) {
	now := time.Date(2026, 10, 14, 15, 0, 0, 0, time.Local)
	quotaNow = func() time.Time { return now }
	defer func() { quotaNow = time.Now }()
	qs := []dashquota.ToolQuota{
		{Tool: "claude", Status: dashquota.Unknown, Reason: "看板没有这个来源的额度窗口"},
		{Tool: "codex", Status: dashquota.Exhausted, Windows: []dashquota.Window{
			{Key: "5h", Pct: 92, Reset: time.Date(2026, 10, 14, 16, 54, 0, 0, time.Local)},
			{Key: "week", Pct: 8, Reset: time.Date(2026, 10, 20, 14, 54, 0, 0, time.Local)}}},
		{Tool: "opencode", Status: dashquota.Available, Windows: []dashquota.Window{{Key: "day", Pct: 50, Reset: time.Date(2026, 10, 15, 0, 0, 0, 0, time.Local)}}},
		{Tool: "kimi", Status: dashquota.Available, Windows: []dashquota.Window{{Key: "5h", Pct: 0}}},
	}
	lines, logos := QuotaLines(qs, 44, "none")
	out := plain(strings.Join(lines, "\n"))
	// 字母徽章在这一块的中间行：codex 三行（名字、5h、周）在 5h 那行，opencode、kimi 两行在名字那行
	for _, want := range []string{"Codex 用尽", "↻ 16:54", "↻ 10/20 14:54", "OpenCode", "日", "50%", "↻ 10/15 00:00", "Kimi Code", "      Codex 用尽", " O   5h", " O   OpenCode", " K   Kimi Code"} {
		if !strings.Contains(out, want) {
			t.Errorf("额度面板缺 %q：\n%s", want, out)
		}
	}
	for _, not := range []string{"Claude", "未知", "来自 Token 看板", "看板没有"} {
		if strings.Contains(out, not) {
			t.Errorf("不该出现 %q：\n%s", not, out)
		}
	}
	if len(logos) != 0 {
		t.Fatal("字母徽章模式不画图")
	}
	// 各窗口的条子起点对齐（全角「周」「日」按两格算），每行不超过面板宽
	var starts []int
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, "━"); i >= 0 {
			starts = append(starts, ansi.StringWidth(line[:i]))
		}
		if ansi.StringWidth(line) > 44 {
			t.Errorf("超宽：%q", line)
		}
	}
	for _, s := range starts {
		if s != starts[0] {
			t.Errorf("额度条起点没对齐：%v\n%s", starts, out)
			break
		}
	}
	// sixel：logo 位置留空，返回位置（每个来源的名字那一行）
	lines, logos = QuotaLines(qs, 44, "sixel")
	if len(logos) != 3 || logos[0].Tool != "codex" || !strings.HasPrefix(plain(lines[logos[0].Row]), "      Codex") {
		t.Fatalf("sixel 的 logo 位置：%+v\n%s", logos, plain(strings.Join(lines, "\n")))
	}
	// 全部未知：一行说明，不显示各家
	lines, _ = QuotaLines(qs[:1], 44, "none")
	if out := plain(strings.Join(lines, "\n")); !strings.Contains(out, "暂时没有可显示的额度") || strings.Contains(out, "Claude") {
		t.Fatal(out)
	}
}

func TestDecisionsPanelListsOnlyOpenAndShowsOptionsOfSelected(t *testing.T) {
	now := time.Now()
	ds := []deckstore.Decision{
		{ID: "a", Project: "deck", Kind: deckstore.KindDeviation, Summary: "按状态分组？", Options: []string{"按项目", "按状态", "reject", "stop"}, State: "open", CreatedAt: now, Recommend: "按项目"},
		{ID: "b", Project: "deck", Kind: deckstore.KindCost, Summary: "多开 frontier", Options: []string{"approve", "reject", "stop"}, State: "open", CreatedAt: now.Add(time.Second)},
		{ID: "c", Project: "deck", Kind: deckstore.KindPush, Summary: "已经答过的", State: "approved", CreatedAt: now},
	}
	out := plain(RenderDecisions(ds, 1, ""))
	for _, want := range []string{"需要你决策（2）", "[偏离你的决策]", "推荐：按项目", "[要花额度]", "1 同意  2 不同意  3 停止"} {
		if !strings.Contains(out, want) {
			t.Errorf("决策面板缺 %q：\n%s", want, out)
		}
	}
	if strings.Contains(out, "已经答过的") {
		t.Error("答过的不该出现")
	}
}

func TestProgressPanelSaysUnknownForMissingBudget(t *testing.T) {
	limit, used := 214.0, 202.0
	out := plain(RenderProgress(deckd.Progress{Project: "deck", Graph: true, Counts: map[string]int{"accepted": 37, "blocked": 3},
		Budgets: []deckd.BudgetView{{Label: "frontier", Used: &used, Limit: &limit}, {Label: "elapsed", Limit: &limit, Unit: "小时"}}}))
	for _, want := range []string{"已验收 37", "阻塞 3", "frontier 202 / 214", "elapsed 未知 / 214小时"} {
		if !strings.Contains(out, want) {
			t.Errorf("进度面板缺 %q：\n%s", want, out)
		}
	}
}

func TestNotificationsPanelKeepsNewestWithinHeight(t *testing.T) {
	var ns []deckstore.Notification
	for i := 1; i <= 10; i++ {
		ns = append(ns, deckstore.Notification{Seq: int64(i), At: time.Now(), Text: "第" + string(rune('0'+i%10)) + "条"})
	}
	out := plain(RenderNotifications(ns, 4))
	if strings.Contains(out, "第1条") || !strings.Contains(out, "第0条") {
		t.Errorf("应只留最新的几条：\n%s", out)
	}
}

// 面板显示的模型文本与错误都清洗过（n050 评审 B7）。
func TestPanelsCleanModelTextAndErrors(t *testing.T) {
	out := RenderActivity([]deckstore.Session{{Project: "p", Role: "sub", Title: "x\x1b]0;evil\x07 sk-abcdefghijklmnop", Agent: "claude", Status: "running"}}, time.Now())
	if strings.Contains(out, "\x07") || strings.Contains(out, "sk-abc") {
		t.Fatalf("标题应清洗：%q", out)
	}
	m := &model{name: "quota", err: errors.New("boom \x1b]0;evil\x07")}
	if v := m.View(); strings.Contains(v, "\x07") || strings.Contains(v, "]0;evil") {
		t.Fatalf("错误文本应清洗：%q", v)
	}
}

// n056 自查：重置时刻已过写「已重置」；窄到放不下重置时间时不写它，百分比不被截断。
func TestQuotaResetPastAndNarrow(t *testing.T) {
	now := time.Date(2026, 10, 14, 15, 25, 0, 0, time.Local)
	quotaNow = func() time.Time { return now }
	defer func() { quotaNow = time.Now }()
	qs := []dashquota.ToolQuota{{Tool: "codex", Status: dashquota.Available, Windows: []dashquota.Window{
		{Key: "5h", Pct: 100, Reset: time.Date(2026, 10, 14, 14, 54, 0, 0, time.Local)},
		{Key: "week", Pct: 8, Reset: time.Date(2026, 10, 20, 14, 54, 0, 0, time.Local)}}}}
	lines, _ := QuotaLines(qs, 44, "none")
	out := plain(strings.Join(lines, "\n"))
	if !strings.Contains(out, "↻ 已重置") || strings.Contains(out, "14:54\n") || !strings.Contains(out, "↻ 10/20 14:54") {
		t.Fatalf("%s", out)
	}
	for _, w := range []int{30, 26, 22} {
		lines, _ := QuotaLines(qs, w, "none")
		out := plain(strings.Join(lines, "\n"))
		if strings.Contains(out, "↻") || !strings.Contains(out, "100%") {
			t.Errorf("宽 %d：不写重置时间、百分比完整：\n%s", w, out)
		}
		for _, l := range strings.Split(out, "\n") {
			if ansi.StringWidth(l) > w {
				t.Errorf("宽 %d 超出：%q", w, l)
			}
		}
	}
}

// n056（owner 2026-10-14 第二轮）：额度条用细横线（上下两根条之间有间隙）；Label 优先于工具名（Antigravity 的各额度池）。
func TestQuotaBarsAndLabels(t *testing.T) {
	qs := []dashquota.ToolQuota{
		{Tool: "antigravity", Label: "Antigravity Gemini", Status: dashquota.Available, Windows: []dashquota.Window{{Key: "5h", Pct: 50}, {Key: "week", Pct: 5}}},
	}
	lines, _ := QuotaLines(qs, 44, "none")
	out := plain(strings.Join(lines, "\n"))
	if !strings.Contains(out, "Antigravity Gemini") || strings.ContainsAny(out, "█░") || !strings.Contains(out, "━") {
		t.Fatalf("%s", out)
	}
	// 池名来自看板：控制序列与换行不进终端（n056 自查）
	qs[0].Label = "Antigravity \x1b]0;PWNED\x07Gem\nini"
	lines, _ = QuotaLines(qs, 44, "none")
	raw := strings.Join(lines, "\n")
	if strings.Contains(raw, "\x1b]0;") || strings.Contains(raw, "\x07") || len(lines) != 4 {
		t.Fatalf("Label 未清洗：%q", raw)
	}
}

// n056 评审 1 B3：一直窄到 14 列，百分比都完整（条先缩、再不画）；没有超出宽度的行。
func TestQuotaPercentWholeWhenNarrow(t *testing.T) {
	qs := []dashquota.ToolQuota{{Tool: "codex", Status: dashquota.Exhausted, Windows: []dashquota.Window{{Key: "5h", Pct: 100}, {Key: "week", Pct: 8}}}}
	for w := 14; w <= 24; w++ {
		lines, _ := QuotaLines(qs, w, "none")
		out := plain(strings.Join(lines, "\n"))
		if !strings.Contains(out, "100%") || !strings.Contains(out, "  8%") {
			t.Errorf("宽 %d：百分比不完整：\n%s", w, out)
		}
		for _, l := range strings.Split(out, "\n")[2:] { // 窗口行
			if ansi.StringWidth(l) > w {
				t.Errorf("宽 %d 超出：%q", w, l)
			}
		}
	}
}

// n056 attempt 2 自查：窄时截名字保住「用尽」；看板给的长窗口键按实际宽度排、不挤掉百分比，并清洗控制字符。
func TestQuotaKeepsStatusAndLongKeys(t *testing.T) {
	qs := []dashquota.ToolQuota{{Tool: "antigravity", Label: "Antigravity Claude", Status: dashquota.Exhausted,
		Windows: []dashquota.Window{{Key: "5h", Pct: 100}, {Key: "monthly\x1b[2J", Pct: 7}}}}
	for _, w := range []int{22, 26, 30, 44} {
		lines, _ := QuotaLines(qs, w, "none")
		raw := strings.Join(lines, "\n")
		out := plain(raw)
		if !strings.Contains(out, "用尽") || !strings.Contains(out, "100%") || !strings.Contains(out, "  7%") || strings.Contains(raw, "\x1b[2J") {
			t.Errorf("宽 %d：\n%s", w, out)
		}
		for _, l := range strings.Split(out, "\n") {
			if ansi.StringWidth(l) > w {
				t.Errorf("宽 %d 超出：%q", w, l)
			}
		}
	}
}
