package panels

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckd"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// n071：进度面板的黄金输出。版式以 owner 确认的草图为准（4.eval/results/n071_deck_ui_tidy/sketch.md）。
// 黄金文件在 testdata/n071/，是去掉颜色的纯文本，评审直接打开就能看版式。
// 重新生成：N071_UPDATE=1 go test ./internal/panels -run N071Golden；
// 同时导出一份到别处（评审证据目录）：N071_RENDER_OUT=<目录>。

// n071Progress 是草图里的那组数据（it-req-automation：25/220 轮、26/80 次、耗时未知、986.2368 万 / 1500 万 token）。
func n071Progress() deckd.Progress {
	used := func(v float64) *float64 { return &v }
	return deckd.Progress{
		Project: "it-req-automation", Graph: true,
		Counts:  map[string]int{"blocked": 1},
		Unknown: 8,
		Budgets: []deckd.BudgetView{
			{Label: "worker rounds", Used: used(25), Limit: used(220), Unit: "轮", UnitKey: "round"},
			{Label: "frontier calls", Used: used(26), Limit: used(80), Unit: "次", UnitKey: "call"},
			{Label: "elapsed", Limit: used(400), Unit: "小时", UnitKey: "hour"},
			{Label: "tokens", Used: used(9862368), Limit: used(15000000)},
		},
	}
}

// n071ProgressWithUnknown 带「状态未知」与几项明细（草图第二块）。
func n071ProgressWithUnknown() deckd.Progress {
	p := n071Progress()
	p.Counts = map[string]int{"accepted": 37, "blocked": 3, "failed": 1, "planned": 7, "running": 1}
	p.Unknown = 8
	return p
}

func goldenCheck(t *testing.T, name, got string) {
	t.Helper()
	got = strings.TrimRight(got, "\n") + "\n"
	if dir := os.Getenv("N071_RENDER_OUT"); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join("testdata", "n071", name)
	if os.Getenv("N071_UPDATE") != "" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("没有黄金文件 %s（N071_UPDATE=1 生成）：%v", path, err)
	}
	if string(want) != got {
		t.Fatalf("%s 与黄金输出不同。\n--- 黄金\n%s--- 实际\n%s", name, want, got)
	}
}

func TestN071GoldenProgress(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	cases := []struct {
		name string
		p    deckd.Progress
	}{{"sketch", n071Progress()}, {"detail", n071ProgressWithUnknown()}}
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		for _, c := range cases {
			for _, w := range []int{40, 36, 24} {
				goldenCheck(t, fmt.Sprintf("progress-%s-%s-w%d.txt", c.name, lang, w), plain(RenderProgressW(c.p, w)))
			}
		}
	}
}
