package deckui

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// n059 评审 1（Codex gpt-6.1-sol）的探针，原样收进仓库作回归测试（只改了名字与注释），后面加 attempt 2 的补充。
type review1CASAPI struct{ *fakeSettingsAPI }

func (a *review1CASAPI) Save(_ context.Context, s deckstore.Settings) (int, error) {
	if s.Generation != a.set.Generation {
		return 0, &deckclient.Error{Status: 409, Message: "changed"}
	}
	if err := s.Validate(); err != nil {
		return 0, err
	}
	a.saved = append(a.saved, s)
	a.set, _ = cloneSettings(s)
	a.set.Generation += 1000
	return a.set.Generation, nil
}

func review1CASFixture() (*Settings, *review1CASAPI) {
	m, f := n057Fixture()
	a := &review1CASAPI{f}
	m.API = a
	return m, a
}

func TestReview1CASInterleavings(t *testing.T) {
	t.Run("reload_before_old_commit_then_other_window_commits", func(t *testing.T) {
		m, api := review1CASFixture()
		goRow(t, m, rowAgentPick, "codex", "")
		_, old := m.Update(tea.KeyMsg{Type: tea.KeySpace})
		press(m, "R", "R")
		if !picked(m.project(), "codex") {
			t.Fatal("R must discard the uncommitted uncheck")
		}
		_, restore := m.Update(old())
		if restore == nil {
			t.Fatal("old commit after reload must schedule restoration")
		}
		// Another window commits AFTER the old save and BEFORE the restoring save.
		other := &Settings{API: api, Installed: m.Installed}
		other.Update(other.load())
		goRow(t, other, rowTier, "", deckstore.Balanced)
		press(other, " ")
		foreign := api.set.Projects[0].Models[deckstore.Balanced].First()
		if foreign == "" {
			t.Fatal("second window did not commit")
		}
		run(m, restore)
		if !m.conflict || api.set.Projects[0].Models[deckstore.Balanced].First() != foreign {
			t.Fatal("restoration must conflict and preserve the other window")
		}
		n := len(api.saved)
		press(m, " ", "s")
		if len(api.saved) != n {
			t.Fatal("409 must stop all saves")
		}
	})
	t.Run("reload_sees_commit_before_reply", func(t *testing.T) {
		m, api := review1CASFixture()
		goRow(t, m, rowAgentPick, "codex", "")
		_, save := m.Update(tea.KeyMsg{Type: tea.KeySpace})
		reply := save() // Server committed, UI has not received the response yet.
		press(m, "R", "R")
		run(m, func() tea.Msg { return reply })
		if m.dirty || m.saving || m.conflict || m.set.Generation != api.set.Generation || len(api.saved) != 1 {
			t.Fatal("reload already saw commit; no compensating write is needed")
		}
	})
	t.Run("stale_409_after_reload_drains_new_edits", func(t *testing.T) {
		m, api := review1CASFixture()
		goRow(t, m, rowAgentPick, "codex", "")
		_, old := m.Update(tea.KeyMsg{Type: tea.KeySpace})
		// A manual/external change invalidates the outstanding snapshot.
		api.set.Projects[0].Envelope.TokenSoftCap = 1234567
		api.set.Generation += 1000
		press(m, "R", "R")
		goRow(t, m, rowTier, "", deckstore.Balanced)
		press(m, " ")
		if !m.pending {
			t.Fatal("edit during outstanding pre-reload save must queue")
		}
		_, next := m.Update(old())
		if next == nil || m.conflict {
			t.Fatal("stale 409 must drain post-reload edits without poisoning new state")
		}
		run(m, next)
		if m.dirty || m.conflict || api.set.Projects[0].Envelope.TokenSoftCap != 1234567 || len(api.set.Projects[0].Models[deckstore.Balanced]) == 0 {
			t.Fatal("external state and the post-reload edit must both survive")
		}
	})
	t.Run("rapid_edits_new_project_and_close_guard", func(t *testing.T) {
		m, api := review1CASFixture()
		goRow(t, m, rowAgentPick, "codex", "")
		_, first := m.Update(tea.KeyMsg{Type: tea.KeySpace})
		press(m, " ") // Queue a second change; the first snapshot must stay unchanged.
		m.Start = t.TempDir()
		press(m, "n", "enter", "enter")
		id := m.project().ID
		_, quit := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
		if quit != nil || !m.quitAsked {
			t.Fatal("closing during a save must ask for confirmation")
		}
		_, second := m.Update(first())
		if second == nil || len(api.saved[0].Projects) != 1 || picked(&api.saved[0].Projects[0], "codex") {
			t.Fatal("first snapshot was mutated, or pending save was lost")
		}
		if strings.Contains(m.msg, "按 m 起主 agent") {
			t.Fatal("new-project hint arrived before its own commit")
		}
		run(m, second)
		if len(api.saved) != 2 || len(api.set.Projects) != 2 || api.set.Projects[1].ID != id || !picked(&api.set.Projects[0], "codex") || m.dirty {
			t.Fatal("latest edits and the new project must both persist, in order")
		}
		if !strings.Contains(m.msg, "按 m 起主 agent") {
			t.Fatal("missing post-commit hint")
		}
	})
}

func TestReview1LongPathWidth(t *testing.T) {
	// Same legal directory string across machines: isolates the failure from t.TempDir's random suffix.
	dir := "/home/linux/.claude/jobs/85e39135/tmp/rv59a/.tmp/TestStandardSizesNothingTruncated2827734111/001/a-quite-long-directory-name-for-the-size-check/terminal-deck-worktrees-n049-a1-and-more"
	m, _ := n057Fixture()
	m.width, m.height = 100, 30
	m.form = &projectForm{dir: dir, subdirs: []string{"sub"}}
	v := plainView(m)
	for _, l := range strings.Split(v, "\n") {
		if w := ansi.StringWidth(l); w > m.width {
			t.Fatalf("100x30: directory row is %d cells (allowed 100): %q\n%s", w, l, v)
		}
	}
}

func TestReview1AllGlobalModelsReachable(t *testing.T) {
	m, _ := n057Fixture()
	m.width, m.height = 80, 24
	m.set.Agents = m.set.Agents[:1]
	a := &m.set.Agents[0]
	a.Models = nil
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("model-%02d-%s", i, strings.Repeat("x", 42))
		if !deckstore.ValidModelID(id) {
			t.Fatal("invalid fixture model")
		}
		a.Models = append(a.Models, deckstore.ModelDef{ID: id, Tier: deckstore.Frontier})
	}
	m.project().Models[deckstore.Frontier] = deckstore.ModelList{a.Models[0].ID}
	m.project().Agents = []string{"claude"}
	if err := m.set.Validate(); err != nil {
		t.Fatalf("invalid settings fixture: %v", err)
	}
	if err := m.set.CheckMainAgents(); err != nil {
		t.Fatalf("invalid main agent fixture: %v", err)
	}
	press(m, "2")
	seen := ""
	for i := 0; i < len(m.rows())+4; i++ {
		v := plainView(m)
		if len(strings.Split(v, "\n")) > 24 {
			t.Fatal("view exceeds height")
		}
		seen += v
		press(m, "down")
	}
	for i := 0; i < len(m.rows())+4; i++ {
		seen += plainView(m)
		press(m, "up")
	}
	var missing []string
	for _, md := range a.Models {
		if !strings.Contains(seen, md.ID) {
			missing = append(missing, md.ID)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("80x24: %d of 40 valid models can never be reached with up/down (agent has only one cursor row):\n%s", len(missing), strings.Join(missing, "\n"))
	}
}

func TestReview1LongModelListFailureNote(t *testing.T) {
	m, _ := n057Fixture()
	m.width, m.height = 80, 24
	press(m, "a", "down", "enter", "+") // Add grok, then ask its CLI for models.
	// attempt 3：只有还在等候选时回来的结果才算数（迟到的不覆盖），这里先回到「正在问」的状态再让它报错
	gt, _ := m.toolInfo("grok")
	m.sub.input.loading, m.sub.input.note = true, T("input.asking", gt.Binary)
	m.Update(toolModelsMsg{tool: "grok", req: m.sub.input.req, err: errors.New(strings.Repeat("无法连接模型服务；", 80) + "请重试")})
	v := plainView(m)
	if n := len(strings.Split(v, "\n")); n > m.height {
		t.Fatalf("80x24: model-list error + input gives %d lines; input/return must fit:\n%s", n, v)
	}
	// attempt 2 补充：输入框看得见；报错全文（含最后的「请重试」）PgDn 翻得到，每屏都不超高、不超宽
	if !strings.Contains(v, "加模型：") {
		t.Fatalf("底部放不下时从输入框看起：\n%s", v)
	}
	seen := strings.ReplaceAll(strings.ReplaceAll(v, "\n", ""), " ", "")
	for i := 0; i < 10; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		v = plainView(m)
		for _, l := range strings.Split(v, "\n") {
			if ansi.StringWidth(l) > m.width {
				t.Fatalf("PgDn 后超宽：%q", l)
			}
		}
		if n := len(strings.Split(v, "\n")); n > m.height {
			t.Fatalf("PgDn 后超高 %d 行", n)
		}
		seen += strings.ReplaceAll(strings.ReplaceAll(v, "\n", ""), " ", "")
	}
	if !strings.Contains(seen, "请重试") || !strings.Contains(seen, "esc取消") {
		t.Fatalf("报错全文与按键说明 PgDn 翻得到：%s", seen)
	}
	press(m, "x") // 打字照常进输入框，翻页清零
	if m.sub.input == nil || !strings.HasSuffix(m.sub.input.text, "x") || m.scr.off != 0 {
		t.Fatalf("翻页后打字照常：%+v off=%d", m.sub.input, m.scr.off)
	}
}

func TestReview1MainMovedText(t *testing.T) {
	m, _ := n057Fixture()
	goRow(t, m, rowTier, "", deckstore.Frontier)
	press(m, "enter", "enter")
	if strings.Contains(m.msg, "%!") {
		t.Fatalf("normal removal of the main model produces broken hint: %q", m.msg)
	}
}

func TestReview1WrapDiagnostics(t *testing.T) {
	m, _ := n057Fixture()
	m.width = 100
	for n := 80; n <= 200; n++ {
		path := "/projects/" + strings.Repeat("a", n) + "-bbb-ccc"
		got := m.fitWidth(" " + bold + path + reset)
		for _, line := range strings.Split(got, "\n") {
			if ansi.StringWidth(line) > m.width {
				t.Fatalf("minimal n=%d: input=%q output=%q width=%d", n, " "+bold+path+reset, got, ansi.StringWidth(line))
			}
		}
	}
}

// attempt 2：出口折行对各种长度与分隔符的长词都不超宽、不丢字、不多出空行（评审 1 B1 的确定性版本，不依赖临时目录名长度）。
func TestFitWidthNeverOverflows(t *testing.T) {
	m, _ := n057Fixture()
	for _, w := range []int{60, 80, 100, 140} {
		m.width = w
		for n := 1; n <= 260; n++ {
			for _, sep := range []string{"-bbb-ccc", "_bbb_ccc", " bbb ccc", "/bbb/ccc", "中文目录名", "-", "", " -c x", "  --flag", "   -"} {
				word := "/projects/" + strings.Repeat("a", n) + sep
				for _, in := range []string{" " + bold + word + reset, "   目录      " + word, word, " " + dimOn + "● " + word + "  1.0" + reset} {
					got := m.fitWidth(in)
					var parts []string
					for _, l := range strings.Split(got, "\n") {
						if ansi.StringWidth(l) > w {
							t.Fatalf("w=%d n=%d sep=%q：超宽 %d：%q", w, n, sep, ansi.StringWidth(l), l)
						}
						if ansi.StringWidth(in) > w && strings.TrimSpace(ansi.Strip(l)) == "" {
							t.Fatalf("w=%d n=%d sep=%q：折出空行：%q", w, n, sep, got)
						}
						parts = append(parts, ansi.Strip(l))
					}
					if strings.ReplaceAll(strings.Join(parts, ""), " ", "") != strings.ReplaceAll(ansi.Strip(in), " ", "") {
						t.Fatalf("w=%d n=%d sep=%q：折行丢字：%q", w, n, sep, got)
					}
				}
			}
		}
	}
}

// attempt 2：块比一屏还高时 ↑↓ 先在块内翻（一次大半屏）、翻到头才移光标；往回也一样；回到这一行时从块顶看起。
// 真实程序每按一次键都会重画，这里也每次先画（块高是画的时候量出来的）。
func TestTallBlockScrollsBeforeCursorMoves(t *testing.T) {
	m, _ := n057Fixture()
	m.width, m.height = 80, 24
	a := m.agentPtr("claude")
	for i := 0; i < 40; i++ {
		a.Models = append(a.Models, deckstore.ModelDef{ID: fmt.Sprintf("model-%02d-%s", i, strings.Repeat("x", 42)), Tier: deckstore.Frontier})
	}
	key := func(k string) string { press(m, k); return plainView(m) }
	v := key("2")
	if m.cur != 0 || m.scr.blockMax == 0 {
		t.Fatalf("前提：光标在 claude、块比一屏高：cur=%d %+v", m.cur, m.scr)
	}
	seen := v
	for i := 0; m.scr.blockOff < m.scr.blockMax; i++ {
		if i > 3 {
			t.Fatalf("块内翻页太慢：%+v", m.scr)
		}
		v = key("down")
		seen += v
		if m.cur != 0 {
			t.Fatal("块没翻完就移了光标")
		}
		if !strings.Contains(v, "› ● Claude Code") {
			t.Fatalf("块内翻页时光标行固定在窗口顶上（attempt 2 自查）：\n%s", v)
		}
		if n := len(strings.Split(v, "\n")); n > 24 {
			t.Fatalf("超高 %d", n)
		}
	}
	for i := 0; i < 40; i++ {
		if id := fmt.Sprintf("model-%02d-", i); !strings.Contains(seen, id) {
			t.Fatalf("块内翻页漏了 %s", id)
		}
	}
	key("down")
	if m.cur != 1 || m.scr.blockOff != 0 {
		t.Fatalf("块翻完再按 ↓ 才移到下一行：cur=%d %+v", m.cur, m.scr)
	}
	if v = key("up"); m.cur != 0 || !strings.Contains(v, "model-00-") {
		t.Fatalf("回到块上从块顶看起：cur=%d\n%s", m.cur, v)
	}
	key("down")
	key("up")
	if m.cur != 0 || m.scr.blockOff != 0 {
		t.Fatalf("块内往回翻：cur=%d %+v", m.cur, m.scr)
	}
}

// attempt 2：项目页某一档清单很长（块比一屏高），块内翻到一半按 → 换到另一个同样长的项目：从新项目这一块的顶上看起。
func TestTallBlockOffsetResetsOnProjectSwitch(t *testing.T) {
	m, _ := n057Fixture()
	m.width, m.height = 80, 24
	c := m.agentPtr("claude")
	var ids deckstore.ModelList
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("tall-%02d-%s", i, strings.Repeat("y", 40))
		c.Models = append(c.Models, deckstore.ModelDef{ID: id, Tier: deckstore.Economy})
		ids = append(ids, id)
	}
	m.project().Models[deckstore.Economy] = ids
	second := *m.project()
	second.ID, second.Name, second.Path = "second", "second", "/tmp/second"
	second.Models = map[deckstore.Tier]deckstore.ModelList{deckstore.Frontier: m.project().Models[deckstore.Frontier], deckstore.Economy: ids}
	m.set.Projects = append(m.set.Projects, second)
	goRow(t, m, rowTier, "", deckstore.Economy)
	plainView(m)
	if m.scr.blockMax == 0 {
		t.Fatalf("前提：便宜档这一块比一屏高：%+v", m.scr)
	}
	press(m, "down")
	plainView(m)
	if m.scr.blockOff == 0 {
		t.Fatal("前提：块内翻了")
	}
	press(m, "right")
	if v := plainView(m); m.proj != 1 || m.scr.blockOff != 0 || !strings.Contains(strings.ReplaceAll(v, " ", ""), "便宜tall-00-") {
		t.Fatalf("换项目后从这一块的顶上看起：proj=%d %+v\n%s", m.proj, m.scr, v)
	}
}

// attempt 2：文字表每条的格式占位符个数与调用时给的参数个数一致（评审 1 已知问题 1：main.moved 少一个参数）。
func TestTextFormatArgsMatch(t *testing.T) {
	verb := regexp.MustCompile(`%[-+# 0]*[0-9]*(\.[0-9]+)?[a-zA-Z]`)
	fset := token.NewFileSet()
	for _, name := range settingsUIFiles {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			x, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := x.Fun.(*ast.Ident); !ok || id.Name != "T" || len(x.Args) == 0 || x.Ellipsis.IsValid() {
				return true
			}
			lit, ok := x.Args[0].(*ast.BasicLit)
			if !ok {
				return true
			}
			key, _ := strconv.Unquote(lit.Value)
			want := len(verb.FindAllString(strings.ReplaceAll(textZH[key], "%%", ""), -1))
			if got := len(x.Args) - 1; got != want {
				t.Errorf("%s：T(%q) 给了 %d 个参数，文字里有 %d 个占位符", fset.Position(x.Pos()), key, got, want)
			}
			return true
		})
	}
}

// attempt 2 自查：底部放不下（很长的保存报错）时，↑↓ 选行仍看得见光标行、每按一次光标就动一次；报错全文 PgDn 翻得到。
func TestCursorStaysVisibleWhenFootOverflows(t *testing.T) {
	m, api := n057Fixture()
	m.width, m.height = 80, 24
	m.set.Agents = deckstore.DefaultAgents()
	long := strings.Repeat("保存失败的原因很长很长；", 70) + "最后一句" // 约 840 字：80×24 下底部放不下
	api.fail = errors.New(long)
	goRow(t, m, rowMain, "", "")
	press(m, " ")
	api.fail = nil
	m.cur = 0 // 回到第一行
	if v := plainView(m); !strings.Contains(v, "保存失败的原因") {
		t.Fatalf("前提：底部有长报错：\n%s", v)
	}
	for i := 0; i < 8; i++ {
		before := m.cur
		press(m, "down")
		v := plainView(m)
		if m.cur != before+1 {
			t.Fatalf("第 %d 次 ↓：光标要动一次（%d→%d）", i+1, before, m.cur)
		}
		if !strings.Contains(v, "›") || len(strings.Split(v, "\n")) > 24 {
			t.Fatalf("第 %d 次 ↓：看不见光标行或超高：\n%s", i+1, v)
		}
	}
	seen := ""
	for i := 0; i < 12; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		seen += strings.ReplaceAll(strings.ReplaceAll(plainView(m), "\n", ""), " ", "")
	}
	if !strings.Contains(seen, "最后一句") {
		t.Fatal("报错全文 PgDn 翻得到")
	}
}

// attempt 2 自查：PgUp/PgDn 与块内翻页像别的键一样，按了就取消「再按一次确认」。
func TestScrollingCancelsConfirm(t *testing.T) {
	m, _ := n057Fixture()
	m.width, m.height = 80, 24
	a := m.agentPtr("claude")
	for i := 0; i < 40; i++ {
		a.Models = append(a.Models, deckstore.ModelDef{ID: fmt.Sprintf("model-%02d-%s", i, strings.Repeat("x", 42)), Tier: deckstore.Frontier})
	}
	m.dirty = true
	press(m, "2")
	plainView(m)
	press(m, "R")
	if !m.reloadAsked {
		t.Fatal("前提：第一次 R 要确认")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.reloadAsked {
		t.Fatal("PgDn 后不再算确认")
	}
	press(m, "R")
	plainView(m)
	press(m, "down") // 块内翻页
	if m.cur != 0 || m.reloadAsked {
		t.Fatalf("块内 ↓ 后不再算确认：cur=%d asked=%v", m.cur, m.reloadAsked)
	}
}

// attempt 2 自查：正文窗口只剩 1 行时块内翻页不固定光标行，否则块里一行也看不到。
func TestTallBlockWithOneLineWindowShowsContent(t *testing.T) {
	body := "title\n\n" + selBg + " " + brand + "›" + reset + selBg + " head" + reset + "\n"
	for i := 0; i < 10; i++ {
		body += selBg + fmt.Sprintf("   c%d", i) + reset + "\n"
	}
	st := scrollState{blockOff: 3}
	v := ansi.Strip(fitHeightState(body, []string{"foot"}, 7, 1, &st))
	if st.show != 1 || !strings.Contains(v, "c2") {
		t.Fatalf("只剩 1 行时显示块里的内容：show=%d\n%s", st.show, v)
	}
}

// attempt 2 自查：数据（模型 id）里带「›」时不被当成光标行，窗口仍跟着真正的光标。
func TestCursorMarkInDataIsIgnored(t *testing.T) {
	m, _ := n057Fixture()
	m.width, m.height = 80, 24
	a := m.agentPtr("claude")
	for i := 0; i < 40; i++ {
		a.Models = append(a.Models, deckstore.ModelDef{ID: fmt.Sprintf("model-%02d", i), Tier: deckstore.Frontier})
	}
	a.Models = append(a.Models, deckstore.ModelDef{ID: "evil›model", Tier: deckstore.Frontier})
	goRow(t, m, rowGlobal, "claude", "")
	press(m, "enter")
	if v := plainView(m); m.sub == nil || m.sub.cur != 0 || !strings.Contains(v, "› claude-opus-5-5") {
		t.Fatalf("光标在第一个模型、窗口跟着它：\n%s", v)
	}
}

// attempt 2 自查：底部很长、整块一起滚时，加模型的候选照常列出（缩到 0 也放不下就不缩），PgDn 翻得到。
func TestCandidatesKeptWhenFootOverflows(t *testing.T) {
	m, _ := n057Fixture()
	m.width, m.height = 80, 24
	goRow(t, m, rowGlobal, "claude", "")
	press(m, "enter", "+")
	var cands []string
	for i := 0; i < 30; i++ {
		cands = append(cands, fmt.Sprintf("cand-%02d", i))
	}
	m.sub.input.all = cands
	m.saveErr = strings.Repeat("一时存不上的原因；", 70) + "到此为止"
	seen := ""
	for i := 0; i < 14; i++ {
		v := plainView(m)
		if len(strings.Split(v, "\n")) > 24 {
			t.Fatal("超高")
		}
		seen += strings.ReplaceAll(v, " ", "")
		m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if m.sub.input.limit == 0 || !strings.Contains(seen, "cand-00") || !strings.Contains(seen, "到此为止") {
		t.Fatalf("候选照常列出、报错全文翻得到：limit=%d", m.sub.input.limit)
	}
}
