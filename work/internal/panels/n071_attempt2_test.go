package panels

import (
	"math"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/Tingschen287/terminal-deck/work/internal/deckd"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// n071 attempt 2（Codex 审查 1）：R1 清洗顺序、R3 最窄兜底行。
// 下面的「密钥」都是合成夹具，不是真凭据。

// R1：格式字符夹在 sk- 中间，不能躲过打码：先删格式字符、再走打码出口，删完拼成的疑似密钥照样被打码。
// 标签与单位两个出口、中英两种语言、几种格式字符、插在几个位置的组合；裸密钥与真中性标记的原行为不变。
func TestN071FormatCharsCannotHideASecret(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	fmtChars := []string{"\u200b", "\u202e", "\ufeff", "\u2060", "\u200d", "\u2067", "\u061c"}
	var raws []string
	for _, c := range fmtChars {
		raws = append(raws, "s"+c+"k-FAKE12345", "sk"+c+"-FAKE12345", "s"+c+"k"+c+"-"+c+"FAKE12345", "ok "+"s"+c+"k-FAKE12345"+" tail")
	}
	one := 1.0
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		mark := deckstore.RedactLabel()
		for _, raw := range raws {
			// 标签出口、单位出口各自：结果里没有 sk-FAKE，且再走一遍出口不会再变（不动点）
			for name, got := range map[string]string{"label": budgetLabel(raw), "unit": oneLine(raw, 12)} {
				if strings.Contains(got, "sk-FAKE") || strings.Contains(got, "FAKE12345") {
					t.Errorf("%s %s %q：夹字符的密钥漏出来了：%q", lang, name, raw, got)
				}
				if again := deckstore.Display(got, 40); again != got && name == "label" {
					t.Errorf("%s %s %q：再走一遍出口还会变（%q → %q）", lang, name, raw, got, again)
				}
			}
			if !strings.Contains(budgetLabel(raw), mark) {
				t.Errorf("%s %q：标签里应有打码记号 %q：%q", lang, raw, mark, budgetLabel(raw))
			}
			// 真实渲染：标签与单位都带夹字符密钥，各种宽度
			p := deckd.Progress{Project: "fixture", Graph: true, Budgets: []deckd.BudgetView{{Label: raw, Used: &one, Limit: &one, Unit: raw}}}
			for _, w := range []int{12, 20, 40, 80} {
				if out := plain(RenderProgressW(p, w)); strings.Contains(out, "FAKE12345") {
					t.Fatalf("%s w%d %q：渲染结果里有未打码的密钥：\n%s", lang, w, raw, out)
				}
			}
		}
		// 保留：裸密钥照旧打码；真中性标记照旧显示成打码记号
		if got := budgetLabel("sk-FAKE12345"); got != mark {
			t.Errorf("%s 裸密钥：%q", lang, got)
		}
		if got := budgetLabel(deckstore.Clean("sk-FAKE12345", 40)); got != mark {
			t.Errorf("%s 真标记：%q", lang, got)
		}
		if got := oneLine(deckstore.Clean("sk-FAKE12345", 40), 12); got != mark {
			t.Errorf("%s 单位出口的真标记：%q", lang, got)
		}
	}
}

// R3：最窄兜底行任何宽度都不超宽。极值数字（MaxFloat64）、未知（NaN/缺失）、最长单位（12 列的英文与汉字）
// × 宽度 2～40 × 中英；排版拆开之后数字与单位一个字符都不丢、顺序不变。
func TestN071NarrowestBudgetFallbackNeverOverflows(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	f := func(v float64) *float64 { return &v }
	numbers := []struct {
		name        string
		used, limit *float64
	}{
		{"max", f(math.MaxFloat64), f(math.MaxFloat64)},
		{"unknown-used", nil, f(math.MaxFloat64)},
		{"nan", f(math.NaN()), f(math.NaN())},
		{"small", f(1), f(2)},
		{"no-limit", f(math.MaxFloat64), nil},
	}
	units := []string{"", "abcdefghijkl", strings.Repeat("非", 6), "mixed中文unit"}
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		for _, n := range numbers {
			for _, u := range units {
				bs := []deckd.BudgetView{{Label: "tokens", Used: n.used, Limit: n.limit, Unit: u}, {Label: "elapsed", Used: n.used, Limit: n.limit, UnitKey: "hour"}}
				for w := 2; w <= 40; w++ {
					rows := budgetRows(bs)
					lines := layoutBudget(rows, w)
					for _, l := range lines {
						if lipgloss.Width(l) > w {
							t.Fatalf("%s %s unit=%q w%d：超宽 %d 列：%q\n%s", lang, n.name, u, w, lipgloss.Width(l), l, strings.Join(lines, "\n"))
						}
					}
					// 一个字符都不丢：把所有行去掉空白连起来，应包含每一行预算的「已用」「/ 上限」「单位」按序出现
					flat := squashAll(strings.Join(lines, ""))
					for _, r := range budgetRows(bs) {
						for _, piece := range []string{r.used, r.limNum, r.unit} {
							if piece != "" && !strings.Contains(flat, squashAll(piece)) {
								t.Fatalf("%s %s unit=%q w%d：%q 不见了：\n%s", lang, n.name, u, w, piece, strings.Join(lines, "\n"))
							}
						}
					}
				}
			}
		}
		// Codex 的原始探针：12 列、MaxFloat64 与 12 列长单位，经真实渲染
		v := math.MaxFloat64
		one := 1.0
		for _, p := range []deckd.Progress{
			{Project: "p", Graph: true, Budgets: []deckd.BudgetView{{Label: "tokens", Used: &v, Limit: &v}}},
			{Project: "p", Graph: true, Budgets: []deckd.BudgetView{{Label: "tokens", Used: &one, Limit: &one, Unit: "abcdefghijkl"}}},
		} {
			for _, l := range strings.Split(plain(RenderProgressW(p, 12)), "\n")[2:] {
				if lipgloss.Width(l) > 12 {
					t.Errorf("%s 真实渲染 12 列超宽 %d：%q", lang, lipgloss.Width(l), l)
				}
			}
		}
	}
}

func squashAll(s string) string { return strings.Join(strings.Fields(s), "") }
