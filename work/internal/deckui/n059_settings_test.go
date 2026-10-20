package deckui

import (
	"fmt"
	"strings"
	"testing"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// n059（owner：「UIUE能不能再优化得更舒服一点。更简单点」）：分页、自动保存、中文档位、上下文提示、整行高亮、矮窗口、文字表。

func cjk(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF) {
			return true
		}
	}
	return false
}

// 文字表把关（n060 从设置三文件推广到 deck 自己画的界面）：代码里不能有中文字面量；用到的键都在表里，表里的键都有人用。
func TestSettingsTextHasNoStrayChinese(t *testing.T) {
	checkNoStrayChinese(t)
	if T("没有这个键") != "没有这个键" || T("tier.frontier") != "最强" || T("main.uses", "x") != "起来时用 x" {
		t.Fatal("T 取文字、填参数，缺的键原样返回")
	}
}

// 分页：Tab / Shift+Tab / 1 2 3 换页，各页的光标各自记着；页签标出当前页；←→ 只在项目页换项目。
func TestPagesKeepTheirCursor(t *testing.T) {
	m, _ := n057Fixture()
	m.set.Projects = append(m.set.Projects, deckstore.ProjectDef{ID: "b", Name: "b", Path: "/tmp/b"})
	press(m, "down", "down")
	press(m, "tab")
	if m.page != pageAgents || m.cur != 0 {
		t.Fatalf("Tab 到全局 agent 页，光标从头：%d %d", m.page, m.cur)
	}
	press(m, "down", "right")
	if m.proj != 0 {
		t.Fatal("全局 agent 页按 → 不换项目")
	}
	press(m, "shift+tab")
	if m.page != pageProject || m.cur != 2 {
		t.Fatalf("回项目页，光标还在原处：%d %d", m.page, m.cur)
	}
	press(m, "2")
	if m.page != pageAgents || m.cur != 1 {
		t.Fatalf("数字键跳页，光标还在原处：%d %d", m.page, m.cur)
	}
	press(m, "1", "right")
	if m.proj != 1 || !strings.Contains(plainView(m), "‹ b ›  2/2") {
		t.Fatalf("项目页 → 换项目：\n%s", plainView(m))
	}
	v := m.View()
	if !strings.Contains(v, tabOn+" "+T("page.project")+" "+reset) || strings.Contains(v, tabOn+" "+T("page.agents")) {
		t.Fatal("页签只标出当前页")
	}
}

// 上下文提示：底部一行说明随光标所在行变；固定的大段说明没有了。光标行整行加底色，别的行没有。
func TestHelpFollowsCursorAndRowIsHighlighted(t *testing.T) {
	m, _ := n057Fixture()
	m.width, m.height = 100, 40
	want := map[rowKind]string{rowAgentPick: "空格 勾选或取消", rowMain: "空格 换主 agent", rowTier: "⏎ 多选并排顺序"}
	seen := map[rowKind]bool{}
	for i, r := range m.rows() {
		m.cur = i
		v := plainView(m)
		if w, ok := want[r.kind]; ok && !strings.Contains(v, w) {
			t.Fatalf("第 %d 行（%v）的说明应有 %q：\n%s", i, r.kind, w, v)
		}
		for k, w := range want {
			if k != r.kind && strings.Contains(v, w) {
				t.Fatalf("光标在 %v 时不该显示 %v 的说明 %q", r.kind, k, w)
			}
		}
		seen[r.kind] = true
		var hl []string
		for _, l := range strings.Split(m.View(), "\n") {
			if strings.HasPrefix(l, selBg) {
				hl = append(hl, l)
			}
		}
		if len(hl) != 1 || !strings.Contains(hl[0], "›") || ansi.StringWidth(hl[0]) != 100 {
			t.Fatalf("应只有光标行整行加底色、铺满窗口宽：%q", hl)
		}
	}
	if len(seen) != 3 {
		t.Fatal("三种行都要看过")
	}
	if v := plainView(m); strings.Contains(v, "档位行按 ⏎ 多选") || strings.Contains(v, "每档清单是只说档位时的挑选顺序") {
		t.Fatalf("固定的大段说明应去掉：\n%s", v)
	}
	press(m, "tab")
	m.cur = rowIdx(t, m, rowAddAgent, "", "") // 段 3：全局 agent 页最后一行是「界面语言」，「加入」行改按种类找（断言不变）
	if !strings.Contains(plainView(m), "从本机装了的 CLI 里加入") {
		t.Fatal("全局 agent 页「加入」行的说明")
	}
}

// 中文档位：项目页、全局 agent 页、agent 编辑、档位多选都写中文档位名；按档位分色。
func TestTiersShownInChinese(t *testing.T) {
	m, _ := n057Fixture()
	m.project().Models[deckstore.Balanced] = deckstore.ModelList{"gpt-5.6-terra"}
	views := []string{m.View()}
	press(m, "tab")
	views = append(views, m.View())
	goRow(t, m, rowGlobal, "claude", "")
	press(m, "enter")
	views = append(views, m.View())
	press(m, "esc")
	goRow(t, m, rowTier, "", deckstore.Balanced)
	press(m, "enter")
	views = append(views, m.View())
	for i, v := range views {
		p := ansi.Strip(v)
		if strings.Contains(p, "frontier") || strings.Contains(p, "balanced") || strings.Contains(p, "economy") {
			t.Fatalf("第 %d 个画面还有英文档位名：\n%s", i, p)
		}
	}
	if !strings.Contains(ansi.Strip(views[0]), "日常  gpt-5.6-terra") || !strings.Contains(views[1], tierColor(deckstore.Frontier)+"最强") ||
		!strings.Contains(views[1], tierColor(deckstore.Economy)+"便宜") || !strings.Contains(ansi.Strip(views[3]), "ai · 日常 档") {
		t.Fatal("中文档位名、分色")
	}
}

// 全局 agent 页列出全部模型（owner 选「保持全部列出」），放不下就折行、不截断；续行对齐到模型那一列。
func TestAgentsPageWrapsModels(t *testing.T) {
	m, _ := n057Fixture()
	m.width, m.height = 70, 60
	for i := 0; i < 6; i++ {
		m.set.Agents[1].Models = append(m.set.Agents[1].Models, deckstore.ModelDef{ID: fmt.Sprintf("opencode/very-long-model-name-%d-free", i), Tier: deckstore.Balanced})
	}
	press(m, "tab")
	v := plainView(m)
	for _, md := range m.set.Agents[1].Models {
		if !strings.Contains(v, md.ID) {
			t.Fatalf("模型 %s 被截掉了：\n%s", md.ID, v)
		}
	}
	for _, l := range strings.Split(v, "\n") {
		if ansi.StringWidth(l) > 70 {
			t.Fatalf("太宽：%q", l)
		}
	}
	lines := strings.Split(v, "\n")
	first := -1
	for i, l := range lines {
		if strings.Contains(l, "Codex") {
			first = i
		}
	}
	if first < 0 || !strings.HasPrefix(lines[first+1], strings.Repeat(" ", 20)) || !strings.Contains(lines[first+1], "very-long") {
		t.Fatalf("续行应对齐到模型列：\n%s", v)
	}
}

// 矮窗口：agent 编辑里打开输入框时候选个数随高度缩减，输入框、顶部「esc 返回」、底部说明都看得见，总行数不超窗口（n057 评审 2、3 的已知问题）。
func TestInputCandidatesShrinkInShortWindows(t *testing.T) {
	for _, size := range [][2]int{{120, 24}, {80, 20}, {60, 16}, {60, 12}} {
		m, _ := n057Fixture()
		m.width, m.height = size[0], size[1]
		var all []string
		for i := 0; i < 20; i++ {
			all = append(all, fmt.Sprintf("claude-cand-%02d", i))
		}
		goRow(t, m, rowGlobal, "claude", "")
		press(m, "enter", "+")
		m.sub.input.all = all
		v := plainView(m)
		lines := strings.Split(v, "\n")
		if len(lines) > size[1] || !strings.Contains(v, "加模型：") || !strings.Contains(v, "esc 返回") || !strings.Contains(v, "esc 取消") {
			t.Fatalf("%dx%d：输入框与返回提示都要看得见、不超高（%d 行）：\n%s", size[0], size[1], len(lines), v)
		}
		if size[1] >= 20 && !strings.Contains(v, "claude-cand-00") {
			t.Fatalf("%dx%d：放得下时要列候选：\n%s", size[0], size[1], v)
		}
	}
}
