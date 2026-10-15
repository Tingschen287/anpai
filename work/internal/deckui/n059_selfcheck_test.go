package deckui

import (
	"errors"
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
	if m.conflict || m.saveErr != "" {
		t.Fatalf("重读之前发出的保存失败不算数：conflict=%v %q", m.conflict, m.saveErr)
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
	if !strings.Contains(m.msg, "撤销 deck 的 codex") {
		t.Fatalf("第一次 d 提醒撤销 deck 的：%q", m.msg)
	}
	m.trust = m.trust[1:] // 列表变了：光标所在的那一条已是 other
	press(m, "d")
	if len(api.revoked) != 0 || !strings.Contains(m.msg, "撤销 other 的 codex") {
		t.Fatalf("换了一条要重新确认：%v %q", api.revoked, m.msg)
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
	if m.sub != nil || !strings.Contains(m.msg, "再按一次 R") {
		t.Fatalf("子视图里按 R：回到页面、要再按一次：%q", m.msg)
	}
	press(m, "R")
	if m.conflict || m.dirty || !strings.Contains(m.msg, "已重新读取") {
		t.Fatalf("重读后恢复：%v %q", m.conflict, m.msg)
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
	if strings.Contains(m.msg, "正在保存") || m.hint == "" {
		t.Fatalf("建好时不说正在保存：%q", m.msg)
	}
	api.fail = nil
	press(m, "R", "R")
	if m.hint != "" {
		t.Fatal("重读丢掉新项目时提示也清掉")
	}
	press(m, "down", "enter")
	if strings.Contains(m.msg, "按 m 起主 agent") {
		t.Fatalf("新项目没存上就不该提示去侧栏：%q", m.msg)
	}
	// 正常建：含新项目的那次存上了才提示
	m2, _ := settingsFixture()
	m2.Start = filepath.Join(root, "ai")
	press(m2, "n", "down", "right", "enter", "enter")
	if !strings.Contains(m2.msg, "按 m 起主 agent") {
		t.Fatalf("新项目存上后提示去侧栏：%q", m2.msg)
	}
	// 建项目时前一次保存还在路上：前一次（不含新项目）回来时不提示，含新项目的那次回来才提示
	m3, _ := settingsFixture()
	m3.Start = filepath.Join(root, "ai")
	press(m3, "down")
	_, first := m3.Update(tea.KeyMsg{Type: tea.KeyEnter}) // 勾 codex：保存发出，先不让它回来
	press(m3, "n", "down", "right", "enter", "enter")     // 建项目：保存在路上，记下回来再存
	_, second := m3.Update(first())
	if strings.Contains(m3.msg, "按 m 起主 agent") || second == nil {
		t.Fatalf("不含新项目的那次保存回来时不该提示：%q", m3.msg)
	}
	m3.Update(second())
	if !strings.Contains(m3.msg, "按 m 起主 agent") {
		t.Fatalf("含新项目的那次存上后提示：%q", m3.msg)
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
	m.conflict, m.saveErr = true, T("save.conflict")
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

// 80×24 及以上（验收）：各页、子视图与加入页每行不超宽、总行数不超高、不截短；很长的授权目录与 CLI 路径折到下一行（n059 自查）。
func TestStandardSizesNothingTruncated(t *testing.T) {
	long := "/home/linux/ai/05.code/github/Tingschen287/terminal-deck/work"
	for _, size := range [][2]int{{80, 24}, {100, 30}, {140, 40}} {
		m, api := n057Fixture()
		m.width, m.height = size[0], size[1]
		m.project().Agents = []string{"claude", "codex"}
		m.project().Models[deckstore.Frontier] = deckstore.ModelList{"claude-opus-5-5", "gpt-6.1-sol"}
		m.trust = []deckstore.TrustGrant{{Project: "deck", Tool: "codex", Dir: long, ApprovedAt: time.Now()}}
		for i := range api.tools {
			api.tools[i].Path = "/home/linux/.local/share/some-very-long-install-prefix/bin/" + api.tools[i].Binary
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
		check("agent 编辑", "esc返回")
		press(m, "+")
		check("输入模型 id", "esc取消")
		press(m, "esc", "esc", "a")
		check("加入", "some-very-long-install-prefix/bin/grok", "esc返回")
		press(m, "esc", "1")
		goRow(t, m, rowTier, "", deckstore.Frontier)
		press(m, "enter")
		check("档位多选", "esc返回")
		press(m, "esc")
	}
}
