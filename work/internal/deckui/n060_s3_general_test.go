package deckui

import (
	"os"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 段 3 返工（owner 2026-10-20 定）：界面语言单独一页「通用」，加在现有各页之后；全局 agent 页恢复成 cf8a8bc 的样子。

// 全局 agent 页与 cf8a8bc 逐字一致：除了顶部页签栏多了「通用」，正文、底部说明、按键行一个字不差。
// 参照文件 testdata/n060_agents_page_cf8a8bc_*.txt 是在 cf8a8bc 的代码上渲染出来的（光标在第一行、在最后一行各一份，两种窗口大小）。
func TestN060S3GlobalAgentPageIsVerbatimCf8a8bc(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	i18n.SetLang(i18n.ZH)
	for _, c := range []struct {
		file          string
		w, h          int
		cursorAtLast  bool
		cursorOnFirst bool
	}{
		{"n060_agents_page_cf8a8bc_100x40.txt", 100, 40, false, true},
		{"n060_agents_page_cf8a8bc_last-100x40.txt", 100, 40, true, false},
		{"n060_agents_page_cf8a8bc_60x24.txt", 60, 24, false, true},
		{"n060_agents_page_cf8a8bc_last-60x24.txt", 60, 24, true, false},
	} {
		want, err := os.ReadFile("testdata/" + c.file)
		if err != nil {
			t.Fatal(err)
		}
		m, _ := n057Fixture()
		// n060 段 4：首次运行默认清单里 Codex 的三档改了（owner 2026-10-16：astra / 6.1-sol / 6-luna）。参照文件是 cf8a8bc 渲染的，
		// 里面是当时的 Codex 清单；这里把夹具里的 Codex 钉回那三档，页面的版式、文字、说明与 cf8a8bc 逐字比对不变
		for i := range m.set.Agents {
			if m.set.Agents[i].Tool == "codex" {
				m.set.Agents[i].Models = []deckstore.ModelDef{{ID: "gpt-6.1-sol", Tier: deckstore.Frontier},
					{ID: "gpt-5.6-terra", Tier: deckstore.Balanced}, {ID: "gpt-5.6-luna", Tier: deckstore.Economy}}
			}
		}
		m.width, m.height = c.w, c.h
		m.switchPage(pageAgents)
		m.cur = 0
		if c.cursorAtLast {
			m.cur = len(m.rows()) - 1
		}
		if last := m.rows()[len(m.rows())-1]; last.kind != rowAddAgent {
			t.Fatalf("全局 agent 页最后一行应是「加入」：%+v", last)
		}
		got := strings.Split(plainView(m), "\n")
		exp := strings.Split(string(want), "\n")
		if len(got) != len(exp) {
			t.Fatalf("%s：行数 %d，cf8a8bc 是 %d\n%s", c.file, len(got), len(exp), strings.Join(got, "\n"))
		}
		// 第一行是页签栏：多了「通用」，其余部分不变
		const extra = "   通用"
		head := strings.TrimRight(got[0], " ")
		if !strings.HasSuffix(head, extra) || head[:len(head)-len(extra)] != strings.TrimRight(exp[0], " ") {
			t.Fatalf("%s 页签栏只该多出「通用」：\n%q\n%q", c.file, got[0], exp[0])
		}
		for i := 1; i < len(got); i++ {
			if got[i] != exp[i] {
				t.Fatalf("%s 第 %d 行与 cf8a8bc 不一致：\n现在 %q\n原来 %q", c.file, i+1, got[i], exp[i])
			}
		}
	}
}

// 页的顺序与数字键：原有三页不动（1 项目、2 全局 agent、3 授权），新页是第四页、数字键 4；Tab 循环多一页。
func TestN060S3GeneralPageIsAppendedAfterTheExistingPages(t *testing.T) {
	if pageProject != 0 || pageAgents != 1 || pageTrust != 2 || pageGeneral != 3 || pageCount != 4 {
		t.Fatalf("页序：%d %d %d %d 共 %d", pageProject, pageAgents, pageTrust, pageGeneral, pageCount)
	}
	m, _ := n057Fixture()
	for _, c := range []struct {
		key  string
		want page
	}{{"1", pageProject}, {"2", pageAgents}, {"3", pageTrust}, {"4", pageGeneral}, {"1", pageProject}} {
		press(m, c.key)
		if m.page != c.want {
			t.Errorf("按 %q：第 %d 页，应为 %d", c.key, m.page, c.want)
		}
	}
	press(m, "5")
	if m.page != pageProject {
		t.Fatalf("没有第五页，5 不该动：%d", m.page)
	}
	// Tab 转一圈：项目 → 全局 agent → 授权 → 通用 → 项目；Shift+Tab 反向
	var seen []page
	for range pageCount + 1 {
		seen = append(seen, m.page)
		press(m, "tab")
	}
	if len(seen) != 5 || seen[1] != pageAgents || seen[2] != pageTrust || seen[3] != pageGeneral || seen[4] != pageProject || m.page != pageAgents {
		t.Fatalf("Tab 循环：%v 现在 %d", seen, m.page)
	}
	press(m, "1", "shift+tab")
	if m.page != pageGeneral {
		t.Fatalf("项目页 Shift+Tab 回到最后一页：%d", m.page)
	}
	// 各页的光标各自记着：通用页只有一行
	press(m, "down", "down")
	if m.cur != 0 {
		t.Fatalf("通用页只有一行：光标 %d", m.cur)
	}
	press(m, "2", "down")
	press(m, "4", "2")
	if m.cur != 1 {
		t.Fatalf("全局 agent 页的光标还在原处：%d", m.cur)
	}
}

// 页签栏、帮助行、按键行都走文字表，中英两套；英文页没有汉字。
func TestN060S3GeneralPageInBothLanguages(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	for _, c := range []struct {
		lang                          string
		w, h                          int
		tabs, label, opts, help, keys string
	}{
		{i18n.ZH, 100, 40, "项目   全局 agent   授权   通用", "界面语言", "● 中文   ○ English", "空格或 ⏎ 在中文与 English 之间切换，保存后立刻生效，不用重启",
			"Tab 换页  ↑↓ 选  空格或 ⏎ 切换  R 重读  q 关闭"},
		{i18n.EN, 100, 40, "Project   Agents   Trust   General", "Language", "○ Chinese   ● English",
			"Space or Enter switches between Chinese and English. It applies as soon as it is saved, no restart",
			"Tab page  ↑↓ move  Space/Enter switch  R reload  q close"},
		{i18n.EN, 40, 12, "General", "Language", "● English", "", ""}, // 窄窗口也完整：页签、一行语言
	} {
		i18n.SetLang(c.lang)
		m, _ := n057Fixture()
		m.width, m.height = c.w, c.h
		press(m, "4")
		v := plainView(m)
		for _, want := range []string{c.tabs, c.label, c.opts, c.help, c.keys} {
			if want != "" && !strings.Contains(v, want) {
				t.Errorf("%s %dx%d 缺 %q：\n%s", c.lang, c.w, c.h, want, v)
			}
		}
		if c.lang == i18n.EN {
			if r, bad := hanIn(v); bad {
				t.Errorf("英文通用页有汉字 %q：\n%s", r, v)
			}
		}
		if strings.Count(v, "界面语言")+strings.Count(v, "Language") != 1 {
			t.Errorf("语言行只出现一次：\n%s", v)
		}
	}
	// 其他页的页签栏也带着新页
	i18n.SetLang(i18n.EN)
	m, _ := n057Fixture()
	m.width, m.height = 100, 40
	for p := pageProject; p < pageCount; p++ {
		m.switchPage(p)
		if tabs := strings.Split(plainView(m), "\n")[0]; !strings.Contains(tabs, "General") {
			t.Errorf("第 %d 页的页签栏缺 General：%q", p, tabs)
		}
	}
}
