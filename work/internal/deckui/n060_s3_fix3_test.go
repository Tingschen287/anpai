package deckui

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// n060 段 3 返工 3（Codex 复审 2 的 R2-2、R2-3、R2-4 与 R2-1 的两条界面路径）。
// 反例来自 4.eval/results/n060_ui_i18n/stage-3/review-2/audit_new_deckui_test.go，在这里改成正式测试。

// quietFromLangSync 取一次「语言变了」之后排出的静默重读命令（BatchMsg 的最后一项），不执行 tick，也先不交付回包。
func quietFromLangSync(t *testing.T, m *Settings) tea.Cmd {
	t.Helper()
	_, next := m.Update(langSyncedMsg{changed: true})
	if next == nil {
		t.Fatal("语言变了且没有未存的改动，应排出静默重读")
	}
	batch, ok := next().(tea.BatchMsg)
	if !ok {
		t.Fatalf("应是 tick + 静默重读：%T", next())
	}
	return batch[len(batch)-1]
}

// R2-2：静默重读的旧回包不能覆盖路上的新编辑（Codex 的 TestAuditS3R2QuietReloadOverwritesNewEdit）。
func TestN060S3Fix3QuietReloadNeverOverwritesNewEdit(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	for _, arrival := range []string{"during-save", "after-save"} {
		t.Run(arrival, func(t *testing.T) {
			i18n.SetLang(i18n.ZH)
			m, api := n057Fixture()
			m.set.UILang = "en"
			api.set.UILang = "en"
			old := quietFromLangSync(t, m)()
			gen := m.set.Generation
			m.sub = &subView{kind: subAgent, agent: "codex", cur: 0}
			before := m.agentPtr("codex").Models[0].Tier
			_, save := m.Update(tea.KeyMsg{Type: tea.KeySpace})
			edited := m.agentPtr("codex").Models[0].Tier
			if edited == before || save == nil || !m.saving || !m.dirty {
				t.Fatal("edit did not start autosave")
			}
			if arrival == "after-save" {
				run(m, save)
			}
			savedGen := m.set.Generation
			m.Update(old)
			if got := m.agentPtr("codex").Models[0].Tier; got != edited {
				t.Fatalf("旧回包把新编辑改回去了：改后 %s，回包之后 %s", edited, got)
			}
			if m.set.Generation != savedGen {
				t.Errorf("旧回包回滚了代数：%d → %d（起点 %d）", savedGen, m.set.Generation, gen)
			}
			switch arrival {
			case "during-save":
				if !m.dirty || !m.saving {
					t.Errorf("保存在路上，旧回包不该清掉 dirty：dirty=%v saving=%v", m.dirty, m.saving)
				}
				// 保存在路上收到 409：owner 刚改的值还在本地，等 R 处理
				m.saveDone(doneMsg{save: true, gen: gen, edits: m.edits, reloads: m.reloads, err: &deckclient.Error{Status: 409, Message: "changed"}})
				if got := m.agentPtr("codex").Models[0].Tier; got != edited || !m.conflict || !m.dirty {
					t.Errorf("409 之后新编辑应还在本地：tier=%s conflict=%v dirty=%v", got, m.conflict, m.dirty)
				}
			case "after-save":
				if m.dirty || !m.saved {
					t.Errorf("保存已成功：dirty=%v saved=%v", m.dirty, m.saved)
				}
			}
		})
	}
}

// R2-2：回包与编辑、保存、强制重读交错的其他顺序。
func TestN060S3Fix3QuietReloadDiscardedOnOtherInterleavings(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	fresh := func() (*Settings, *fakeSettingsAPI, tea.Msg) {
		i18n.SetLang(i18n.ZH)
		m, api := n057Fixture()
		m.set.UILang = "en"
		api.set.UILang = "en"
		return m, api, quietFromLangSync(t, m)()
	}
	// 对照：期间什么也没发生，回包照常生效（让界面语言行和代数跟上）
	t.Run("control-accepted", func(t *testing.T) {
		m, api, old := fresh()
		api.set.Generation = 9
		api.set.UILang = "zh"
		old = quietFromLangSync(t, m)()
		m.Update(old)
		if m.set.Generation != 9 || m.set.UILang != "zh" {
			t.Fatalf("没有交错时静默重读应生效：gen=%d lang=%q", m.set.Generation, m.set.UILang)
		}
	})
	// 回包前 owner 编辑了、保存已经成功，之后又有第二个静默重读回包
	t.Run("edit-then-save-done-then-late-reply", func(t *testing.T) {
		m, _, old := fresh()
		m.sub = &subView{kind: subAgent, agent: "codex", cur: 0}
		_, save := m.Update(tea.KeyMsg{Type: tea.KeySpace})
		run(m, save)
		edited := m.agentPtr("codex").Models[0].Tier
		m.Update(old)
		if m.agentPtr("codex").Models[0].Tier != edited {
			t.Fatal("保存成功之后到的旧回包覆盖了新值")
		}
	})
	// 没有任何编辑，但手动 s 重试了一次保存（saveErr 之后）：保存在路上，旧回包也要丢
	t.Run("manual-save-retry-only", func(t *testing.T) {
		m, _, old := fresh()
		m.dirty, m.saveErr = true, lit("x")
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
		if !m.saving {
			t.Fatal("s 应重试保存")
		}
		m.set.Projects[0].Name = "kept"
		m.Update(old)
		if m.set.Projects[0].Name != "kept" || !m.dirty {
			t.Fatalf("保存在路上，旧回包不该替换设置：name=%q dirty=%v", m.set.Projects[0].Name, m.dirty)
		}
	})
	// 手动 s 重试的保存已经成功（没有任何编辑，回到干净）之后，较早发出的静默回包才到：只有保存计数变了
	t.Run("manual-save-retry-completed", func(t *testing.T) {
		m, _, old := fresh()
		m.dirty, m.saveErr = true, lit("x")
		_, save := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
		run(m, save)
		if m.dirty || m.saving || !m.saved {
			t.Fatalf("重试的保存应已成功：dirty=%v saving=%v saved=%v", m.dirty, m.saving, m.saved)
		}
		m.set.Projects[0].Name = "kept"
		m.Update(old)
		if m.set.Projects[0].Name != "kept" {
			t.Fatal("保存之后到的旧回包替换了设置")
		}
	})
	// 按 R 强制重读完成之后才到的静默回包
	t.Run("force-reload-then-late-quiet", func(t *testing.T) {
		m, api, old := fresh()
		api.set.Generation = 20
		run(m, func() tea.Msg { msg := m.load().(loadedMsg); msg.force = true; return msg })
		if m.set.Generation != 20 {
			t.Fatalf("强制重读没生效：%d", m.set.Generation)
		}
		m.set.Generation = 20
		m.Update(old)
		if m.set.Generation != 20 || m.reloads != 1 {
			t.Fatalf("强制重读之后到的旧静默回包应丢弃：gen=%d reloads=%d", m.set.Generation, m.reloads)
		}
	})
	// 两个静默重读：较早发出的回包晚到，不覆盖较新的
	t.Run("older-quiet-after-newer", func(t *testing.T) {
		m, api, older := fresh()
		api.set.Generation = 30
		newer := quietFromLangSync(t, m)()
		api.set.Generation = 31
		m.Update(newer)
		if m.set.Generation != 30 {
			t.Fatalf("较新的静默回包应生效：%d", m.set.Generation)
		}
		m.Update(older)
		if m.set.Generation != 30 {
			t.Fatalf("较早发出的静默回包不该再覆盖：%d", m.set.Generation)
		}
	})
	// 回包到时已有保存冲突（别处改过）：不替换，等 owner 按 R
	t.Run("conflict-before-reply", func(t *testing.T) {
		m, _, old := fresh()
		m.conflict, m.dirty = true, true
		m.set.Projects[0].Name = "kept"
		m.Update(old)
		if m.set.Projects[0].Name != "kept" {
			t.Fatal("冲突状态下静默回包不该替换设置")
		}
	})
	// 静默重读失败（连不上）的旧回包也要丢弃，不在 owner 编辑之后冒出读取失败的提示
	t.Run("stale-failure-is-silent", func(t *testing.T) {
		m, _, _ := fresh()
		old := quietFromLangSync(t, m)()
		m.sub = &subView{kind: subAgent, agent: "codex", cur: 0}
		m.Update(tea.KeyMsg{Type: tea.KeySpace})
		m.msg = nil
		failed := old.(loadedMsg)
		failed.err = &deckclient.Error{Status: 500, Message: "boom"}
		m.Update(failed)
		if !m.msg.empty() {
			t.Fatalf("过期的失败回包不该显示提示：%q", m.msg.String())
		}
	})
}

// R2-3：别的请求先换了语言，设置页也要跟上（Codex 的 TestAuditS3R2ToolsLanguageChangeMissesQuietReload）。
func TestN060S3Fix3OtherRequestLanguageChangeReconciles(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	sock := shortSock(t) // n060 自己的短路径辅助（n060_s3_fix2_test.go）；testsock 在 n067 分支，n060-a1 单独编译用不到
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(i18n.LangHeader, "en")
		switch r.URL.Path {
		case "/v1/tools":
			w.Write([]byte(`[]`))
		case "/v1/lang":
			w.Write([]byte(`{"lang":"en"}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	})}
	go srv.Serve(ln)
	defer srv.Close()
	m, api := n057Fixture()
	m.width, m.height = 100, 40
	m.set.UILang = "zh"
	// 另一个窗口已经把语言存成英文（代数也往前走了）
	api.set.UILang = "en"
	api.set.Generation = 40
	real := DeckSettingsAPI{C: deckclient.NewUI(sock).FollowLang()}
	if _, err := real.Tools(context.Background()); err != nil {
		t.Fatal(err)
	}
	if i18n.Lang() != i18n.EN {
		t.Fatal("取工具的响应没有让本进程换语言")
	}
	synced := m.syncLangCmd()().(langSyncedMsg)
	if synced.changed {
		t.Fatal("这一次对时前后语言没变（语言是别的请求换的）：测试前提不成立")
	}
	_, next := m.Update(synced)
	batch, ok := next().(tea.BatchMsg)
	if !ok {
		t.Fatal("别的请求先换了语言，设置页应排出静默重读")
	}
	for _, c := range batch {
		if lm, isLoaded := c().(loadedMsg); isLoaded {
			m.Update(lm)
		}
	}
	if m.set.UILang != "en" || m.set.Generation != 40 {
		t.Fatalf("设置没有跟上：ui_lang=%q generation=%d", m.set.UILang, m.set.Generation)
	}
	m.switchPage(pageGeneral)
	m.cur = rowIdx(t, m, rowLang, "", "")
	if v := plainView(m); !strings.Contains(v, "● English") || strings.Contains(v, "● Chinese") {
		t.Fatalf("通用页的语言行没有跟上：\n%s", v)
	}
	// 编辑别的设置：带的是新代数，不会因为旧代数遇到冲突；空格能正常切回中文
	press(m, " ")
	if len(api.saved) != 1 || api.saved[0].Generation != 40 || api.saved[0].UILang != "zh" {
		t.Fatalf("空格应从 English 切到中文并带新代数保存：%+v", api.saved)
	}
	if m.conflict || i18n.Lang() != i18n.ZH {
		t.Errorf("不该有冲突，语言应切回中文：conflict=%v lang=%s", m.conflict, i18n.Lang())
	}
}

// R2-3：同一件事的另一种来源——设置页自己的保存之后别的请求又换了语言；以及脏的时候先不重读，干净了再对上。
func TestN060S3Fix3LanguageDivergenceRetriesUntilClean(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	m, api := n057Fixture()
	api.set.UILang = "en"
	api.set.Generation = 50
	i18n.SetLang(i18n.EN) // 别的请求换的
	m.dirty = true
	_, next := m.Update(langSyncedMsg{changed: false})
	if b, ok := next().(tea.BatchMsg); ok && len(b) > 1 {
		t.Fatal("有未存的改动时不该重读")
	}
	m.dirty = false
	_, next = m.Update(langSyncedMsg{changed: false})
	b, ok := next().(tea.BatchMsg)
	if !ok {
		t.Fatal("改动存上之后，下一拍应发现语言没对上并静默重读")
	}
	m.Update(b[len(b)-1]())
	if m.set.UILang != "en" || m.set.Generation != 50 {
		t.Fatalf("重读后设置应跟上：%q %d", m.set.UILang, m.set.Generation)
	}
	// 对上之后不再重读
	_, next = m.Update(langSyncedMsg{changed: false})
	if b, ok := next().(tea.BatchMsg); ok && len(b) > 1 {
		t.Error("语言已对上，不该再重读")
	}
}

// R2-4：动作发生时只存原始（已中和、未按语言渲染）的值，显示清洗与打码记号在画的时候按当时的语言做
// （Codex 的 TestAuditS3R2SanitizedPromptKeepsOldRedactionLanguage）。
func TestN060S3Fix3SanitizedPromptFollowsLanguage(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	dir := filepath.Clean(t.TempDir())
	m, _ := n057Fixture()
	m.width, m.height = 160, 40
	m.set.Projects[0].Path = dir
	m.set.Projects[0].Name = "token=abcdefghijklmnop" // 普通配置名，显示清洗时才会打码
	m.form = newProjectForm(dir)
	m.choose(dir)
	zh := m.form.err.String()
	if !strings.Contains(zh, "[已打码]") {
		t.Fatalf("中文下应是真打码：%q", zh)
	}
	i18n.SetLang(i18n.EN)
	en := m.form.err.String()
	if strings.Contains(en, "[已打码]") || !strings.Contains(en, "[redacted]") {
		t.Errorf("同一实例切英文后应显示 [redacted]：%q", en)
	}
	i18n.SetLang(i18n.ZH)
	if back := m.form.err.String(); back != zh {
		t.Errorf("切回中文应与原来逐字一致：%q != %q", back, zh)
	}
}

// R2-4：新建成功、下一步提示也一样。
func TestN060S3Fix3CreatedAndNextHintsFollowLanguage(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	root := t.TempDir()
	dir := filepath.Join(root, "token=abcdefghijklmnop") // 目录名本身被打码
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	m, _ := n057Fixture()
	m.width, m.height = 160, 40
	m.form = newProjectForm(root)
	m.choose(dir)
	m.form.id = "newone"
	m.createProject()
	msgZH, hintZH := m.msg.String(), m.hint.String()
	if !strings.Contains(msgZH, "[已打码]") || !strings.Contains(hintZH, "[已打码]") {
		t.Fatalf("中文下新建成功与下一步提示应含真打码：%q / %q", msgZH, hintZH)
	}
	i18n.SetLang(i18n.EN)
	for name, s := range map[string]string{"msg": m.msg.String(), "hint": m.hint.String()} {
		if strings.Contains(s, "[已打码]") || !strings.Contains(s, "[redacted]") {
			t.Errorf("%s 切英文后应是 [redacted]：%q", name, s)
		}
		if r, bad := hanIn(s); bad {
			t.Errorf("%s 切英文后还有汉字 %q：%q", name, r, s)
		}
	}
}

// R2-1（路径 1）：最外层不带码的文件系统错误，显示用的副本要先中和；错误本身和 err.Error() 原文不改
// （Codex 的 TestAuditS3R2FilesystemOuterErrorForgesMark）。
func TestN060S3Fix3FilesystemOuterErrorDoesNotForgeMark(t *testing.T) {
	i18n.SetLang(i18n.EN)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	root := t.TempDir()
	missing := filepath.Join(root, "external-"+i18n.RedactMark)
	_, raw := os.ReadDir(missing)
	if raw == nil {
		t.Fatal("expected filesystem error")
	}
	if _, _, ok := i18n.Wire(raw); ok {
		t.Fatal("PathError should have no code")
	}
	rawText := raw.Error()
	shown := deckclient.Show(raw)
	m, _ := n057Fixture()
	m.width, m.height = 160, 40
	m.form = &projectForm{dir: root}
	m.form.enter(missing)
	foot := strings.Join(m.formFoot(), "\n")
	for name, s := range map[string]string{"Show": shown, "formFoot": foot, "errLong": errLong(raw)} {
		if strings.Contains(s, "[redacted]") || i18n.HasMark(s) {
			t.Errorf("%s 把外部文件名当成了打码：%q", name, s)
		}
	}
	if raw.Error() != rawText || !strings.Contains(rawText, i18n.RedactMark) {
		t.Error("错误本身与 err.Error() 原文不能改")
	}
}

// R2-1（路径 2）：新建项目表单里直接显示的目录名；选好的目录消失后的提示（Codex 的 TestAuditS3R2FilesystemDirectStringForgesMark）。
func TestN060S3Fix3FormDirectoryNamesDoNotForgeMark(t *testing.T) {
	for _, lang := range []string{i18n.EN, i18n.ZH} {
		t.Run(lang, func(t *testing.T) {
			i18n.SetLang(lang)
			t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
			root := t.TempDir()
			dir := filepath.Join(root, "external-"+i18n.RedactMark)
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			sub := filepath.Join(dir, "child-"+i18n.RedactMark)
			if err := os.Mkdir(sub, 0700); err != nil {
				t.Fatal(err)
			}
			forged := func(s string) bool {
				return strings.Contains(s, "[redacted]") || strings.Contains(s, "[已打码]") || i18n.HasMark(s)
			}
			m, _ := n057Fixture()
			m.width, m.height = 160, 40
			m.form = newProjectForm(dir)
			if view := m.formView(); forged(view) {
				t.Errorf("目录名与子目录名被当成打码：%q", view)
			}
			m.form.chosen, m.form.id = dir, "external" // 确认 id 那一步显示选中的目录
			if view := m.formView(); forged(view) {
				t.Errorf("已选目录被当成打码：%q", view)
			}
			// 选好的目录在确认前消失
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
			m.createProject()
			if foot := strings.Join(m.formFoot(), "\n"); forged(foot) || foot == "" {
				t.Errorf("目录消失的提示：%q", foot)
			}
			// 业务原值不变：表单里仍是真路径
			if m.form.chosen != dir {
				t.Error("业务路径被改了")
			}
		})
	}
}

// R2-1（路径 2 的近邻）：读目录失败的提示里的路径、目录已是项目时的项目名、路径检查失败的提示。
func TestN060S3Fix3FormNeighbouringMessagesDoNotForgeMark(t *testing.T) {
	i18n.SetLang(i18n.EN)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	root := t.TempDir()
	bad := filepath.Join(root, "bad-"+i18n.RedactMark)
	m, _ := n057Fixture()
	m.width, m.height = 160, 40
	m.form = &projectForm{dir: root}
	m.form.enter(bad) // 读不了
	if s := m.form.err.String(); strings.Contains(s, "[redacted]") || i18n.HasMark(s) {
		t.Errorf("读目录失败的提示：%q", s)
	}
	// 目录已经是项目（项目名里有外部伪造标记）
	dir := filepath.Clean(t.TempDir())
	m.set.Projects[0].Path = dir
	m.set.Projects[0].Name = "x-" + i18n.RedactMark
	m.form = newProjectForm(dir)
	m.choose(dir)
	if s := m.form.err.String(); strings.Contains(s, "[redacted]") || i18n.HasMark(s) {
		t.Errorf("目录已是项目的提示：%q", s)
	}
	// 路径检查失败（路径里带伪造标记的目录名）
	weird := filepath.Join(root, "w-"+i18n.RedactMark)
	if err := os.Mkdir(weird, 0700); err != nil {
		t.Fatal(err)
	}
	m.form = newProjectForm(root)
	m.choose(weird)
	if s := strings.Join(m.formFoot(), "") + m.form.err.String(); strings.Contains(s, "[redacted]") || i18n.HasMark(s) {
		t.Errorf("选择含标记字符的目录的结果：%q", s)
	}
}

// R2-4（清单里的另一处）：主 agent 被换掉的提示（main.moved）里的 agent 名也是画的时候才清洗、打码记号按当时的语言。
func TestN060S3Fix3MainMovedHintFollowsLanguage(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	m, _ := n057Fixture()
	m.width, m.height = 160, 40
	for i := range m.set.Agents {
		if m.set.Agents[i].ID == "codex" {
			m.set.Agents[i].Name = "token=abcdefghijklmnop" // 普通配置名（新的主 agent），显示清洗时才会打码
		}
	}
	// 主 agent 是 claude；把 claude 的模型清单拿掉，fixMain 会换到 codex 并提示
	p := m.project()
	codex, _ := m.agent("codex")
	p.Models = map[deckstore.Tier]deckstore.ModelList{codex.Models[0].Tier: {codex.Models[0].ID}}
	m.fixMain(p)
	zh := m.msg.String()
	if !strings.Contains(zh, "[已打码]") {
		t.Fatalf("夹具应产生带打码名字的提示：%q", zh)
	}
	i18n.SetLang(i18n.EN)
	en := m.msg.String()
	if strings.Contains(en, "[已打码]") || r(en) {
		t.Errorf("切英文后提示里不该有旧语言的打码记号：%q", en)
	}
}

func r(s string) bool { _, bad := hanIn(s); return bad }

// R2-2/R2-3 的配合：过时被丢弃的静默回包不会让语言永远对不上——干净之后下一拍再读；自己存上语言之后不会多读。
func TestN060S3Fix3DiscardedQuietReplyIsRetriedAndOwnSaveNeedsNoReload(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	m, api := n057Fixture()
	api.set.UILang = "en"
	api.set.Generation = 60
	i18n.SetLang(i18n.EN) // 别处换了语言
	old := quietFromLangSync(t, m)()
	m.sub = &subView{kind: subAgent, agent: "codex", cur: 0}
	_, save := m.Update(tea.KeyMsg{Type: tea.KeySpace}) // owner 改了一处
	m.Update(old)                                       // 旧回包被丢
	if m.set.Generation == 60 {
		t.Fatal("旧回包不该生效")
	}
	run(m, save) // 保存回来
	api.set.UILang, api.set.Generation = "en", 61
	_, next := m.Update(langSyncedMsg{changed: false})
	b, ok := next().(tea.BatchMsg)
	if !ok {
		t.Fatal("干净之后，语言仍没对上，下一拍应再读一次")
	}
	m.Update(b[len(b)-1]())
	if m.set.Generation != 61 || m.set.UILang != "en" {
		t.Fatalf("再读之后应跟上：%d %q", m.set.Generation, m.set.UILang)
	}
	// 对上之后自己在通用页切语言并存上：进程语言就是最后对齐的语言，不再多读
	m.sub, m.saved = nil, false
	m.switchPage(pageGeneral)
	m.cur = rowIdx(t, m, rowLang, "", "")
	n := len(api.saved)
	press(m, " ")
	if !m.saved || m.dirty || len(api.saved) != n+1 || api.saved[n].UILang != "zh" || i18n.Lang() != i18n.ZH {
		t.Fatalf("应已存上中文：saved=%v dirty=%v lang=%s %+v", m.saved, m.dirty, i18n.Lang(), api.saved)
	}
	_, next = m.Update(langSyncedMsg{changed: false})
	if b, ok := next().(tea.BatchMsg); ok && len(b) > 1 {
		t.Error("自己存上语言之后语言是对齐的，不该再静默重读")
	}
}

// 回包保护的四个条件各自都有用（直接表驱动：别的条件都保持相同，只让一个不同）。
func TestN060S3Fix3QuietStaleChecksEachCondition(t *testing.T) {
	m, _ := n057Fixture()
	m.quietSeq, m.edits, m.reloads, m.saves = 5, 3, 2, 4
	same := quietStamp{seq: 5, edits: 3, reloads: 2, saves: 4}
	if m.quietStale(same) {
		t.Fatal("什么都没变：不该过时")
	}
	for name, st := range map[string]quietStamp{
		"请求序号": {seq: 4, edits: 3, reloads: 2, saves: 4},
		"编辑":   {seq: 5, edits: 2, reloads: 2, saves: 4},
		"强制重读": {seq: 5, edits: 3, reloads: 1, saves: 4},
		"保存":   {seq: 5, edits: 3, reloads: 2, saves: 3},
	} {
		if !m.quietStale(st) {
			t.Errorf("%s 变了，回包应当过时", name)
		}
	}
	for name, set := range map[string]func(){
		"未存的改动": func() { m.dirty = true }, "保存在路上": func() { m.saving = true },
		"保存期间又改": func() { m.pending = true }, "冲突": func() { m.conflict = true },
	} {
		m.dirty, m.saving, m.pending, m.conflict = false, false, false, false
		set()
		if !m.quietStale(same) {
			t.Errorf("%s：回包到时设置不干净，应当过时", name)
		}
	}
}
