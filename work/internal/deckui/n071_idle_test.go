package deckui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/layout"
)

// n071：空闲页的版式（owner 确认的草图 A「整块居中，块内左对齐」）。

func keyRowCount() int { return len(layout.Keys()) }

// 表格整块居中、块内两列各自左对齐：所有表格行起始列相同；按键列起点一致；说明列的左边界一致；续行也从说明列左边界开始。
func TestN071IdleTableAlignsLeftAsOneBlock(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		for _, w := range []int{60, 80, 100, 120, 200} {
			rows := IdleRows(w)
			start := -1
			for i, r := range rows {
				if strings.HasPrefix(r.Text, "F2 ") { // 提示句比窗口宽时会折成几行，表格从 F2 那一行开始
					start = i
					break
				}
			}
			if start < 2 {
				t.Fatalf("%s w%d：找不到表格的第一行：%+v", lang, w, rows)
			}
			table := rows[start:]
			if len(table) < keyRowCount() {
				t.Fatalf("%s w%d：表格行太少：%d", lang, w, len(table))
			}
			col := table[0].Col
			maxEnd := 0
			for _, r := range table {
				if r.Col != col {
					t.Fatalf("%s w%d：表格行起始列不同（%d 对 %d）：%q", lang, w, r.Col, col, r.Text)
				}
				maxEnd = max(maxEnd, r.Col-1+lipgloss.Width(r.Text))
				if r.Col-1+lipgloss.Width(r.Text) > w {
					t.Errorf("%s w%d：超出窗口：%q", lang, w, r.Text)
				}
			}
			// 整块居中：按整块最宽一行算左边距（与品牌名、提示句同一个居中公式）
			if want := max(1, (w-(maxEnd-(col-1)))/2+1); col != want {
				t.Errorf("%s w%d：整块起始列 %d，应为 %d（按最宽一行 %d 列居中）", lang, w, col, want, maxEnd-(col-1))
			}
			// 说明列左边界：按键列宽取最长按键（Ctrl-Q，6 列）加两格
			keys := layout.Keys()
			keyW := 0
			for _, k := range keys {
				keyW = max(keyW, lipgloss.Width(k.Key))
			}
			ki := 0
			for _, r := range table {
				body := r.Text
				if ki < len(keys) && strings.HasPrefix(body, keys[ki].Key+" ") && strings.TrimSpace(body[:len(keys[ki].Key)]) == keys[ki].Key {
					rest := strings.TrimLeft(body[len(keys[ki].Key):], " ")
					if off := lipgloss.Width(body) - lipgloss.Width(rest); off != keyW+2 {
						t.Errorf("%s w%d：说明列左边界在 %d，应为 %d：%q", lang, w, off, keyW+2, body)
					}
					ki++
					continue
				}
				// 续行：以 keyW+2 个空格开头
				if !strings.HasPrefix(body, strings.Repeat(" ", keyW+2)) || strings.HasPrefix(body, strings.Repeat(" ", keyW+3)) {
					t.Errorf("%s w%d：续行应从说明列左边界开始：%q", lang, w, body)
				}
			}
			if ki != len(keys) {
				t.Errorf("%s w%d：只认出 %d/%d 个按键行", lang, w, ki, len(keys))
			}
			// 说明文字沿用现有文案、不改词：折行后拼回去等于原文
			var got []string
			for _, r := range table {
				got = append(got, strings.TrimSpace(strings.TrimPrefix(strings.TrimLeft(r.Text, " "), firstField(r.Text))))
			}
			joined := strings.ReplaceAll(strings.Join(got, ""), " ", "")
			for _, k := range keys {
				if !strings.Contains(joined, strings.ReplaceAll(k.What, " ", "")) {
					t.Errorf("%s w%d：说明文字被改了或丢了：%q", lang, w, k.What)
				}
			}
		}
	}
}

func firstField(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	if strings.HasPrefix(s, " ") { // 续行没有按键
		return ""
	}
	return f[0]
}

// logo、品牌名、提示句仍各自水平居中（逐行居中，与改版前同一公式）。
func TestN071IdleHeadStaysCentered(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		for _, w := range []int{60, 80, 120} {
			rows := IdleRows(w)
			for _, i := range []int{0, 2} {
				want := max(1, (w-lipgloss.Width(rows[i].Text))/2+1)
				if rows[i].Col != want {
					t.Errorf("%s w%d 第 %d 行起始列 %d，应为 %d", lang, w, i, rows[i].Col, want)
				}
			}
		}
	}
}

// 说明放不下就折行，不丢、不截、不超窗口；英文 F2 的说明最长，在 80 列会折；提示句比窗口宽也折（各段各自居中）。
func TestN071IdleWrapsLongTexts(t *testing.T) {
	i18n.SetLang(i18n.EN)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	rows := IdleRows(80)
	if len(rows) <= len(IdleLines(80)) {
		t.Fatalf("英文 80 列下 F2 的说明与提示句应折行，行数 %d", len(rows))
	}
	for _, r := range rows {
		if r.Col-1+lipgloss.Width(r.Text) > 80 {
			t.Errorf("超出 80 列：%q", r.Text)
		}
	}
}

// 窄窗（放不下按键列 + 最短说明，窗口 <26 列）：退回逐行居中；每一行（太长就折成几段）各自居中，一行都不超窗口，字一个不少。
// 窗口 <10 列时不留边，必要时英文词与按键也按显示宽度拆开（n071 attempt 2，Codex 审查 R4：从 6 到 9 列也不能整行超宽）。
func TestN071IdleNarrowNeverOverflows(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		for w := 2; w <= 60; w++ {
			rows := IdleRows(w)
			for i, r := range rows {
				if end := r.Col - 1 + lipgloss.Width(r.Text); end > w || r.Col < 1 {
					t.Errorf("%s w%d 行 %d：超出窗口（起始列 %d，结束列 %d）：%q", lang, w, i, r.Col, end, r.Text)
				}
			}
			var got []string
			for _, r := range rows {
				got = append(got, r.Text)
			}
			if a, b := squashAll(strings.Join(got, "")), squashAll(strings.Join(IdleLines(w), "")); w < 26 && a != b {
				t.Errorf("%s w%d：折行丢了字：\n%q\n%q", lang, w, a, b)
			}
			if w >= 10 && w < 26 {
				for i, r := range rows {
					if r.Col != max(1, (w-lipgloss.Width(r.Text))/2+1) {
						t.Errorf("%s w%d 行 %d：逐行写法没居中：%+v", lang, w, i, r)
					}
				}
			}
		}
		// 没有窗口宽度（0、负数）：原样逐行，不 panic
		for _, w := range []int{-5, 0} {
			if rows := IdleRows(w); len(rows) != len(IdleLines(w)) {
				t.Errorf("%s w%d：宽度未知不折行，应与 IdleLines 同样 %d 行，实际 %d", lang, w, len(IdleLines(w)), len(rows))
			}
		}
	}
}

func squashAll(s string) string { return strings.Join(strings.Fields(s), "") }

// 逻辑行里 8 个按键一个不少（太窄时按键本身也可能被拆成两段）。这只说明「没有被折行逻辑吃掉」，不说明屏幕上看得见——
// 高度不够时底部的行会被 IdlePlace 裁掉，屏幕上可见的按键见 cmd/deck 的 TestN071IdleScreen*。
func TestN071IdleLogicalRowsKeepEveryKey(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		for w := 0; w <= 160; w += 3 {
			text := ""
			for _, r := range IdleRows(w) {
				text += r.Text
			}
			text = squashAll(text)
			for _, k := range layout.Keys() {
				if !strings.Contains(text, squashAll(k.Key)) {
					t.Fatalf("%s w%d：按键 %s 丢了", lang, w, k.Key)
				}
			}
		}
	}
}

// 摆到屏幕上：每一行画在 top+i 行、自己的起始列；矮窗里画到窗外的行丢掉；只有第一行用品牌色。
func TestN071IdlePlace(t *testing.T) {
	rows := []IdleRow{{"a", 5}, {"b", 7}, {"c", 9}, {"d", 11}}
	got := IdlePlace(rows, 3, 0) // 高度未知：全画
	if len(got) != 4 || got[0] != (IdlePut{3, 5, "a", true}) || got[3] != (IdlePut{6, 11, "d", false}) {
		t.Fatalf("%+v", got)
	}
	got = IdlePlace(rows, 3, 5) // 第 3、4、5 行放得下，第 6 行在窗外
	if len(got) != 3 || got[2].Row != 5 || got[2].Text != "c" {
		t.Fatalf("矮窗应丢掉窗外的行：%+v", got)
	}
	if got = IdlePlace(rows, 9, 5); len(got) != 0 {
		t.Fatalf("整块都在窗外：%+v", got)
	}
	for i, p := range IdlePlace(rows, 1, 100) {
		if p.Col != rows[i].Col || p.Brand != (i == 0) {
			t.Fatalf("起始列或品牌色不对：%+v", p)
		}
	}
}
