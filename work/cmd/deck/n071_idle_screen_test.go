package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/layout"
)

// n071 attempt 2（Codex 审查 R2）：空闲页的黄金样张与可见性断言走 idle() 真实使用的那条路（idleFrame：
// 选 logo 大小、算顶部位置、摆放并裁掉窗外的行），不是未摆放的逻辑行。

// idleScreenOf 把 idleFrame 的结果摆成 h 行的屏幕（纯文本，去掉颜色）；同一行上的块按起始列排好，重叠就报错。
func idleScreenOf(t *testing.T, w, h int) []string {
	t.Helper()
	byRow := map[int][]idlePut{}
	for _, p := range idleFrame(w, h) {
		if p.Row < 1 || p.Row > h {
			t.Fatalf("%dx%d：画到了窗外第 %d 行：%q", w, h, p.Row, p.Text)
		}
		byRow[p.Row] = append(byRow[p.Row], p)
	}
	out := make([]string, h)
	for r := 1; r <= h; r++ {
		ps := byRow[r]
		sort.Slice(ps, func(i, j int) bool { return ps[i].Col < ps[j].Col })
		line, col := "", 1
		for _, p := range ps {
			if p.Col < col {
				t.Fatalf("%dx%d 第 %d 行：两块重叠（%q 起始列 %d，前一块写到第 %d 列）", w, h, r, p.Text, p.Col, col-1)
			}
			line += strings.Repeat(" ", p.Col-col)
			txt := ansi.Strip(p.Text)
			line += txt
			col = p.Col + lipgloss.Width(txt)
		}
		out[r-1] = line
	}
	return out
}

func idleLangs(t *testing.T) []string {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	return []string{i18n.ZH, i18n.EN}
}

// 黄金样张：中英 × 若干窗口（含窄、矮、又窄又矮），右缘用竖线标出，整个屏幕 h 行。
// 重新生成：N071_UPDATE=1 go test ./cmd/deck -run N071IdleScreenGolden；导出到别处：N071_RENDER_OUT=<目录>。
func TestN071IdleScreenGolden(t *testing.T) {
	for _, lang := range idleLangs(t) {
		i18n.SetLang(lang)
		for _, sz := range [][2]int{{120, 40}, {80, 24}, {44, 24}, {44, 12}, {40, 12}, {30, 24}, {24, 24}, {9, 24}, {6, 24}} {
			w, h := sz[0], sz[1]
			var b strings.Builder
			fmt.Fprintf(&b, "# %s %dx%d：idle() 真实摆放与裁剪后的屏幕（竖线是窗口右缘）\n", lang, w, h)
			for _, l := range idleScreenOf(t, w, h) {
				b.WriteString(l + strings.Repeat(" ", max(0, w-lipgloss.Width(l))) + "|\n")
			}
			goldenCheckScreen(t, fmt.Sprintf("screen-%s-%dx%d.txt", lang, w, h), b.String())
		}
	}
}

func goldenCheckScreen(t *testing.T, name, got string) {
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

// 屏幕上：任何宽高都没有一行超出窗口、没有画到窗外、块不重叠（w≥2：再窄连一个汉字都放不下）。
func TestN071IdleScreenFitsWindow(t *testing.T) {
	for _, lang := range idleLangs(t) {
		i18n.SetLang(lang)
		for w := 2; w <= 130; w++ {
			for _, h := range []int{1, 3, 6, 12, 24, 40, 70} {
				for r, l := range idleScreenOf(t, w, h) {
					if lipgloss.Width(l) > w {
						t.Fatalf("%s %dx%d 第 %d 行超宽 %d：%q", lang, w, h, r+1, lipgloss.Width(l), l)
					}
				}
			}
		}
	}
}

// 高度不够不改版式：宽度够放表格（≥26 列）时，屏幕上看得见的表格行始终是两列——
// 从 F2 那一行起，每一行要么缩进恰好是按键列（按键行：说明都从同一个说明列左边界开始），要么缩进恰好是说明列（续行）；
// 看不见的只是底部被裁掉的行。
func TestN071IdleScreenShortKeepsTwoColumns(t *testing.T) {
	for _, lang := range idleLangs(t) {
		i18n.SetLang(lang)
		keys := layout.Keys()
		for _, w := range []int{26, 30, 40, 44, 60, 80, 120} {
			for h := 5; h <= 45; h++ {
				screen := idleScreenOf(t, w, h)
				keyCol, descCol, seen := -1, -1, 0
				indent := func(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }
				for _, l := range screen {
					if strings.TrimSpace(l) == "" {
						continue
					}
					body := strings.TrimLeft(l, " ")
					if keyCol < 0 {
						if strings.HasPrefix(body, keys[0].Key+" ") { // F2：表格从这里开始
							keyCol = indent(l)
							descCol = keyCol + lipgloss.Width(keys[0].Key)
							for _, k := range keys {
								descCol = max(descCol, keyCol+lipgloss.Width(k.Key))
							}
							descCol += 2
							seen = 1
							if got := lipgloss.Width(l) - lipgloss.Width(strings.TrimLeft(body[len(keys[0].Key):], " ")); got != descCol {
								t.Fatalf("%s %dx%d：F2 行的说明列在 %d，应为最长按键加两格 = %d：%q", lang, w, h, got, descCol, l)
							}
						}
						continue
					}
					switch indent(l) {
					case keyCol:
						if seen >= len(keys) || !strings.HasPrefix(body, keys[seen].Key+" ") {
							t.Fatalf("%s %dx%d：按键列上的行不是下一个按键 %v：%q\n%s", lang, w, h, seen, l, strings.Join(screen, "\n"))
						}
						seen++
					case descCol: // 续行
					default:
						t.Fatalf("%s %dx%d：表格行既不在按键列（%d）也不在说明列（%d）：%q\n%s", lang, w, h, keyCol, descCol, l, strings.Join(screen, "\n"))
					}
				}
				if h >= 40 && seen != len(keys) {
					t.Fatalf("%s %dx%d：窗口够高，8 个按键应都看得见，实际 %d", lang, w, h, seen)
				}
			}
		}
	}
}
