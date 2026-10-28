package deckui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// n071：折行工具——不超宽、不丢字、不拆英文词、收尾标点不留在行首。
func TestN071WrapText(t *testing.T) {
	texts := []string{
		"接管当前显示的子 agent（切走、隐藏或 Ctrl-Q 即自动交还）",
		"把中间正在看的会话放回后台（会话照常运行）",
		"显示/隐藏左栏",
		"Take over the sub agent on screen (switching away, hiding, or Ctrl-Q hands it back)",
		"Pick a session in the left pane to show it here. Sessions keep running in the background.",
		"一个特别特别长的没有任何空格也没有标点的说明文字用来测试硬断行的情形是否正常工作",
		"averyveryverylongasciiwordwithoutanyspacesatall-and-more",
		"",
		"   ",
	}
	for _, s := range texts {
		for limit := 1; limit <= 120; limit++ {
			lines := wrapText(s, limit)
			if len(lines) == 0 {
				t.Fatalf("%q/%d：没有输出", s, limit)
			}
			for _, l := range lines {
				// 单个不可分的整体比 limit 宽时才允许硬断；其余不得超宽（limit 比一个汉字还窄时至少放一个，宽 2 > 1）
				if lipgloss.Width(l) > limit && limit >= 2 {
					t.Fatalf("%q/%d：超宽行 %q（%d 列）", s, limit, l, lipgloss.Width(l))
				}
				if l != strings.TrimSpace(l) && strings.TrimSpace(s) != "" {
					t.Fatalf("%q/%d：行首尾有空格 %q", s, limit, l)
				}
			}
			if got, want := strings.Join(strings.Fields(strings.Join(lines, "")), ""), strings.Join(strings.Fields(s), ""); got != want {
				t.Fatalf("%q/%d：丢字或改字：%q", s, limit, got)
			}
			if limit >= 4 {
				for _, l := range lines[1:] {
					if l != "" && strings.ContainsRune(wrapClosing, []rune(l)[0]) && []rune(l)[0] > 0x7f {
						t.Fatalf("%q/%d：收尾标点留在行首：%q", s, limit, l)
					}
				}
			}
		}
	}
	// 不拆英文词：只要每个词放得下一行
	for limit := 9; limit <= 60; limit++ {
		lines := wrapText("接管当前显示的子 agent（切走、隐藏或 Ctrl-Q 即自动交还）", limit)
		joined := strings.Join(lines, "\n")
		for _, w := range []string{"agent", "Ctrl-Q"} {
			if !strings.Contains(joined, w) {
				t.Fatalf("limit %d：英文词 %q 被拆开：\n%s", limit, w, joined)
			}
		}
	}
}
