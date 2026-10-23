package deckui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// n059 自查发现并修好的：重读之前发出的保存回来不算冲突；撤销确认认那一条；子视图里认 R；
// 「去侧栏按 m」只在新项目存上后说；存不上时 s 重试；子视图里看得见已保存；窄窗口不截；新建项目后光标。

// 重读之前发出的保存在重读之后 409 回来：不把刚读到的设置标成冲突，之后的改动照常自动保存。
func TestStaleSaveAfterReloadIsIgnored(t *testing.T) {
	m, api := settingsFixture()
	press(m, "down")
	_, save := m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // 勾 codex：保存发出，先不让它回来
	press(m, "R", "R")                                  // 有没存上的改动：第二次 R 才重读
	if m.reloads != 1 || m.conflict {
		t.Fatal("前提：已重读")
	}
	api.fail = &deckclient.Error{Status: 409, Message: "changed"}
	m.Update(save()) // 旧的那次 409 回来
	api.fail = nil
	if m.conflict || !m.saveErr.empty() {
		t.Fatalf("重读之前发出的保存失败不算数：conflict=%v %q", m.conflict, m.saveErr.String())
	}
	press(m, "enter")
	if len(api.saved) != 1 || m.dirty {
		t.Fatalf("重读后的改动照常自动保存：%d", len(api.saved))
	}
}

// 撤销授权的确认认的是那一条：两次 d 之间列表刷新、光标落到另一条上，第二次 d 只重新提醒。
func TestRevokeConfirmIsForThatGrant(t *testing.T) {
	m, api := settingsFixture()
	m.trust = append(m.trust, deckstore.TrustGrant{Project: "other", Tool: "codex", Dir: "/tmp/other", ApprovedAt: time.Now()})
	press(m, "3", "d")
	if !strings.Contains(m.msg.String(), "撤销 deck 的 codex") {
		t.Fatalf("第一次 d 提醒撤销 deck 的：%q", m.msg.String())
	}
	m.trust = m.trust[1:] // 列表变了：光标所在的那一条已是 other
	press(m, "d")
	if len(api.revoked) != 0 || !strings.Contains(m.msg.String(), "撤销 other 的 codex") {
		t.Fatalf("换了一条要重新确认：%v %q", api.revoked, m.msg.String())
	}
	press(m, "d")
	if len(api.revoked) != 1 || api.revoked[0] != "other/codex" {
		t.Fatalf("确认后撤销的是 other：%v", api.revoked)
	}
	// 授权列表刷新也清掉确认
	m.revokeAsked = &deckstore.TrustGrant{Project: "x", Tool: "codex"}
	m.Update(loadedMsg{trustOnly: true, trust: m.trust})
	if m.revokeAsked != nil {
		t.Fatal("授权列表刷新后要重新确认")
	}
}

// 子视图里冲突：提示让按 R，子视图里按 R（有没存上的改动时再按一次）就回到页面重读。
func TestReloadFromSubViewAfterConflict(t *testing.T) {
	m, api := n057Fixture()
	goRow(t, m, rowTier, "", deckstore.Frontier)
	press(m, "enter")
	api.fail = &deckclient.Error{Status: 409, Message: "changed"}
	press(m, "down", "enter")
	api.fail = nil
	if !m.conflict || !strings.Contains(plainView(m), "按 R 重新读取") {
		t.Fatalf("子视图里也看得见冲突提示：\n%s", plainView(m))
	}
	press(m, "R")
	if m.sub != nil || !strings.Contains(m.msg.String(), "再按一次 R") {
		t.Fatalf("子视图里按 R：回到页面、要再按一次：%q", m.msg.String())
	}
	press(m, "R")
	if m.conflict || m.dirty || !strings.Contains(m.msg.String(), "已重新读取") {
		t.Fatalf("重读后恢复：%v %q", m.conflict, m.msg.String())
	}
}

// 「去侧栏按 m」只在含新项目的那次保存存上后说；重读丢掉新项目时提示也清掉；建完不再说「正在保存」。
func TestNewProjectHintFollowsItsOwnSave(t *testing.T) {
	root := tree(t)
	m, api := settingsFixture()
	m.Start = filepath.Join(root, "ai")
	api.fail = &deckclient.Error{Status: 409, Message: "changed"}
	press(m, "down", "enter") // 先冲突
	press(m, "n", "down", "right", "enter", "enter")
	if strings.Contains(m.msg.String(), "正在保存") || m.hint.empty() {
		t.Fatalf("建好时不说正在保存：%q", m.msg.String())
	}
	api.fail = nil
	press(m, "R", "R")
	if !m.hint.empty() {
		t.Fatal("重读丢掉新项目时提示也清掉")
	}
	press(m, "down", "enter")
	if strings.Contains(m.msg.String(), "按 m 起主 agent") {
		t.Fatalf("新项目没存上就不该提示去侧栏：%q", m.msg.String())
	}
	// 正常建：含新项目的那次存上了才提示
	m2, _ := settingsFixture()
	m2.Start = filepath.Join(root, "ai")
	press(m2, "n", "down", "right", "enter", "enter")
	if !strings.Contains(m2.msg.String(), "按 m 起主 agent") {
		t.Fatalf("新项目存上后提示去侧栏：%q", m2.msg.String())
	}
	// 建项目时前一次保存还在路上：前一次（不含新项目）回来时不提示，含新项目的那次回来才提示
	m3, _ := settingsFixture()
	m3.Start = filepath.Join(root, "ai")
	press(m3, "down")
	_, first := m3.Update(tea.KeyMsg{Type: tea.KeyEnter}) // 勾 codex：保存发出，先不让它回来
	press(m3, "n", "down", "right", "enter", "enter")     // 建项目：保存在路上，记下回来再存
	_, second := m3.Update(first())
	if strings.Contains(m3.msg.String(), "按 m 起主 agent") || second == nil {
		t.Fatalf("不含新项目的那次保存回来时不该提示：%q", m3.msg.String())
	}
	m3.Update(second())
	if !strings.Contains(m3.msg.String(), "按 m 起主 agent") {
		t.Fatalf("含新项目的那次存上后提示：%q", m3.msg.String())
	}
}

// 存不上（不是冲突）：提示写明 s 重试；按 s 重试成功；平时 s 什么也不做。
func TestRetryAfterTransientFailure(t *testing.T) {
	m, api := settingsFixture()
	press(m, "s")
	if len(api.saved) != 0 {
		t.Fatal("平时 s 不保存（改动自动保存）")
	}
	api.fail = errors.New("连不上 deckd")
	press(m, "down", "enter")
	if !strings.Contains(plainView(m), "s 重试") {
		t.Fatalf("存不上时写明怎么重试：\n%s", plainView(m))
	}
	api.fail = nil
	press(m, "tab", "s")
	if m.dirty || len(api.saved) != 1 || !picked(m.project(), "codex") {
		t.Fatalf("s 重试存上：dirty=%v %d", m.dirty, len(api.saved))
	}
}

// 子视图里也看得见保存状态；全局没有 agent 时项目页指出下一步。
func TestSubViewShowsSavedAndEmptyAgentsHint(t *testing.T) {
	m, _ := n057Fixture()
	goRow(t, m, rowTier, "", deckstore.Frontier)
	press(m, "enter", "down", "enter")
	if !strings.Contains(plainView(m), "✓ 已保存") {
		t.Fatalf("子视图里看得见已保存：\n%s", plainView(m))
	}
	press(m, "esc")
	m.set.Agents = nil
	if !strings.Contains(plainView(m), "Tab 到「全局 agent」页按 a") {
		t.Fatalf("全局没有 agent 时项目页指出下一步：\n%s", plainView(m))
	}
}

// 窄窗口（60 列）：各页与子视图的每一行都不超宽，长内容（多选清单、长目录、没装、很长的模型 id、冲突提示）折行或截短而不丢关键字。
func TestNarrowWindowNothingOverflows(t *testing.T) {
	m, _ := n057Fixture()
	m.width, m.height = 60, 60
	m.set.Agents = deckstore.DefaultAgents()
	m.Installed = func(b string) bool { return b != "kimi" }
	m.project().Agents = []string{"claude", "codex", "opencode"}
	m.project().Models[deckstore.Frontier] = deckstore.ModelList{"claude-opus-5-5", "gpt-6.1-sol", "opencode/muse-spark-1.3-contributor-free"}
	m.trust = []deckstore.TrustGrant{{Project: "deck", Tool: "codex", Dir: "/home/linux/ai/05.code/github/Tingschen287/terminal-deck/work", ApprovedAt: time.Now()}}
	m.conflict, m.saveErr = true, lit(T("save.conflict"))
	check := func(what string, must ...string) {
		t.Helper()
		v := plainView(m)
		for _, l := range strings.Split(v, "\n") {
			if ansi.StringWidth(l) > 60 {
				t.Fatalf("%s：超宽 %d：%q", what, ansi.StringWidth(l), l)
			}
		}
		flat := strings.ReplaceAll(strings.ReplaceAll(v, "\n", ""), " ", "")
		for _, w := range must {
			if !strings.Contains(flat, strings.ReplaceAll(w, " ", "")) {
				t.Fatalf("%s：缺 %q：\n%s", what, w, v)
			}
		}
	}
	check("项目页", "opencode/muse-spark-1.3-contributor-free", "按R重新读取")
	press(m, "tab")
	check("全局 agent 页", "（没装）", "opencode/muse-spark-1.3-contributor-free")
	press(m, "tab")
	check("授权页", "deck · codex")
	press(m, "2", "enter")
	check("agent 编辑", "esc 返回", "按R重新读取")
	press(m, "esc")
}

// 从全局 agent 页新建项目：建好后项目页光标在新项目第一行，全局 agent 页的光标不被清零；确认页的标签按宽度对齐。
func TestNewProjectCursorsAndLabels(t *testing.T) {
	root := tree(t)
	m, _ := settingsFixture()
	m.Start = filepath.Join(root, "ai")
	press(m, "down", "down", "down", "tab", "down")
	press(m, "n", "down", "right", "enter")
	v := plainView(m)
	col := -1
	for _, l := range strings.Split(v, "\n") {
		for _, lab := range []string{"目录", "项目 id", "agent", "主 agent"} {
			if strings.HasPrefix(strings.TrimLeft(l, " "), lab+" ") {
				c := ansi.StringWidth(l) - ansi.StringWidth(strings.TrimLeft(strings.TrimPrefix(strings.TrimLeft(l, " "), lab), " "))
				if col >= 0 && c != col {
					t.Fatalf("确认页的值没对齐：\n%s", v)
				}
				col = c
			}
		}
	}
	press(m, "enter")
	if m.page != pageProject || m.cur != 0 || m.curs[pageAgents] != 1 {
		t.Fatalf("建好后：项目页光标在第一行、全局 agent 页光标留着：page=%d cur=%d agents=%d", m.page, m.cur, m.curs[pageAgents])
	}
}

// 80×24 及以上（验收）：各页、子视图、加入页与新建项目两页每行不超宽、总行数不超高、不截短；
// 超过 80 个字的授权目录与 CLI 路径、很多项目在用同一个模型、很长的新建目录都折到下一行（n059 自查 1、2）。
func TestStandardSizesNothingTruncated(t *testing.T) {
	long := "/home/linux/ai/05.code/github/Tingschen287/terminal-deck/.claude/worktrees/n049-a1/work" // 88 个字
	newDir := filepath.Join(t.TempDir(), "a-quite-long-directory-name-for-the-size-check", "terminal-deck-worktrees-n049-a1-and-more")
	if err := os.MkdirAll(filepath.Join(newDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, size := range [][2]int{{80, 24}, {100, 30}, {140, 40}} {
		m, api := n057Fixture()
		m.width, m.height = size[0], size[1]
		m.Start = newDir
		m.project().Agents = []string{"claude", "codex"}
		m.project().Models[deckstore.Frontier] = deckstore.ModelList{"claude-opus-5-5", "gpt-6.1-sol"}
		for _, id := range []string{"finance-reconcile-ai", "hr-resume-screen", "terminal-deck-main"} {
			m.set.Projects = append(m.set.Projects, deckstore.ProjectDef{ID: id, Name: id, Path: "/tmp/" + id, Agents: []string{"claude"},
				Models:    map[deckstore.Tier]deckstore.ModelList{deckstore.Frontier: {"claude-opus-5-5"}},
				MainAgent: deckstore.MainAgent{Agent: "claude", Tier: deckstore.Frontier}, Envelope: deckstore.DefaultEnvelope(id)})
		}
		m.trust = []deckstore.TrustGrant{{Project: "deck", Tool: "codex", Dir: long, ApprovedAt: time.Now()}}
		for i := range api.tools {
			api.tools[i].Path = "/home/linux/.local/share/some-very-long-install-prefix/with-another-level/bin/" + api.tools[i].Binary
		}
		check := func(what string, must ...string) {
			t.Helper()
			v := plainView(m)
			ls := strings.Split(v, "\n")
			if len(ls) > size[1] {
				t.Fatalf("%dx%d %s：超高 %d 行：\n%s", size[0], size[1], what, len(ls), v)
			}
			for _, l := range ls {
				if ansi.StringWidth(l) > size[0] || strings.Contains(l, "…") {
					t.Fatalf("%dx%d %s：超宽或截短：%q", size[0], size[1], what, l)
				}
			}
			flat := strings.ReplaceAll(strings.ReplaceAll(v, "\n", ""), " ", "")
			for _, w := range must {
				if !strings.Contains(flat, w) {
					t.Fatalf("%dx%d %s：缺 %q：\n%s", size[0], size[1], what, w, v)
				}
			}
		}
		check("项目页", "q关闭")
		press(m, "3")
		check("授权页", long)
		press(m, "2", "enter")
		check("agent 编辑", "esc返回", "项目在用：ai、finance-reconcile-ai、hr-resume-screen、terminal-deck-main")
		press(m, "+")
		check("输入模型 id", "esc取消")
		press(m, "esc", "esc", "a")
		check("加入", "with-another-level/bin/grok", "esc返回")
		press(m, "esc", "1")
		goRow(t, m, rowTier, "", deckstore.Frontier)
		press(m, "enter")
		check("档位多选", "esc返回")
		press(m, "esc", "n")
		check("新建项目 · 选目录", newDir, "sub/")
		press(m, "enter")
		check("新建项目 · 确认", newDir)
		press(m, "esc", "esc")
	}
}

// 子视图里存不上：底部让按 s 重试，子视图里按 s 就重试；重试在路上时子视图与页面一样说「保存中」（n059 自查 2）。
func TestSubViewRetryAndSavingStatus(t *testing.T) {
	m, api := n057Fixture()
	goRow(t, m, rowTier, "", deckstore.Frontier)
	press(m, "enter")
	api.fail = errors.New("deckd down")
	press(m, "down", "enter")
	if !strings.Contains(plainView(m), "s 重试") || m.sub == nil {
		t.Fatalf("子视图里写明 s 重试：\n%s", plainView(m))
	}
	api.fail = nil
	n := len(api.saved)
	_, retry := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if retry == nil || !m.saving || m.sub == nil {
		t.Fatalf("子视图里按 s 重试：saving=%v sub=%v", m.saving, m.sub != nil)
	}
	if v := plainView(m); !strings.Contains(v, "保存中") || strings.Contains(v, "没存上") {
		t.Fatalf("重试在路上：子视图说保存中，不再说没存上：\n%s", v)
	}
	m.Update(retry())
	if len(api.saved) != n+1 || m.dirty || !m.saveErr.empty() || !strings.Contains(plainView(m), "✓ 已保存") {
		t.Fatalf("重试存上：%d dirty=%v %q", len(api.saved)-n, m.dirty, m.saveErr.String())
	}
}

// 重读读在路上那次保存存上之前、那次保存在重读之后成功回来：把画面上的再存一次，deckd 与画面一致（n059 自查 2：以前只跟上代数）。
func TestSaveLandingAfterEarlierReloadIsOverwrittenByScreen(t *testing.T) {
	m, api := settingsFixture()
	press(m, "down")
	_, save := m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // 勾 codex：保存发出，先不让它回来
	press(m, "R", "R")                                  // 重读读到的是它存上之前的（假 deckd 不写回）
	if picked(m.project(), "codex") {
		t.Fatal("前提：重读后画面上没勾 codex")
	}
	_, again := m.Update(save()) // 它在重读之后成功回来
	if again == nil {
		t.Fatal("要把画面上的再存一次")
	}
	m.Update(again())
	last := api.saved[len(api.saved)-1]
	if len(api.saved) != 2 || picked(&last.Projects[0], "codex") || m.dirty || m.conflict {
		t.Fatalf("deckd 与画面一致：%d codex=%v dirty=%v", len(api.saved), picked(&last.Projects[0], "codex"), m.dirty)
	}
	if last.Generation != api.saved[0].Generation+1000 {
		t.Fatalf("再存带它存下的代数：%d", last.Generation)
	}
}

// 新建项目那次保存一时存不上：提示留着，s 重试存上后照样提示去侧栏（n059 自查 2）。
func TestNewProjectHintSurvivesTransientFailure(t *testing.T) {
	root := tree(t)
	m, api := settingsFixture()
	m.Start = filepath.Join(root, "ai")
	api.fail = errors.New("deckd down")
	press(m, "n", "down", "right", "enter", "enter")
	if m.hint.empty() || strings.Contains(m.msg.String(), "按 m 起主 agent") {
		t.Fatalf("没存上：不提示去侧栏、提示留着：%q %q", m.msg.String(), m.hint.String())
	}
	api.fail = nil
	press(m, "s")
	if !strings.Contains(m.msg.String(), "按 m 起主 agent") {
		t.Fatalf("重试存上后提示去侧栏：%q", m.msg.String())
	}
}

// 长内容扫一遍（n059 自查 3）：很多长名字的项目在用同一个模型、很长的模型 id 与改名输入框、很长的授权目录与 CLI 路径、
// 很长的新建目录与很多子目录、表单报错——80×24 及以上每个画面都不超宽、不超高、不截短（「……」是省略号文字，不算），标题与关键内容都在。
func TestLongContentSweepNothingTruncated(t *testing.T) {
	longID := "us.anthropic.claude-sonnet-4-5-20250929-v1:0"
	longDir := "/home/linux/ai/05.code/github/Tingschen287/terminal-deck/.claude/worktrees/n049-a1/work"
	newDir := filepath.Join(t.TempDir(), "a-quite-long-directory-name-for-the-size-check", "terminal-deck-worktrees-n049-a1-and-more")
	for i := 0; i < 20; i++ {
		if err := os.MkdirAll(filepath.Join(newDir, fmt.Sprintf("subdirectory-with-a-long-name-%02d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	names := []string{"finance-reconcile-ai", "hr-resume-screen", "contract-review-ai", "terminal-deck-main", "data-platform-ontology", "infra-visible-agent-orchestration"}
	for _, size := range [][2]int{{80, 24}, {100, 30}, {140, 40}} {
		m, api := n057Fixture()
		m.width, m.height = size[0], size[1]
		m.Start = newDir
		for _, id := range names {
			m.set.Projects = append(m.set.Projects, deckstore.ProjectDef{ID: id, Name: id, Path: "/tmp/" + id, Agents: []string{"claude"},
				Models:    map[deckstore.Tier]deckstore.ModelList{deckstore.Frontier: {"claude-opus-5-5"}},
				MainAgent: deckstore.MainAgent{Agent: "claude", Tier: deckstore.Frontier}, Envelope: deckstore.DefaultEnvelope(id)})
		}
		m.set.Projects[len(m.set.Projects)-1].Path = newDir // 选这个目录会报「已经是项目」
		c := m.agentPtr("claude")
		c.Models = append([]deckstore.ModelDef{{ID: longID, Tier: deckstore.Balanced}}, c.Models...)
		m.trust = []deckstore.TrustGrant{{Project: "infra-visible-agent-orchestration", Tool: "codex", Dir: longDir, ApprovedAt: time.Now()}}
		for i := range api.tools {
			api.tools[i].Path = "/home/linux/.local/share/some-very-long-install-prefix/with-another-level/bin/" + api.tools[i].Binary
		}
		check := func(what, head string, must ...string) {
			t.Helper()
			v := plainView(m)
			ls := strings.Split(v, "\n")
			if len(ls) > size[1] {
				t.Fatalf("%dx%d %s：超高 %d 行：\n%s", size[0], size[1], what, len(ls), v)
			}
			if !strings.Contains(ls[0], head) {
				t.Fatalf("%dx%d %s：第一行不是标题 %q：\n%s", size[0], size[1], what, head, v)
			}
			for _, l := range ls {
				if ansi.StringWidth(l) > size[0] || strings.Contains(strings.ReplaceAll(l, "……", ""), "…") {
					t.Fatalf("%dx%d %s：超宽或截短：%q\n%s", size[0], size[1], what, l, v)
				}
			}
			flat := strings.ReplaceAll(strings.ReplaceAll(v, "\n", ""), " ", "")
			for _, w := range must {
				if !strings.Contains(flat, strings.ReplaceAll(w, " ", "")) {
					t.Fatalf("%dx%d %s：缺 %q：\n%s", size[0], size[1], what, w, v)
				}
			}
		}
		check("项目页", "设置", "q 关闭")
		press(m, "3")
		check("授权页", "设置", longDir)
		press(m, "2", "enter")
		check("agent 编辑", "Claude Code", "esc 返回", longID, "项目在用：ai、contract-review-ai、data-platform-ontology、finance-reconcile-ai、hr-resume-screen、infra-visible-agent-orchestration、terminal-deck-main")
		press(m, "e")
		check("改长模型 id", "Claude Code", "esc 返回", "改 "+longID+" 的 id："+longID+"▏", "esc 取消")
		press(m, "esc", "+")
		m.sub.input.all = names
		check("加模型", "Claude Code", "esc 返回", "esc 取消")
		press(m, "esc", "esc", "a")
		check("加入", "加入本机的 agent", "with-another-level/bin/grok", "esc 返回")
		press(m, "esc", "1")
		goRow(t, m, rowTier, "", deckstore.Balanced)
		press(m, "enter")
		check("档位多选", "日常", "esc 返回")
		press(m, "esc", "n")
		check("新建项目 · 选目录", "新建项目", newDir, "subdirectory-with-a-long-name-00/")
		press(m, "down", "down", "down", "down", "down", "down", "down", "down", "down", "down", "down", "down", "down", "down", "down", "down", "down", "down")
		check("新建项目 · 选目录（滚到下面）", "新建项目", "subdirectory-with-a-long-name-17/")
		press(m, "up", "up", "up", "up", "up", "up", "up", "up", "up", "up", "up", "up", "up", "up", "up", "up", "up", "up", "enter")
		check("新建项目 · 已是项目", "新建项目", newDir, "infra-visible-agent-orchestration")
		m.form.err = lit("读不了 " + newDir + "/subdirectory-with-a-long-name-03：open " + newDir + "/subdirectory-with-a-long-name-03: permission denied")
		check("新建项目 · 读不了", "新建项目", "permission denied")
		m.form.err = nil
		press(m, "esc")
	}
}

// 长内容再扫一遍（n059 自查 4）：读不到设置与保存失败的长报错（后半句「先运行 deck daemon」要看得见）、合规的 99 字模型 id
// （项目页、全局 agent 页、agent 编辑、档位多选、输入框里打的字）、很长的候选（个数按折行后的行数缩减）、项目页滚动时项目名固定在顶部、
// 没有连字符的超长目录——80×24 及以上不超宽、不超高、不截短。
func TestLongContentSweepMore(t *testing.T) {
	arn := "arn:aws:bedrock:us-east-1:123456789012:application-inference-profile/us.anthropic.claude-sonnet-4-5"
	if !deckstore.ValidModelID(arn) {
		t.Fatal("前提：99 字的 ARN 是合规的模型 id")
	}
	dial := "连不上 deckd（Get \"http://deckd/v1/settings\": dial unix /home/linux/.local/state/deck/deckd.sock: connect: no such file or directory）：先运行 deck daemon"
	under := filepath.Join(t.TempDir(), "Tingschen287", "terminal_deck_worktrees_n049_a1_work_internal_deckui_settings_and_more_words")
	if err := os.MkdirAll(filepath.Join(under, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, size := range [][2]int{{80, 24}, {100, 30}, {140, 40}} {
		check := func(m *Settings, what, head string, must ...string) {
			t.Helper()
			v := plainView(m)
			ls := strings.Split(v, "\n")
			if len(ls) > size[1] {
				t.Fatalf("%dx%d %s：超高 %d 行：\n%s", size[0], size[1], what, len(ls), v)
			}
			if !strings.Contains(ls[0], head) {
				t.Fatalf("%dx%d %s：第一行不是标题 %q：\n%s", size[0], size[1], what, head, v)
			}
			for _, l := range ls {
				if ansi.StringWidth(l) > size[0] || strings.Contains(strings.ReplaceAll(l, "……", ""), "…") {
					t.Fatalf("%dx%d %s：超宽或截短：%q\n%s", size[0], size[1], what, l, v)
				}
			}
			flat := strings.ReplaceAll(strings.ReplaceAll(v, "\n", ""), " ", "")
			for _, w := range must {
				if !strings.Contains(flat, strings.ReplaceAll(w, " ", "")) {
					t.Fatalf("%dx%d %s：缺 %q：\n%s", size[0], size[1], what, w, v)
				}
			}
		}
		// 读不到设置
		m0 := &Settings{width: size[0], height: size[1]}
		m0.Update(loadedMsg{err: errors.New(dial)})
		check(m0, "读不到设置", "设置", "先运行 deck daemon")

		m, api := n057Fixture()
		m.width, m.height = size[0], size[1]
		m.set.Agents = deckstore.DefaultAgents() // 6 个 agent：80×24 下项目页要滚动
		c := m.agentPtr("claude")
		c.Models = append(c.Models, deckstore.ModelDef{ID: arn, Tier: deckstore.Frontier})
		p := m.project()
		p.Agents = []string{"claude", "codex"}
		p.Models[deckstore.Frontier] = deckstore.ModelList{"claude-opus-5-5", arn, "gpt-6.1-sol", "claude-fable-5-1"}
		p.Models[deckstore.Balanced] = deckstore.ModelList{"claude-sonnet-5-5", "gpt-5.6-terra"}
		p.Models[deckstore.Economy] = deckstore.ModelList{"claude-haiku-5-5"}
		check(m, "项目页（长 id）", "设置", arn)
		goRow(t, m, rowTier, "", deckstore.Economy)
		check(m, "项目页（光标在便宜档）", "设置", "‹ ai ›", "便宜")
		// 保存失败的长报错
		api.fail = errors.New(dial)
		goRow(t, m, rowMain, "", "")
		press(m, " ") // 换主 agent：一处改动
		check(m, "保存失败", "设置", "先运行 deck daemon", "s 重试")
		api.fail = nil
		press(m, "s")
		press(m, "2")
		check(m, "全局 agent 页（长 id）", "设置", arn)
		goRow(t, m, rowGlobal, "claude", "")
		press(m, "enter")
		check(m, "agent 编辑（长 id）", "Claude Code", arn, "esc 返回")
		press(m, "+")
		var cands []string
		for i := 0; i < 20; i++ {
			cands = append(cands, fmt.Sprintf("openrouter/anthropic/claude-sonnet-4-5-variant-%02d-extended:free-and-something", i))
		}
		m.sub.input.all = cands
		check(m, "长候选", "Claude Code", "esc 返回", "esc 取消", cands[0])
		for _, r := range arn {
			press(m, string(r))
		}
		check(m, "输入框里打长 id", "Claude Code", "加模型："+arn+"▏", "esc 取消")
		press(m, "esc", "esc", "1")
		goRow(t, m, rowTier, "", deckstore.Frontier)
		press(m, "enter")
		check(m, "档位多选（长 id）", "最强", arn, "esc 返回")
		press(m, "esc")
		// 没有连字符的超长目录
		m.Start = under
		press(m, "n")
		check(m, "新建项目 · 选目录（无连字符）", "新建项目", under)
		press(m, "esc")
	}
}

// 每一行都滚得到（n059 自查 5）：光标在最后一行、这一行折出很多续行时，续行也要在窗口里——
// 便宜档放 9 个长 id、某个模型被 13 个项目在用，80×24 与 100×30 下从上到下移一遍光标，每个 id、每个项目名都出现过，且不超高。
func TestEveryWrappedLineReachable(t *testing.T) {
	var ids []string
	for i := 0; i < 9; i++ {
		ids = append(ids, fmt.Sprintf("us.anthropic.claude-haiku-4-5-202510%02d-v1:0", i))
	}
	var projects []string
	for i := 0; i < 13; i++ {
		projects = append(projects, fmt.Sprintf("project-with-a-longer-name-%02d", i))
	}
	for _, size := range [][2]int{{80, 24}, {100, 30}} {
		m, _ := n057Fixture()
		m.width, m.height = size[0], size[1]
		m.set.Agents = deckstore.DefaultAgents()
		c := m.agentPtr("claude")
		for _, id := range ids {
			c.Models = append(c.Models, deckstore.ModelDef{ID: id, Tier: deckstore.Economy})
		}
		last := c.Models[len(c.Models)-1].ID
		for _, id := range projects {
			m.set.Projects = append(m.set.Projects, deckstore.ProjectDef{ID: id, Name: id, Path: "/tmp/" + id, Agents: []string{"claude"},
				Models:    map[deckstore.Tier]deckstore.ModelList{deckstore.Economy: {last}},
				MainAgent: deckstore.MainAgent{Agent: "claude", Tier: deckstore.Economy}, Envelope: deckstore.DefaultEnvelope(id)})
		}
		p := m.project()
		for _, a := range m.set.Agents {
			p.Agents = append(p.Agents, a.ID)
		}
		p.Agents = p.Agents[1:] // n057Fixture 已勾 claude
		p.Agents = append([]string{"claude"}, p.Agents...)
		p.Models[deckstore.Economy] = append(deckstore.ModelList{}, ids...)
		m.saved = true
		sweep := func(what string, steps int) string {
			t.Helper()
			seen := ""
			for i := 0; i <= steps; i++ {
				v := plainView(m)
				if n := strings.Count(v, "\n") + 1; n > size[1] {
					t.Fatalf("%dx%d %s：超高 %d 行", size[0], size[1], what, n)
				}
				seen += strings.ReplaceAll(strings.ReplaceAll(v, "\n", ""), " ", "") + "|"
				press(m, "down")
			}
			return seen
		}
		seen := sweep("项目页", 20)
		for _, id := range ids {
			if !strings.Contains(seen, id) {
				t.Fatalf("%dx%d 项目页：%s 哪个光标位置都看不到", size[0], size[1], id)
			}
		}
		press(m, "2")
		goRow(t, m, rowGlobal, "claude", "")
		press(m, "enter")
		seen = sweep("agent 编辑", len(c.Models)+2)
		for _, id := range projects {
			if !strings.Contains(seen, id) {
				t.Fatalf("%dx%d agent 编辑：项目在用里的 %s 哪个光标位置都看不到", size[0], size[1], id)
			}
		}
		press(m, "esc")
	}
}
