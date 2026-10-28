package deckui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/layout"
)

// n071：空闲页的版式（owner 确认的草图 A「整块居中，块内左对齐」）。

// idleScreen 按 cmd/deck 画空闲页的方式把各行摆到 w 列的屏幕上，返回每行的起始列、文字与结束列（显示宽度）。
func idleScreen(w, h int) (rows []IdleRow, ends []int) {
	rows = IdleRows(w, h)
	for _, r := range rows {
		ends = append(ends, r.Col-1+lipgloss.Width(r.Text))
	}
	return
}

func keyRowCount() int { return len(layout.Keys()) }

// 表格整块居中、块内两列各自左对齐：所有表格行起始列相同；按键列起点一致；说明列的左边界一致；续行也从说明列左边界开始。
func TestN071IdleTableAlignsLeftAsOneBlock(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		for _, w := range []int{60, 80, 100, 120, 200} {
			rows := IdleRows(w, 40)
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
			rows := IdleRows(w, 40)
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
	rows := IdleRows(80, 40)
	if len(rows) <= len(IdleLines(80)) {
		t.Fatalf("英文 80 列下 F2 的说明与提示句应折行，行数 %d", len(rows))
	}
	for _, r := range rows {
		if r.Col-1+lipgloss.Width(r.Text) > 80 {
			t.Errorf("超出 80 列：%q", r.Text)
		}
	}
}

// 窄窗（放不下按键列 + 最短说明）：退回逐行居中；每一行（太长就折成几段）各自居中，不超窗口，字一个不少。
func TestN071IdleNarrowFallsBackToPerLine(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		for _, w := range []int{10, 12, 20, 24} {
			rows := IdleRows(w, 0)
			for i, r := range rows {
				if r.Col != max(1, (w-lipgloss.Width(r.Text))/2+1) {
					t.Errorf("%s w%d 行 %d：没居中：%+v", lang, w, i, r)
				}
				if r.Col-1+lipgloss.Width(r.Text) > w && lipgloss.Width(r.Text) <= w-2 {
					t.Errorf("%s w%d 行 %d：超出窗口：%+v", lang, w, i, r)
				}
				if lipgloss.Width(r.Text) > w {
					t.Errorf("%s w%d 行 %d：比窗口还宽 %d：%q", lang, w, i, lipgloss.Width(r.Text), r.Text)
				}
			}
			var got []string
			for _, r := range rows {
				got = append(got, r.Text)
			}
			if a, b := squashAll(strings.Join(got, "")), squashAll(strings.Join(IdleLines(w), "")); a != b {
				t.Errorf("%s w%d：折行丢了字：\n%q\n%q", lang, w, a, b)
			}
		}
		// 没有窗口宽度（0、负数）与极窄：原样逐行，不 panic
		for _, w := range []int{-5, 0, 1, 2, 3, 4, 6, 8, 9} { // 窗口小到放不下 8 列（w<10）：不折行，免得把 Anpai、Ctrl-Q 拆开
			if rows := IdleRows(w, 0); len(rows) != len(IdleLines(w)) {
				t.Errorf("%s w%d：太窄不折行，应与 IdleLines 同样 %d 行，实际 %d", lang, w, len(IdleLines(w)), len(rows))
			}
		}
	}
}

func squashAll(s string) string { return strings.Join(strings.Fields(s), "") }

// 矮窗：表格折行后比窗口高、逐行写法放得下，就换成逐行写法；矮窗永远不会比不限高时更长。
func TestN071IdleShortWindow(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	switched := 0
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		for w := 30; w <= 120; w++ {
			tab := IdleRows(w, 0)
			for h := 1; h <= 45; h++ {
				res := IdleRows(w, h)
				switch {
				case len(tab) <= h:
					if len(res) != len(tab) {
						t.Fatalf("%s %dx%d：窗口放得下表格却没用表格（%d 对 %d）", lang, w, h, len(res), len(tab))
					}
				case len(res) != len(tab):
					switched++
					if len(res) >= len(tab) {
						t.Fatalf("%s %dx%d：换成的写法没有更短（%d 对 %d）", lang, w, h, len(res), len(tab))
					}
				}
				if len(res) > len(tab) {
					t.Fatalf("%s %dx%d：矮窗比不限高还长（%d 对 %d）", lang, w, h, len(res), len(tab))
				}
			}
		}
	}
	if switched == 0 {
		t.Fatal("扫了一圈也没遇到换成逐行写法的情形：这一支没被测到")
	}
}

// 不丢键：任何宽高下，8 个按键每个都在输出里恰好出现一次（作为说明列左边的按键）。
func TestN071IdleNeverDropsAKey(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		for w := 0; w <= 160; w += 3 {
			for _, h := range []int{0, 3, 12, 24, 60} {
				text := ""
				for _, r := range IdleRows(w, h) {
					text += r.Text
				}
				text = squashAll(text) // 太窄时按键本身也可能被折成两段
				for _, k := range layout.Keys() {
					if !strings.Contains(text, squashAll(k.Key)) {
						t.Fatalf("%s %dx%d：按键 %s 丢了", lang, w, h, k.Key)
					}
				}
			}
		}
	}
}

// 黄金输出：把各行摆到屏幕上（用空格补到起始列）的纯文本，评审直接看版式。
// 重新生成：N071_UPDATE=1 go test ./internal/deckui -run N071IdleGolden；导出到别处：N071_RENDER_OUT=<目录>。
func TestN071IdleGolden(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		for _, sz := range [][2]int{{80, 24}, {120, 40}, {44, 24}, {30, 24}} {
			rows := IdleRows(sz[0], sz[1])
			var b strings.Builder
			fmt.Fprintf(&b, "# %s %dx%d（竖线是窗口右缘）\n", lang, sz[0], sz[1])
			for _, r := range rows {
				line := strings.Repeat(" ", r.Col-1) + r.Text
				pad := sz[0] - lipgloss.Width(line)
				if pad > 0 {
					line += strings.Repeat(" ", pad)
				}
				b.WriteString(line + "|\n")
			}
			goldenCheckIdle(t, fmt.Sprintf("idle-%s-%dx%d.txt", lang, sz[0], sz[1]), b.String())
		}
	}
}

func goldenCheckIdle(t *testing.T, name, got string) {
	t.Helper()
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
