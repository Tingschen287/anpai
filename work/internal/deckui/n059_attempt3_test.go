package deckui

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	tea "github.com/charmbracelet/bubbletea"
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

// 名字很长时打开加模型：候选个数按限高后的标题算，底部（输入框、候选、说明）仍固定在最下面，不因为标题长就整块滚走。
func TestLongTitleInputFootStaysPinned(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}} {
		m, _ := n057Fixture()
		m.width, m.height = size[0], size[1]
		m.agentPtr("claude").Name = uniqueName(900)
		goRow(t, m, rowGlobal, "claude", "")
		review2Key(m, "enter")
		review2Key(m, "+")
		var all []string
		for i := 0; i < 20; i++ {
			all = append(all, fmt.Sprintf("claude-cand-%02d", i))
		}
		m.sub.input.all = all
		v := plainView(m)
		review2Bounds(t, m, v)
		lines := strings.Split(v, "\n")
		if !strings.Contains(lines[len(lines)-1], "esc 取消") || !strings.Contains(v, "加模型：") || !strings.Contains(v, "claude-cand-00") {
			t.Fatalf("%v：底部应固定在最下面并列出候选：\n%s", size, v)
		}
		if !strings.Contains(strings.Join(lines[:pinCap(m.height)], "\n"), "esc 返回") {
			t.Fatalf("%v：「esc 返回」不在固定区：\n%s", size, v)
		}
	}
}

// ---------------- attempt 3 自查 ----------------

// 新建项目确认页：agent 名很长、正文比一屏高时，打开就看得见「项目 id ▏」，打字后也在。
func TestConfirmPageCaretVisible(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {81, 25}, {100, 30}, {119, 24}} {
		for _, name := range []string{"", uniqueName(1024), strings.Repeat("名", 400)} {
			m, _ := n057Fixture()
			m.width, m.height = size[0], size[1]
			if name != "" {
				m.agentPtr("claude").Name = name
			}
			// 目录名最多 80 个字；临时目录前缀长（macOS 的 $TMPDIR 就有 50 多字节）时缩短，使整条路径留在
			// deckstore.CheckPath 的 300 字节上限内，否则选不了目录、进不了确认页（n067）
			base := t.TempDir()
			dir := filepath.Join(base, strings.Repeat("项", min(80, (290-len(base)-1)/3)))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			m.Start = dir
			review2Key(m, "n")
			v := review2Key(m, "enter")
			if m.form == nil || m.form.chosen == "" {
				t.Fatal("没进确认页")
			}
			review2Bounds(t, m, v)
			if !strings.Contains(v, "▏") {
				t.Fatalf("%v 名字 %d 字：确认页看不见项目 id 输入行：\n%s", size, len([]rune(name)), v)
			}
			if v = review2Key(m, "x"); !strings.Contains(v, "x▏") {
				t.Fatalf("%v：打字后看不见项目 id 输入行：\n%s", size, v)
			}
		}
	}
}

// 多个名字拼成一串时每个名字各自清理：一个 1024 字的 agent 名不会把后面的 agent 挤掉。
func TestJoinedNamesEachKeptWhole(t *testing.T) {
	m, _ := n057Fixture()
	m.width, m.height = 100, 200
	m.agentPtr("claude").Name = uniqueName(1024)
	m.Start = t.TempDir()
	review2Key(m, "n")
	v := review2Key(m, "enter")
	if m.form == nil || m.form.chosen == "" {
		t.Fatal("没进确认页")
	}
	flat := strings.ReplaceAll(v, "\n", "") // 长名字折了行，「Codex」可能被折成两段
	last := string(rune(0x4E00 + 1023))     // 1024 字名字的最后一个字：后面应接「、Codex」，不是截短的「…」
	if name := m.agentName("codex"); !strings.Contains(flat, last+T("sep.list")+name) {
		t.Fatalf("拼起来的 agent 清单被截掉（找不到 %q）：\n%s", name, v)
	}
}

// 80×24 及以上：报错长到「0 个候选刚好放得下、1 个就放不下」时，候选不缩到 0（整块滚动、照常列出），↓ 选得到、看得见。
func TestNoZeroCandidatesAtStandardSizes(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {81, 25}, {100, 30}, {119, 24}} {
		for _, long := range []bool{false, true} {
			for n := 200; n <= 1000; n += 15 {
				m, api := n057Fixture()
				m.width, m.height = size[0], size[1]
				api.tools[0].CanList = true
				api.models["claude"] = nil
				for i := 0; i < 12; i++ {
					api.models["claude"] = append(api.models["claude"], fmt.Sprintf("claude-model-%02d", i))
				}
				if long {
					m.agentPtr("claude").Name = uniqueName(900)
				}
				goRow(t, m, rowGlobal, "claude", "")
				review2Key(m, "enter")
				review2Key(m, "+")
				m.saveErr = strings.Repeat("存", n)
				v := review2Key(m, "down")
				review2Bounds(t, m, v)
				if m.sub.input.cur != 0 || !strings.Contains(v, "› claude-model-00") || !strings.Contains(v, "▏") {
					t.Fatalf("%v 长名=%v 报错 %d 字：候选选不到或看不见（limit=%d cur=%d）：\n%s", size, long, n, m.sub.input.limit, m.sub.input.cur, v)
				}
			}
		}
	}
}

// 输入框里打的过滤字长到折行时，选候选也看得见打字处与选中的候选；没选中的长候选折行不算进选中块。
func TestLongFilterWithCandidatesCaretVisible(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {81, 25}, {100, 30}, {119, 24}} {
		m, api := n057Fixture()
		m.width, m.height = size[0], size[1]
		api.tools[0].CanList = true
		api.models["claude"] = nil
		for i := 0; i < 12; i++ {
			api.models["claude"] = append(api.models["claude"], fmt.Sprintf("c%02d-%s-e%02d", i, strings.Repeat("y", 120), i))
		}
		api.fail = errors.New(strings.Repeat("存不上；", 240))
		goRow(t, m, rowAgentPick, "codex", "")
		review2Key(m, " ")
		goRow(t, m, rowGlobal, "claude", "")
		review2Key(m, "enter")
		review2Key(m, "+")
		for i := 0; i < min(size[0]+10, 115); i++ { // 折成两行，又不比候选里的 120 个 y 长
			review2Key(m, "y")
		}
		for i := 0; i < 12; i++ {
			v := review2Key(m, "down")
			review2Bounds(t, m, v)
			if m.sub.input.cur != i {
				t.Fatalf("%v：候选 %d 选不到（cur=%d）", size, i, m.sub.input.cur)
			}
			if !strings.Contains(v, "y▏") || !strings.Contains(v, fmt.Sprintf("› c%02d-", i)) || !strings.Contains(v, fmt.Sprintf("e%02d", i)) {
				t.Fatalf("%v：选候选 %d 时打字处或选中的候选看不见：\n%s", size, i, v)
			}
		}
	}
}

// ⏎ 被拒（候选已有）后说明变长：选中的候选不能掉出列表，再按 ⏎ 也不会把过滤的字当 id 加进去。
func TestSelectionSurvivesRejectedEnter(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 24}, {100, 30}, {120, 40}} {
		for _, idLen := range []int{20, 60, 128} {
			m, api := n057Fixture()
			m.width, m.height = size[0], size[1]
			api.tools[0].CanList = true
			var ids []string
			for i := 0; i < 30; i++ {
				id := fmt.Sprintf("gpt-%02d-", i)
				ids = append(ids, id+strings.Repeat("q", idLen-len(id)))
			}
			api.models["claude"] = ids
			cx := m.agentPtr("codex")
			for _, id := range ids {
				cx.Models = append(cx.Models, deckstore.ModelDef{ID: id, Tier: deckstore.Balanced})
			}
			goRow(t, m, rowGlobal, "claude", "")
			review2Key(m, "enter")
			review2Key(m, "+")
			for _, c := range "gpt" {
				review2Key(m, string(c))
			}
			for i := 0; i < 40; i++ {
				review2Key(m, "down")
			}
			chosen := m.sub.input.matches()[m.sub.input.cur]
			v := review2Key(m, "enter")
			review2Bounds(t, m, v)
			if ms := m.sub.input.matches(); m.sub.input.cur >= len(ms) || ms[m.sub.input.cur] != chosen || !strings.Contains(v, "› gpt-") {
				t.Fatalf("%v len%d：被拒后选中的候选掉出列表或看不见：\n%s", size, idLen, v)
			}
			before := len(m.agentPtr("claude").Models)
			review2Key(m, "enter")
			if len(m.agentPtr("claude").Models) != before {
				t.Fatalf("%v len%d：再按 ⏎ 加进了 %q", size, idLen, m.agentPtr("claude").Models[before].ID)
			}
		}
	}
}

// 候选迟到：等候选时 ⏎ 被拒的原因不被抹掉；落不进改 id 的输入框。
func TestLateModelListKeepsNotesAndSkipsRename(t *testing.T) {
	m, api := n057Fixture()
	m.width, m.height = 80, 24
	api.tools[0].CanList = true
	goRow(t, m, rowGlobal, "claude", "")
	review2Key(m, "enter")
	plainView(m)
	_, slow := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("+")}) // 候选还在路上
	for _, c := range "gpt x" {
		review2Key(m, string(c))
	}
	review2Key(m, "enter") // 有空格：被拒
	reason := m.sub.input.note
	run(m, slow)
	if v := plainView(m); m.sub.input.note != reason || reason == "" {
		t.Fatalf("候选回来后拒绝原因没了（原来 %q，现在 %q）：\n%s", reason, m.sub.input.note, v)
	}

	m, api = n057Fixture()
	m.width, m.height = 80, 24
	api.tools[0].CanList = true
	api.models["claude"] = []string{"zzz-from-cli"}
	goRow(t, m, rowGlobal, "claude", "")
	review2Key(m, "enter")
	plainView(m)
	_, slow = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("+")})
	review2Key(m, "esc")
	review2Key(m, "e")
	before := m.agentPtr("claude").Models[0].ID
	run(m, slow)
	if len(m.sub.input.all) != 0 {
		t.Fatalf("迟到的候选落进了改 id 的输入框：%v", m.sub.input.all)
	}
	for _, c := range "-v2" {
		review2Key(m, string(c))
	}
	review2Key(m, "down")
	review2Key(m, "enter")
	if after := m.agentPtr("claude").Models[0].ID; after != before+"-v2" {
		t.Fatalf("改 id 用了 %q，不是打的 %q", after, before+"-v2")
	}
}

// 标题宽度正好等于窗口宽时一行放得下：按键提示仍在标题下面（与 a49e1e8 一样）。
func TestTitleExactWidthKeepsKeysBelow(t *testing.T) {
	m, _ := n057Fixture()
	m.width, m.height = 80, 24
	title := " " + T("agent.title", strings.Repeat("a", 1), "claude")
	m.agentPtr("claude").Name = strings.Repeat("a", 80-ansi.StringWidth(title)+1)
	goRow(t, m, rowGlobal, "claude", "")
	lines := strings.Split(review2Key(m, "enter"), "\n")
	if w := ansi.StringWidth(lines[0]); w != 80 || !strings.Contains(lines[1], "esc 返回") {
		t.Fatalf("标题 %d 格时布局变了：\n%s", w, strings.Join(lines[:3], "\n"))
	}
}

// PgUp 到顶、再 PgDn（或反过来）回得到原来的画面：选中的候选与输入框都在（attempt 3 自查：以前翻过头）。
func TestPagingReturnsHome(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}, {140, 40}} {
		m, api := n057Fixture()
		m.width, m.height = size[0], size[1]
		api.tools[0].CanList = true
		api.models["claude"] = nil
		for i := 0; i < 12; i++ {
			api.models["claude"] = append(api.models["claude"], fmt.Sprintf("cand-%02d-%s", i, strings.Repeat("q", 119)))
		}
		api.fail = errors.New(strings.Repeat("存不上；", 240))
		goRow(t, m, rowAgentPick, "codex", "")
		review2Key(m, " ")
		goRow(t, m, rowGlobal, "claude", "")
		review2Key(m, "enter")
		review2Key(m, "+")
		for i := 0; i < 9; i++ {
			review2Key(m, "down")
		}
		home := plainView(m)
		moved := 0
		for _, keys := range [][]string{{"pgdown", "pgup"}, {"pgup", "pgdown"}} {
			// 第一下真的翻动了（不在顶上或底上）才要求第二下回到原来的画面
			if v := review2Key(m, keys[0]); v != home {
				moved++
				if v = review2Key(m, keys[1]); v != home {
					t.Fatalf("%v %v：回不到原来的画面：\n%s\n--- 原来：\n%s", size, keys, v, home)
				}
			}
			review2Key(m, "down")
			review2Key(m, "up")
		}
		if size == [2]int{80, 24} && moved != 2 {
			t.Fatalf("80×24 底部放不下时 PgUp、PgDn 都应翻得动，实际 %d 个", moved)
		}
	}
}

// ---------------- attempt 3 自查 2 ----------------

// agent 名 1024 字时「主 agent」一行仍写出档位（项目页、新建项目确认页）：名字自己清理，档位接在后面。
func TestMainLineKeepsTierWithLongName(t *testing.T) {
	name := uniqueName(1024)
	last := string(rune(0x4E00 + 1023))
	m, _ := n057Fixture()
	m.width, m.height = 100, 30
	m.agentPtr("claude").Name = name
	flat := strings.ReplaceAll(seenAll(t, m, nil), "\n", "")
	if !strings.Contains(flat, last+" · "+tierName(deckstore.Frontier)) {
		t.Fatal("项目页主 agent 一行丢了档位")
	}
	m.Start = t.TempDir()
	review2Key(m, "n")
	review2Key(m, "enter")
	if m.form == nil || m.form.chosen == "" {
		t.Fatal("没进确认页")
	}
	if flat = strings.ReplaceAll(seenAll(t, m, nil), "\n", ""); !strings.Contains(flat, last+" · ") {
		t.Fatal("确认页主 agent 一行丢了档位")
	}
}

// 嵌着长名字、长报错的提示整句显示：后半句的操作说明不被截掉（以前整句按 1000 字再截一次）。
func TestComposedMessagesKeepTheirTail(t *testing.T) {
	flat := func(v string) string { return strings.ReplaceAll(strings.ReplaceAll(v, "\n", ""), " ", "") }
	m, _ := n057Fixture()
	m.width, m.height = 80, 24
	m.msg = T("model.inuse.tier", "claude-opus-5-5", uniqueName(1024))
	if v := flat(seenAll(t, m, nil)); !strings.Contains(v, "先在项目里拿掉它，再改档位") {
		t.Fatal("页面底部的提示后半句被截掉")
	}

	m, api := n057Fixture()
	m.width, m.height = 100, 60
	api.tools[0].CanList = true
	goRow(t, m, rowGlobal, "claude", "")
	review2Key(m, "enter")
	plainView(m)
	_, slow := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("+")})
	api.modelsErr = errors.New(strings.Repeat("误", 1000))
	run(m, slow)
	if v := flat(seenAll(t, m, nil)); !strings.Contains(v, "：手填id") {
		t.Fatalf("「列不出模型…：手填 id」后半句被截掉（note 结尾 %q）", string([]rune(m.sub.input.note)[len([]rune(m.sub.input.note))-8:]))
	}
}

// 候选请求的先后：先发的那次失败、后发的那次成功，先发的晚回来也不占掉新输入框，新输入框显示后发那次的候选。
func TestModelListAnswersOnlyItsOwnInput(t *testing.T) {
	m, api := n057Fixture()
	m.width, m.height = 80, 24
	api.tools[0].CanList = true
	goRow(t, m, rowGlobal, "claude", "")
	review2Key(m, "enter")
	plainView(m)
	api.modelsErr = errors.New("context deadline exceeded")
	_, first := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("+")})
	review2Key(m, "esc")
	plainView(m)
	_, second := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("+")})
	firstMsg := first() // 先发的那次报错
	api.modelsErr = nil
	api.models["claude"] = []string{"claude-from-second"}
	secondMsg := second()
	m.Update(firstMsg) // 先发的先回来
	m.Update(secondMsg)
	v := plainView(m)
	if len(m.sub.input.all) != 1 || strings.Contains(v, "列不出") || !strings.Contains(v, "claude-from-second") {
		t.Fatalf("新输入框被上一次的结果占掉：all=%v note=%q\n%s", m.sub.input.all, m.sub.input.note, v)
	}
}

// 合规模型 id（含连字符、≤128 字）的候选随机测（20000 个，约 1 秒）：选中的候选折行后每段都不超宽、整段都带底色，选中块里是完整的 id；
// 没选中的行不带底色（attempt 3 自查 2 的探针收为回归测试：以前带连字符的 id 折出超宽的一段，到出口再折时丢了底色）。
func TestSelectedCandidateBlockHoldsWholeID(t *testing.T) {
	const cs = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._/:@+-"
	rng := rand.New(rand.NewSource(7))
	bad := 0
	for iter := 0; iter < 20000 && bad < 400; iter++ {
		w := 80 + rng.Intn(61)
		n := 1 + rng.Intn(128)
		b := make([]byte, n)
		for i := range b {
			if rng.Intn(6) == 0 {
				b[i] = "-./:"[rng.Intn(4)]
			} else {
				b[i] = cs[rng.Intn(len(cs))]
			}
		}
		id := string(b)
		if !strings.ContainsAny(id[:1], "abcdefghijklmnopqrstuvwxyz0123456789") {
			id = "x" + id[1:]
		}
		if sanitizeFull(id) != id {
			continue
		}
		m, _ := n057Fixture()
		m.width, m.height = w, 60
		goRow(t, m, rowGlobal, "claude", "")
		press(m, "enter")
		m.sub.input = &modelInput{cur: 0, limit: 12, all: []string{id, "other-model"}}
		if rng.Intn(2) == 0 {
			m.sub.input.all = []string{"claude-opus-5-5", id} // already-owned first
			m.sub.input.cur = 1
		}
		lines := m.fitLines(strings.Split(strings.TrimRight(m.inputView(), "\n"), "\n"))
		sel := -1
		for i, l := range lines {
			if ansi.StringWidth(l) > w {
				t.Errorf("w=%d id=%q line too wide %d", w, id, ansi.StringWidth(l))
				bad++
			}
			if strings.TrimSpace(ansi.Strip(l)) == "" {
				t.Errorf("w=%d id=%q blank line %d: %q", w, id, i, l)
				bad++
			}
			if strings.Contains(l, brand+"›") {
				sel = i
			}
		}
		if sel < 0 {
			t.Errorf("w=%d id=%q no selected line", w, id)
			bad++
			continue
		}
		got := ansi.Strip(lines[sel])
		j := sel + 1
		for ; j < len(lines) && strings.Contains(lines[j], selBg); j++ {
			got += ansi.Strip(lines[j])
		}
		if !strings.Contains(strings.ReplaceAll(got, " ", ""), id) {
			t.Errorf("w=%d id=%q selected bg block lacks id: %q next=%q", w, id, got, ansi.Strip(lines[min(j, len(lines)-1)]))
			bad++
		}
		// unselected lines must not carry selBg
		for k := j; k < len(lines); k++ {
			if strings.Contains(lines[k], selBg) {
				t.Errorf("w=%d id=%q stray selBg on line %d", w, id, k)
				bad++
			}
		}
	}
}
