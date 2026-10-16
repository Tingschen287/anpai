package deckui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/charmbracelet/x/ansi"
)

// uniqueName 是 n 个各不相同的汉字：画面里每个字都找得到，才说明整段名字翻得到（不是只看见重复的一段）。
func uniqueName(n int) string {
	r := make([]rune, n)
	for i := range r {
		r[i] = rune(0x4E00 + i)
	}
	return string(r)
}

// seenAll 从最上面翻到最下面，把看到的每一屏拼起来；每一屏都检查不超高、不超宽，并交给 each 再查。
func seenAll(t *testing.T, m *Settings, each func(v string)) string {
	t.Helper()
	check := func(v string) {
		t.Helper()
		review2Bounds(t, m, v)
		if each != nil {
			each(v)
		}
	}
	v := plainView(m)
	check(v)
	for i := 0; i < 80 && !strings.Contains(v, T("scroll.up", 0)) && strings.Contains(v, "上面还有"); i++ {
		v = review2Key(m, "pgup")
		check(v)
	}
	seen := v
	for i := 0; i < 80 && strings.Contains(v, "下面还有") && !strings.Contains(v, T("scroll.down", 0)); i++ {
		v = review2Key(m, "pgdown")
		check(v)
		seen += "\n" + v
	}
	return seen
}

func missingRunes(seen, name string) []rune {
	var miss []rune
	for _, r := range name {
		if !strings.ContainsRune(seen, r) {
			miss = append(miss, r)
		}
	}
	return miss
}

// 评审 2 B1：名字长到几百字时，固定的标题最多占一屏的三分之一，其余随正文滚动；整段名字翻得到，
// 页签与子视图的「esc 返回」始终在固定区里。
func TestLongTitleCappedAndReachable(t *testing.T) {
	sizes := [][2]int{{80, 24}, {81, 25}, {100, 24}, {100, 30}, {119, 24}, {140, 40}, {80, 60}}
	name := uniqueName(900)
	for _, size := range sizes {
		t.Run(fmt.Sprintf("project_%dx%d", size[0], size[1]), func(t *testing.T) {
			m, _ := n057Fixture()
			m.width, m.height = size[0], size[1]
			m.project().Name = name
			if err := m.set.Validate(); err != nil {
				t.Fatal(err)
			}
			seen := seenAll(t, m, func(v string) {
				if !strings.Contains(strings.Split(v, "\n")[0], T("page.project")) {
					t.Fatalf("页签不在第一行：\n%s", v)
				}
			})
			if miss := missingRunes(seen, name); len(miss) > 0 {
				t.Fatalf("项目名有 %d 个字翻不到", len(miss))
			}
		})
		t.Run(fmt.Sprintf("agent_%dx%d", size[0], size[1]), func(t *testing.T) {
			m, _ := n057Fixture()
			m.width, m.height = size[0], size[1]
			m.agentPtr("claude").Name = name
			if err := m.set.Validate(); err != nil {
				t.Fatal(err)
			}
			goRow(t, m, rowGlobal, "claude", "")
			review2Key(m, "enter")
			esc := func(v string) {
				top := strings.Join(strings.Split(v, "\n")[:pinCap(m.height)], "\n")
				if !strings.Contains(top, "esc 返回") {
					t.Fatalf("「esc 返回」不在固定区：\n%s", v)
				}
			}
			seen := seenAll(t, m, esc)
			if miss := missingRunes(seen, name); len(miss) > 0 {
				t.Fatalf("agent 名有 %d 个字翻不到", len(miss))
			}
			// ↓ 到最后一个模型，光标一直看得见，「esc 返回」仍在
			for i := 0; i < 10; i++ {
				v := review2Key(m, "down")
				review2Bounds(t, m, v)
				esc(v)
				if !strings.Contains(v, "›") {
					t.Fatalf("光标看不见：\n%s", v)
				}
			}
		})
		t.Run(fmt.Sprintf("tier_%dx%d", size[0], size[1]), func(t *testing.T) {
			m, _ := n057Fixture()
			m.width, m.height = size[0], size[1]
			m.project().Name = name
			goRow(t, m, rowTier, "", deckstore.Frontier)
			v := review2Key(m, "enter")
			if m.sub == nil {
				t.Fatal("没进档位清单")
			}
			review2Bounds(t, m, v)
			if !strings.Contains(strings.Join(strings.Split(v, "\n")[:pinCap(m.height)], "\n"), "esc 返回") {
				t.Fatalf("「esc 返回」不在固定区：\n%s", v)
			}
			if miss := missingRunes(seenAll(t, m, nil), name); len(miss) > 0 {
				t.Fatalf("档位清单标题里的项目名有 %d 个字翻不到", len(miss))
			}
		})
	}
}

// 短名字的标题不变：按键提示仍在标题下面一行（只有标题本身放不下时才挪到上面）。
func TestShortTitleKeepsKeysBelow(t *testing.T) {
	m, _ := n057Fixture()
	m.width, m.height = 80, 24
	goRow(t, m, rowGlobal, "claude", "")
	lines := strings.Split(review2Key(m, "enter"), "\n")
	if !strings.Contains(lines[0], "claude") || !strings.Contains(lines[1], "esc 返回") {
		t.Fatalf("短标题的布局变了：\n%s", strings.Join(lines[:3], "\n"))
	}
	m.width = 40
	lines = strings.Split(plainView(m), "\n")
	if !strings.Contains(lines[0], "claude") {
		t.Fatalf("窄窗口短标题应仍在第一行：\n%s", strings.Join(lines[:4], "\n"))
	}
}

// 评审 2 已知问题 1：底部很长（长报错）又列着候选时，每按一次 ↓ 选中的候选（连同折下来的续行）都在画面里，
// 输入框那一行也在。
func TestCandidateAndInputBothVisible(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 24}, {80, 30}} {
		m, api := n057Fixture()
		m.width, m.height = size[0], size[1]
		api.tools[0].CanList = true
		for i := 0; i < 12; i++ {
			api.models["claude"] = append(api.models["claude"], fmt.Sprintf("candidate-%02d-%s-end%02d", i, strings.Repeat("x", 100), i))
		}
		api.fail = errors.New(strings.Repeat("存不上；", 140))
		goRow(t, m, rowAgentPick, "codex", "")
		review2Key(m, " ")
		goRow(t, m, rowGlobal, "claude", "")
		review2Key(m, "enter")
		review2Key(m, "+")
		n := 0
		for i := 0; i < 12; i++ {
			v := review2Key(m, "down")
			if m.sub.input.cur != i {
				break // 这个尺寸下候选缩减了（底部固定得下时，候选个数随高度缩减，见 TestInputCandidatesShrinkInShortWindows）
			}
			n++
			review2Bounds(t, m, v)
			if !strings.Contains(v, fmt.Sprintf("› candidate-%02d-", i)) || !strings.Contains(v, fmt.Sprintf("end%02d", i)) {
				t.Fatalf("%v：选中的候选 %d 不全在画面里：\n%s", size, i, v)
			}
			if !strings.Contains(v, "▏") {
				t.Fatalf("%v：选候选 %d 时输入框不在画面里：\n%s", size, i, v)
			}
		}
		if size == [2]int{80, 24} && n != 12 {
			t.Fatalf("80×24 底部放不下时应整块滚动、12 个候选都选得到，实际 %d 个", n)
		}
		for i := n - 1; i > 0; i-- {
			v := review2Key(m, "up")
			if !strings.Contains(v, fmt.Sprintf("› candidate-%02d-", i-1)) || !strings.Contains(v, "▏") {
				t.Fatalf("%v：↑ 回到候选 %d 时看不见：\n%s", size, i-1, v)
			}
		}
	}
}

// 输入框里打得很长（折成许多行）时，打字处「▏」仍在画面里。
func TestLongTypedTextCaretVisible(t *testing.T) {
	m, _ := n057Fixture()
	m.width, m.height = 80, 24
	m.saveErr = strings.Repeat("存不上；", 140)
	goRow(t, m, rowGlobal, "claude", "")
	review2Key(m, "enter")
	review2Key(m, "+")
	m.sub.input.text = strings.Repeat("长", 1000)
	v := plainView(m)
	review2Bounds(t, m, v)
	if !strings.Contains(v, "▏") {
		t.Fatalf("打字处不在画面里：\n%s", v)
	}
	if w := ansi.StringWidth(v); w == 0 {
		t.Fatal("空画面")
	}
}

// 候选回来以后不再写「正在问 … 有哪些模型」（attempt 3 自查：以前一直挂在输入框下面）。
func TestAskingNoteClearedWhenModelsArrive(t *testing.T) {
	m, api := n057Fixture()
	m.width, m.height = 80, 24
	api.tools[0].CanList = true
	api.models["claude"] = []string{"claude-x-1"}
	goRow(t, m, rowGlobal, "claude", "")
	review2Key(m, "enter")
	v := review2Key(m, "+")
	if m.sub.input.loading || strings.Contains(v, "正在问") || !strings.Contains(v, "claude-x-1") {
		t.Fatalf("候选回来后仍写着正在问，或没列出候选：\n%s", v)
	}
}
