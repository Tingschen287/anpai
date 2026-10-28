package deckui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/rivo/uniseg"
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

// attempt 3（Codex 复审 2 R3）：硬断按字素簇，宽度以前缀整体的显示宽度为准：
// 变体选择符 emoji、ZWJ 序列、组合附加符不被拆开，也不因逐 rune 累加低估宽度而超宽。
func TestN071WrapHardSplitGraphemes(t *testing.T) {
	texts := []string{
		strings.Repeat("❤️", 6),
		strings.Repeat("\U0001F468‍\U0001F469‍\U0001F467", 3),
		strings.Repeat("é", 8),
		strings.Repeat("\U0001F1E8\U0001F1F3", 3),
	}
	for _, s := range texts {
		for limit := 2; limit <= 30; limit++ {
			lines := wrapText(s, limit)
			var back strings.Builder
			for _, l := range lines {
				if lipgloss.Width(l) > limit {
					// 单个字素簇比 limit 宽（limit=2 的 ZWJ 家庭等）：无法再窄
					if _, rest := firstGrapheme(l); rest != "" {
						t.Fatalf("%q limit %d：超宽 %d 列且不止一个字素簇：%q", s, limit, lipgloss.Width(l), l)
					}
				}
				back.WriteString(l)
			}
			if back.String() != s {
				t.Fatalf("%q limit %d：拆开后字符或顺序变了：%q", s, limit, back.String())
			}
		}
	}
}

func firstGrapheme(s string) (cluster, rest string) {
	cluster, rest, _, _ = uniseg.FirstGraphemeClusterInString(s, -1)
	return
}
