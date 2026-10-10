package panels

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/dashquota"
	"github.com/Tingschen287/terminal-deck/work/internal/deckd"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

func plain(s string) string { return ansi.Strip(s) }

func TestQuotaPanelShowsReasonInsteadOfNumbersWhenUnknown(t *testing.T) {
	out := plain(RenderQuota([]dashquota.ToolQuota{
		{Tool: "claude", Status: dashquota.Available, Windows: []dashquota.Window{{Key: "5h", Pct: 9}, {Key: "week", Pct: 2}}},
		{Tool: "codex", Status: dashquota.Exhausted, Windows: []dashquota.Window{{Key: "week", Pct: 92}}},
		{Tool: "opencode", Status: dashquota.Unknown, Reason: "看板没有这个来源的额度窗口"},
	}, 40))
	for _, want := range []string{"Claude Code", "5h", "9%", "周", "Codex 用尽", "92%", "OpenCode 未知", "看板没有这个来源的额度窗口"} {
		if !strings.Contains(out, want) {
			t.Errorf("额度面板缺 %q：\n%s", want, out)
		}
	}
	if strings.Contains(strings.Split(out, "OpenCode")[1], "%") {
		t.Errorf("未知的来源不该出现百分比：\n%s", out)
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
