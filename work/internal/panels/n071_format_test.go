package panels

import (
	"math"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"

	"github.com/Tingschen287/terminal-deck/work/internal/deckd"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// n071：数字格式化、百分比、对齐、窄栏、外部文字。

func TestN071FormatCount(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	nan, inf := math.NaN(), math.Inf(1)
	cases := []struct {
		v      float64
		zh, en string
	}{
		{0, "0", "0"},
		{1, "1", "1"},
		{25, "25", "25"},
		{999, "999", "999"},
		{1000, "1000", "1K"},
		{1234, "1234", "1.2K"},
		{9999, "9999", "10K"},
		{10000, "1 万", "10K"},
		{12345, "1.2 万", "12.3K"},
		{99999, "10 万", "100K"},
		{123456, "12.3 万", "123K"},
		{999999, "100 万", "1M"},
		{1000000, "100 万", "1M"},
		{9862368, "986 万", "9.9M"}, // owner 截图里的「9.862368e+06」
		{15000000, "1500 万", "15M"},
		{99999999, "1 亿", "100M"},
		{100000000, "1 亿", "100M"},
		{120000000, "1.2 亿", "120M"},
		{123456789, "1.2 亿", "123M"},
		{1234567890, "12.3 亿", "1.2B"},
		{1e12, "1 万亿", "1T"},
		{3.5e15, "3500 万亿", "3500T"},
		// 非整数：至多一位小数，去掉多余的 .0
		{0.04, "0", "0"},
		{0.5, "0.5", "0.5"},
		{12.34, "12.3", "12.3"},
		{12.96, "13", "13"},
		{999.96, "1000", "1K"},
		{9999.96, "1 万", "10K"},
		{400.0, "400", "400"},
		// 不可信的数：按「未知」，不 panic
		{-1, "未知", "unknown"},
		{-0.0001, "未知", "unknown"},
		{nan, "未知", "unknown"},
		{inf, "未知", "unknown"},
		{math.Inf(-1), "未知", "unknown"},
		// 极大值：不写科学计数，也不铺几百位
		{1e16, ">9999 万亿", ">9999T"},
		{math.MaxFloat64, ">9999 万亿", ">9999T"},
		{math.MaxInt64, ">9999 万亿", ">9999T"},
		{9999.5e12, ">9999 万亿", ">9999T"}, // 最大一档四舍五入凑成五位数：同样写「>9999」
	}
	for _, c := range cases {
		i18n.SetLang(i18n.ZH)
		if got := formatCount(c.v); got != c.zh {
			t.Errorf("zh formatCount(%v) = %q，应为 %q", c.v, got, c.zh)
		}
		i18n.SetLang(i18n.EN)
		if got := formatCount(c.v); got != c.en {
			t.Errorf("en formatCount(%v) = %q，应为 %q", c.v, got, c.en)
		}
	}
	// 任何输入都不出现科学计数
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		for _, v := range []float64{1e-9, 1e-300, 5e-324, 123456789012345678, 1e100, 4.5e9, 7e8, 0.000123} {
			if s := formatCount(v); strings.ContainsAny(s, "eE") && lang == i18n.ZH || strings.Contains(s, "e+") || strings.Contains(s, "e-") {
				t.Errorf("%s formatCount(%v) = %q 含科学计数", lang, v, s)
			}
		}
	}
}

func TestN071PctText(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	cases := []struct {
		used, limit *float64
		want        string
	}{
		{f(25), f(220), "11%"},
		{f(26), f(80), "33%"},
		{f(9862368), f(15000000), "66%"},
		{f(0), f(10), "0%"},
		{f(10), f(10), "100%"},
		{f(112), f(100), "112%"},
		{f(1e9), f(1), ">999%"},
		{f(99.5), f(100), "99%"}, // 还没到上限，不虚报 100%
		{f(99.99), f(100), "99%"},
		{f(100), f(100), "100%"},
		{f(0.5), f(100), "1%"},
		{f(9.996), f(1), ">999%"}, // 取整后才比较：同一量级不出现「1000%」与「>999%」两种写法
		{nil, f(10), "—"},
		{f(10), nil, "—"},
		{nil, nil, "—"},
		{f(5), f(0), "—"}, // 0 上限：不除零
		{f(0), f(0), "—"},
		{f(5), f(-3), "—"},
		{f(-5), f(10), "—"},
		{f(math.NaN()), f(10), "—"},
		{f(5), f(math.NaN()), "—"},
		{f(5), f(math.Inf(1)), "—"},
		{f(math.Inf(1)), f(10), "—"},
		{f(math.MaxFloat64), f(1e-300), ">999%"},
	}
	for i, c := range cases {
		if got := pctText(c.used, c.limit); got != c.want {
			t.Errorf("#%d pctText = %q，应为 %q", i, got, c.want)
		}
	}
}

func fullProgress() deckd.Progress {
	p := n071ProgressWithUnknown()
	return p
}

// 每一行预算的 4 段起止列一致：「 / 」在同一显示列，行尾（百分比右缘）在同一显示列；对中英两种语言、多种宽度成立。
func TestN071BudgetColumnsAlign(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		for _, w := range []int{34, 36, 40, 48, 60, 80, 120, 200} {
			out := plain(RenderProgressW(fullProgress(), w))
			var rows []string
			for _, l := range strings.Split(out, "\n") {
				if strings.Contains(l, " / ") {
					rows = append(rows, l)
				}
			}
			if len(rows) != 4 {
				t.Fatalf("%s w%d：预算应是 4 行，实际 %d：\n%s", lang, w, len(rows), out)
			}
			slash, end := -1, -1
			allPct := true // 只有每一行都带百分比时，百分比右缘才要求对齐（省了百分比的窄档，行尾是上限与单位）
			for _, l := range rows {
				f := strings.Fields(l)
				if last := f[len(f)-1]; !strings.HasSuffix(last, "%") && last != "—" {
					allPct = false
				}
			}
			for _, l := range rows {
				c := lipgloss.Width(l[:strings.Index(l, " / ")])
				if slash >= 0 && c != slash {
					t.Errorf("%s w%d：「 / 」没对齐（%d 对 %d）：\n%s", lang, w, c, slash, out)
				}
				slash = c
				if e := lipgloss.Width(l); allPct && end >= 0 && e != end {
					t.Errorf("%s w%d：行尾没对齐（%d 对 %d）：\n%s", lang, w, e, end, out)
				} else {
					end = e
				}
				if lipgloss.Width(l) > w {
					t.Errorf("%s w%d：一行 %d 列，超宽：%q", lang, w, lipgloss.Width(l), l)
				}
			}
		}
	}
}

// 数字格：两列的左边界各自对齐；数字右对齐到格内固定位置（同一列里数字的右缘相同）。
func TestN071CountGridAligns(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		for _, w := range []int{30, 36, 40, 60, 100} {
			lines := strings.Split(plain(RenderProgressW(fullProgress(), w)), "\n")
			// lines[0] 标题、[1] 分段条、其后是网格直到空行
			var grid []string
			for _, l := range lines[2:] {
				if l == "" {
					break
				}
				grid = append(grid, l)
			}
			if len(grid) < 4 {
				t.Fatalf("%s w%d：网格太短：%q", lang, w, grid)
			}
			twoCol := strings.Count(strings.TrimSpace(grid[0]), "  ") > 0 && lipgloss.Width(grid[0]) > lipgloss.Width(grid[0][:0])+0 && len(strings.Fields(grid[0])) == 4
			// 找每行里各数字（最后一个字段、倒数第三个字段）的右缘
			rightEdge := func(l string, nth int) int {
				idx := 0
				for i, r := range l {
					if r != ' ' && (i+utf8.RuneLen(r) == len(l) || l[i+utf8.RuneLen(r)] == ' ') {
						idx++
						if idx == nth {
							return lipgloss.Width(l[:i+utf8.RuneLen(r)])
						}
					}
				}
				return -1
			}
			if twoCol {
				var right1, right2 = -1, -1
				for i, l := range grid {
					if len(strings.Fields(l)) != 4 {
						continue
					}
					a, b := rightEdge(l, 2), rightEdge(l, 4)
					if i == 0 {
						right1, right2 = a, b
					}
					if a != right1 || b != right2 {
						t.Errorf("%s w%d：第 %d 行数字没右对齐（%d,%d 对 %d,%d）：\n%s", lang, w, i, a, b, right1, right2, strings.Join(grid, "\n"))
					}
					if lipgloss.Width(l) > w {
						t.Errorf("%s w%d：网格行超宽：%q", lang, w, l)
					}
				}
			} else {
				e0 := lipgloss.Width(grid[0])
				for _, l := range grid {
					if lipgloss.Width(l) != e0 {
						t.Errorf("%s w%d：单列叠放时数字没右对齐：\n%s", lang, w, strings.Join(grid, "\n"))
						break
					}
				}
			}
		}
	}
}

// 窄栏：宽度不够就先省百分比，再折行，数字（已用、上限）一个字不丢，也没有超宽的行。
func TestN071NarrowKeepsEveryNumber(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	want := map[string][]string{
		i18n.ZH: {"25", "220", "26", "80", "未知", "400", "986 万", "1500 万"},
		i18n.EN: {"25", "220", "26", "80", "unknown", "400", "9.9M", "15M"},
	}
	for lang, tokens := range want {
		i18n.SetLang(lang)
		for w := 12; w <= 90; w++ {
			out := plain(RenderProgressW(n071Progress(), w))
			for _, tok := range tokens {
				if !strings.Contains(out, tok) {
					t.Fatalf("%s w%d：数字 %q 不见了：\n%s", lang, w, tok, out)
				}
			}
			for _, l := range strings.Split(out, "\n")[2:] { // 标题与分段条的宽度不归本测试管
				if lipgloss.Width(l) > w {
					t.Fatalf("%s w%d：超宽的行 %q（%d 列）：\n%s", lang, w, l, lipgloss.Width(l), out)
				}
			}
		}
	}
}

// 极窄、极宽、各种怪数据：不 panic，也没有 e+、NaN、Inf、负号、%!（格式化错）。
func TestN071NoPanicAndNoJunk(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	f := func(v float64) *float64 { return &v }
	odd := []*float64{nil, f(0), f(-5), f(math.NaN()), f(math.Inf(1)), f(math.Inf(-1)), f(1e300), f(1.5), f(123456789), f(math.MaxFloat64)}
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		for _, u := range odd {
			for _, l := range odd {
				p := deckd.Progress{Project: "p", Graph: true, Counts: map[string]int{"accepted": 3},
					Budgets: []deckd.BudgetView{{Label: "tokens", Used: u, Limit: l}, {Label: "elapsed", Used: u, Limit: l, Unit: "", UnitKey: "hour"}, {Label: "x", Used: u, Limit: l, Unit: "?"}}}
				for _, w := range []int{math.MinInt, -1, 0, 1, 5, 12, 20, 36, 40, 80, 500, 5000} {
					out := plain(RenderProgressW(p, w))
					for _, bad := range []string{"e+", "e-", "NaN", "Inf", "%!", "-1", "E+"} {
						if strings.Contains(out, bad) {
							t.Fatalf("%s u=%v l=%v w=%d：出现 %q：\n%s", lang, u, l, w, bad, out)
						}
					}
				}
			}
		}
	}
}

// 预算缺失的各种组合：已用缺→「未知」，上限缺→「—」，两者都缺→这一行不显示（同从前），0 上限→百分比「—」，单位为空→不多空格。
func TestN071BudgetMissingValues(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	f := func(v float64) *float64 { return &v }
	p := deckd.Progress{Project: "p", Graph: true, Counts: map[string]int{"accepted": 1}, Budgets: []deckd.BudgetView{
		{Label: "tokens", Used: f(5), Limit: nil},                           // 上限缺
		{Label: "worker rounds", Used: nil, Limit: f(10), UnitKey: "round"}, // 已用缺
		{Label: "frontier calls", Used: nil, Limit: nil, UnitKey: "call"},   // 都缺：不显示
		{Label: "elapsed", Used: f(3), Limit: f(0), UnitKey: "hour"},        // 0 上限
	}}
	out := squash(plain(RenderProgressW(p, 60)))
	for _, want := range []string{"token 5 / —", "执行轮次 未知 / 10 轮 —", "耗时 3 / 0 小时 —"} {
		if !strings.Contains(out, want) {
			t.Errorf("缺 %q：\n%s", want, out)
		}
	}
	if strings.Contains(out, "frontier") {
		t.Errorf("已用与上限都缺的维度不该显示：\n%s", out)
	}
}

// 外部文字：认不出的预算标签与单位原样显示，但经清洗出口——控制序列、换行、超长、伪装的打码标记都不能原样带进面板。
func TestN071ExternalLabelsAreSanitized(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	f := func(v float64) *float64 { return &v }
	evil := "weird\x1b]0;pwned\x07\x1b[31m red\r\nsecond\tline " + strings.Repeat("长", 200)
	p := deckd.Progress{Project: "p", Graph: true, Counts: map[string]int{"accepted": 1}, Budgets: []deckd.BudgetView{
		{Label: evil, Used: f(1), Limit: f(2), Unit: "\x1b[2J\x1b[Hu\nnit" + strings.Repeat("x", 80)},
		{Label: "sk-abcdefghijklmnopqrstuvwxyz0123456789", Used: f(1), Limit: f(2), Unit: "\x9b31m"},
	}}
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		for _, w := range []int{20, 40, 120} {
			raw := RenderProgressW(p, w)
			for i := 0; i < len(raw); i++ { // 按字节查：lipgloss 自己的样式序列只会是 ESC [，其余控制字节（含 C1 的 0x9b）一个都不该有
				c := raw[i]
				if (c < 0x20 && c != '\n' && c != 0x1b) || c == 0x7f || (c == 0x1b && (i+1 >= len(raw) || raw[i+1] != '[')) {
					t.Fatalf("%s w%d：外部文字里的控制字符漏进面板（第 %d 字节 %#x）：%q", lang, w, i, c, raw)
				}
			}
			if strings.Contains(raw, "\u009b") || strings.Contains(raw, "\u0090") || strings.Contains(raw, "\u009d") {
				t.Fatalf("%s w%d：C1 控制字符漏进面板：%q", lang, w, raw)
			}
			out := plain(raw)
			if strings.Contains(out, "pwned") && strings.Contains(raw, "\x1b]") {
				t.Fatalf("OSC 没清掉：%q", raw)
			}
			if strings.Contains(out, "sk-abcdefghijklmnopqrstuvwxyz") {
				t.Fatalf("疑似密钥没打码：\n%s", out)
			}
			for _, l := range strings.Split(out, "\n")[2:] {
				if lipgloss.Width(l) > max(w, 16) {
					t.Errorf("%s w%d：外部文字撑出超宽行 %d 列：%q", lang, w, lipgloss.Width(l), l)
				}
			}
		}
	}
}

// 英文界面：认得的标签、单位、缩写、「未知」都是英文，没有汉字。
func TestN071EnglishHasNoHan(t *testing.T) {
	i18n.SetLang(i18n.EN)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	for _, p := range []deckd.Progress{n071Progress(), n071ProgressWithUnknown()} {
		for _, w := range []int{20, 40, 120} {
			if out := plain(RenderProgressW(p, w)); hasHan(out) {
				t.Fatalf("w%d 英文面板有汉字：\n%s", w, out)
			}
		}
	}
}

// 中文界面不夹英文的预算名（worker rounds 等）；认不出的才原样。
func TestN071ChineseBudgetLabels(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	out := plain(RenderProgressW(n071Progress(), 40))
	for _, bad := range []string{"worker rounds", "frontier calls", "elapsed", "tokens"} {
		if strings.Contains(out, bad) {
			t.Errorf("中文界面不该出现 %q：\n%s", bad, out)
		}
	}
	for _, want := range []string{"执行轮次", "frontier 调用", "耗时", "token"} {
		if !strings.Contains(out, want) {
			t.Errorf("缺 %q：\n%s", want, out)
		}
	}
}

// 对抗自查后加的：外部标签里的格式控制符（RLO 等）、空标签、超宽单位、过长的项目名。
func TestN071SelfCheckFixes(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	f := func(v float64) *float64 { return &v }
	p := deckd.Progress{Project: "p", Graph: true, Counts: map[string]int{"accepted": 1}, Budgets: []deckd.BudgetView{
		{Label: "rtl\u202eabc\u200b\ufeff", Used: f(1), Limit: f(2), Unit: "\u2067"},
		{Label: "", Used: f(1), Limit: f(2)},
		{Label: "   ", Used: f(1), Limit: f(2), Unit: strings.Repeat("非", 30)},
	}}
	for _, w := range []int{20, 40, 80} {
		raw := plain(RenderProgressW(p, w))
		for _, r := range raw {
			if unicode.Is(unicode.Cf, r) {
				t.Fatalf("w%d：格式控制符 %U 漏进面板：%q", w, r, raw)
			}
		}
		if !strings.Contains(raw, "rtlabc") {
			t.Fatalf("w%d：清掉格式控制符后标签应是 rtlabc：\n%s", w, raw)
		}
		for _, l := range strings.Split(raw, "\n")[2:] {
			if strings.TrimSpace(l) == "" {
				continue
			}
			if lipgloss.Width(l) > max(w, 29) {
				t.Errorf("w%d：超宽的行 %q", w, l)
			}
		}
		if !strings.Contains(raw, "—") { // 空标签写成破折号，不留空行
			t.Errorf("w%d：空标签应写成「—」：\n%s", w, raw)
		}
	}
	// 项目名很长：标题行截成一行，不超宽
	long := deckd.Progress{Project: strings.Repeat("项目", 40), Graph: true, Counts: map[string]int{"accepted": 1}}
	for _, w := range []int{8, 20, 36, 40} {
		first := strings.Split(plain(RenderProgressW(long, w)), "\n")[0]
		if lipgloss.Width(first) > w {
			t.Errorf("w%d：标题 %d 列，超宽", w, lipgloss.Width(first))
		}
	}
}
