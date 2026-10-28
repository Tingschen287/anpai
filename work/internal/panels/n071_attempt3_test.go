package panels

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/Tingschen287/terminal-deck/work/internal/deckd"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// n071 attempt 3（Codex 复审 2）：R1 归一化先于限长与打码；R3 按字素簇折行。
// 下面的「密钥」都是合成夹具，不是真凭据。

// R1：Unicode 空白（NBSP、U+2003、U+202F、全角空格、NEL、行/段分隔符……）夹在 bearer / token= 里，n060 的正则只认 ASCII 空白；
// 归一化必须在打码和限长之前做完，不然限长把 bearer 后面截成不足 8 个字符，打码认不出，凭据前缀就露在屏幕上。
// 空白 × 密钥形状 × 前缀长度（让限长落在密钥的各个位置）× 标签与单位出口 × 中英 × 真实渲染。
func TestN071UnicodeSpaceCannotHideASecretBeforeClipping(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	spaces := []string{" ", " ", " ", "　", "\u0085", " ", " ", " ", " ", "\t", "  "}
	shapes := []string{"bearer%sFAKE12345", "Bearer%sFAKE12345", "token%s=%sFAKE12345", "api_key%s:%sFAKE12345"}
	one := 1.0
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		mark := deckstore.RedactLabel()
		for _, sp := range spaces {
			for _, shape := range shapes {
				raw := strings.ReplaceAll(shape, "%s", sp)
				for pad := 0; pad <= 8; pad++ {
					in := strings.Repeat("p", pad) + raw
					for name, got := range map[string]string{"label": budgetLabel(in), "unit": oneLine(in, 12)} {
						if strings.Contains(got, "FAKE") {
							t.Fatalf("%s %s sp=%q shape=%q pad=%d：凭据前缀露出来了：%q", lang, name, sp, shape, pad, got)
						}
						// 记号完整，或者被限长截到一半（以「…」收尾）；两种都不露凭据。限长落在记号里时只剩残缺的记号前缀
						if !strings.Contains(got, mark) && !strings.HasSuffix(got, "…") {
							t.Fatalf("%s %s sp=%q shape=%q pad=%d：应有打码记号：%q", lang, name, sp, shape, pad, got)
						}
					}
					p := deckd.Progress{Project: "fixture", Graph: true, Budgets: []deckd.BudgetView{{Label: in, Used: &one, Limit: &one, Unit: in}}}
					for _, w := range []int{12, 20, 40, 80} {
						if out := plain(RenderProgressW(p, w)); strings.Contains(out, "FAKE") {
							t.Fatalf("%s w%d sp=%q shape=%q pad=%d：真实渲染里有凭据前缀：\n%s", lang, w, sp, shape, pad, out)
						}
					}
				}
			}
		}
	}
}

// 已呈现的打码记号不被再次截短：单位里的真中性标记，呈现成完整的「[已打码]」/「[redacted]」，不是「[redacte…」。
func TestN071PresentedMarkIsNotClippedAgain(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	one := 1.0
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		mark := deckstore.RedactLabel()
		in := "abc " + deckstore.Clean("sk-FAKE12345", 40)
		if got := oneLine(in, 12); got != "abc "+mark {
			t.Errorf("%s oneLine：%q", lang, got)
		}
		if rows := budgetRows([]deckd.BudgetView{{Label: "tokens", Used: &one, Limit: &one, Unit: in}}); len(rows) != 1 || rows[0].unit != "abc "+mark {
			t.Errorf("%s 预算行的单位被再次截短：%+v", lang, rows)
		}
		p := deckd.Progress{Project: "p", Graph: true, Budgets: []deckd.BudgetView{{Label: "tokens", Used: &one, Limit: &one, Unit: in}}}
		for _, w := range []int{80} {
			if out := plain(RenderProgressW(p, w)); !strings.Contains(out, "abc "+mark) || strings.Contains(out, "…") {
				t.Errorf("%s w%d：渲染里的记号不完整：\n%s", lang, w, out)
			}
		}
		// 真标记仍是真标记；伪造的字面「[已打码]」仍原样（本节点不管它，归 n068），控制序列仍被清掉
		if got := oneLine("\x1b[31m"+deckstore.Clean("sk-FAKE12345", 40)+"\x1b[0m", 40); got != mark {
			t.Errorf("%s 真标记加控制序列：%q", lang, got)
		}
	}
}

// R3：按字素簇折行。变体选择符 emoji、ZWJ 序列、国旗、组合附加符——各 rune 宽度之和不等于整段显示宽度。
// 单位 × 宽度 2～40 × 中英：每一行（含缩进）不超宽，且字符与顺序完整。
func TestN071GraphemeUnitsNeverOverflow(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	units := map[string]string{
		"vs16":     strings.Repeat("❤️", 6),
		"zwj":      strings.Repeat("\U0001F468‍\U0001F469‍\U0001F467", 3),
		"combine":  strings.Repeat("é", 6),
		"flags":    strings.Repeat("\U0001F1E8\U0001F1F3", 3),
		"mixed":    "ab❤️中文é!",
		"keycap":   "1️⃣" + "2️⃣" + "3️⃣",
		"skintone": strings.Repeat("\U0001F44D\U0001F3FD", 4),
	}
	one := 1.0
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		for name, unit := range units {
			want := oneLine(unit, 12)
			for w := 2; w <= 40; w++ {
				lines := layoutBudget(budgetRows([]deckd.BudgetView{{Label: "tokens", Used: &one, Limit: &one, Unit: unit}}), w)
				for _, l := range lines {
					if lipgloss.Width(l) > w && !singleClusterLine(l) {
						t.Fatalf("%s %s w%d：超宽 %d 列：%q\n%s", lang, name, w, lipgloss.Width(l), l, strings.Join(lines, "\n"))
					}
				}
				if flat := squashAll(strings.Join(lines, "")); !strings.Contains(flat, squashAll(want)) {
					t.Fatalf("%s %s w%d：单位的字符或顺序不完整（要 %q）：\n%s", lang, name, w, want, strings.Join(lines, "\n"))
				}
			}
			// 真实渲染（Codex 的 10 列探针）
			p := deckd.Progress{Project: "p", Graph: true, Budgets: []deckd.BudgetView{{Label: "tokens", Used: &one, Limit: &one, Unit: unit}}}
			for _, w := range []int{6, 10, 12} {
				for _, l := range strings.Split(plain(RenderProgressW(p, w)), "\n")[2:] {
					if lipgloss.Width(l) > w && !singleClusterLine(l) {
						t.Errorf("%s %s 真实渲染 w%d 超宽 %d：%q", lang, name, w, lipgloss.Width(l), l)
					}
				}
			}
		}
	}
}

// 一行只有一个字素簇、而这个簇本身比窗口还宽：无法再窄，不算超宽。
func singleClusterLine(l string) bool {
	return len(hangLines(0, strings.TrimSpace(l), 1<<30)) == 1 && clusterCount(strings.TrimSpace(l)) == 1
}

func clusterCount(s string) int {
	n := 0
	for rest := s; rest != ""; n++ {
		_, tail := firstCluster(rest)
		rest = tail
	}
	return n
}

// hangLines 直接测：任意文字、任意宽度，每一行不超宽（单簇宽于窗口除外），去掉缩进拼回等于原文。
func TestN071HangLinesGraphemes(t *testing.T) {
	texts := []string{"", "x", "abc def", strings.Repeat("❤️", 6), "\U0001F468‍\U0001F469‍\U0001F467\U0001F468‍\U0001F469‍\U0001F467", strings.Repeat("é", 9), "中文abc❤️"}
	for _, text := range texts {
		for _, indent := range []int{0, 2, 4} {
			for w := 2; w <= 40; w++ {
				lines := hangLines(indent, text, w)
				var back strings.Builder
				for _, l := range lines {
					body := strings.TrimLeft(l, " ")
					if lipgloss.Width(l) > w && clusterCount(body) > 1 {
						t.Fatalf("%q indent%d w%d：超宽 %d：%q", text, indent, w, lipgloss.Width(l), l)
					}
					back.WriteString(body)
				}
				if squashAll(back.String()) != squashAll(text) { // 换行处的空格不保留
					t.Fatalf("%q indent%d w%d：拼回不等于原文：%q", text, indent, w, back.String())
				}
			}
		}
	}
}

// 截短时打码记号是不可分的整体：输入里有几个记号，输出里就有几个完整的记号、没有被截残的；普通文字照常截到宽度。
func TestN071ClipKeepsMarksWhole(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		mark := deckstore.RedactLabel()
		clean := deckstore.Clean("sk-FAKE12345", 40)
		for _, in := range []string{"abc " + clean, "abc " + clean + " tail tail tail", clean + clean, "ab" + clean + "cd", clean + " " + clean + " " + clean} {
			got := oneLine(in, 12)
			want := strings.Count(in, clean)
			if n := strings.Count(got, mark); n < 1 || n > want {
				t.Errorf("%s oneLine(%q) = %q：完整记号 %d 个，输入有 %d 个", lang, in, got, n, want)
			}
			if strings.Count(got, "[") != strings.Count(got, mark) {
				t.Errorf("%s oneLine(%q) = %q：有记号被截残", lang, in, got)
			}
		}
		if got := oneLine("abc "+clean, 12); got != "abc "+mark {
			t.Errorf("%s：%q", lang, got)
		}
		if got := oneLine(clean+clean, 12); got != mark+mark {
			t.Errorf("%s：两个记号都应完整：%q", lang, got)
		}
		if got := oneLine("0123456789abcdef", 12); got != "0123456789a…" {
			t.Errorf("%s：普通文字截到 12 列：%q", lang, got)
		}
		if got := oneLine("short", 12); got != "short" {
			t.Errorf("%s：%q", lang, got)
		}
	}
}
