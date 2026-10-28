package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/termprobe"
)

// 空闲页的 logo：真机上 16x8 是一团、32x16 清楚（n069 样张 4）。放得下用 32x16，放不下退回 16x8，再放不下不画。
func TestIdleArtPicksTheBiggestThatFits(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	lines := len(idleLines(120))
	cases := []struct {
		w, h      int
		rows, wid int
	}{
		{120, 60, 16, 32},             // 宽敞
		{120, lines + 16 + 3, 16, 32}, // 高度刚好放下大的
		{120, lines + 16 + 2, 8, 16},  // 差一行：退回小的
		{36, 60, 16, 32},              // 宽刚好
		{35, 60, 8, 16},               // 宽差一格：小的
		{20, 60, 8, 16},               // 窄
		{19, 60, 0, 0},                // 比小的还窄（16+4）：不画
		{120, lines + 8 + 3, 8, 16},   // 高度刚好放下小的
		{120, lines + 8 + 2, 0, 0},    // 放不下：不画，免得挤掉按键说明
		{0, 0, 0, 0},                  // 尺寸未知
	}
	for _, c := range cases {
		art, wid := idleArt(c.w, c.h, lines)
		if len(art) != c.rows || wid != c.wid {
			t.Errorf("%dx%d：画了 %d 行 %d 格宽，应为 %d 行 %d 格", c.w, c.h, len(art), wid, c.rows, c.wid)
		}
		for _, l := range art {
			if got := ansi.StringWidth(l); got != wid {
				t.Errorf("%dx%d：一行占 %d 列，应为 %d", c.w, c.h, got, wid)
			}
		}
	}
}

// 不是 iTerm2：画法判断与 n056 完全一样，不多发查询、不合成背景（Windows Terminal 不回退）。
func TestDecideGraphicsNonITermIsUnchanged(t *testing.T) {
	for _, k := range []string{"TERM_PROGRAM", "LC_TERMINAL"} {
		t.Setenv(k, "")
	}
	wt := termprobe.Parse("\x1b[6;20;10t\x1b[?61;4;6;7;14;21;22;23;24;28;32;42;52c")
	d := decideGraphics("", wt, false)
	if d.Mode != "sixel" || d.Background != nil {
		t.Fatalf("%+v", d)
	}
	if d := decideGraphics("", termprobe.Result{}, false); d.Mode != "none" {
		t.Fatalf("没回答：字母徽章：%+v", d)
	}
	// 在 iTerm2 里但标准输入不是终端（测试里）：问不了背景色，自动判断退回字母徽章，不是实心方块
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	it := termprobe.Parse("\x1b[6;19;8t\x1b[?64;1;2;4;6;17;18;21;22;52c")
	if d := decideGraphics("", it, true); d.Mode != "none" || d.Background != nil {
		t.Fatalf("iTerm2 问不到背景色应退回字母徽章：%+v", d)
	}
}

// pushTitle 不是终端时什么都不发（脚本、管道里跑 deck up 不能被转义序列弄脏输出）。
func TestPushTitleSilentWhenNotATerminal(t *testing.T) {
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	pop := pushTitle("安排 Anpai")
	pop()
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("不该输出：%q", out)
	}
}
