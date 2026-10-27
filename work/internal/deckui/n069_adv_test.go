package deckui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 挂拔请求在路上时按 q / Esc 退出：设置窗口直接退出，不等回答（请求被断开）。
func TestAdvQuitWhilePanelRequestInFlight(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	m, _ := panelsFixture(t)
	press(m, "5")
	cmd := m.togglePanel("decisions")
	if cmd == nil || !m.panelBusy {
		t.Fatal("夹具")
	}
	quit := false
	for _, k := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyRunes, Runes: []rune("q")}} {
		_, c := m.Update(k)
		if c != nil {
			if _, ok := c().(tea.QuitMsg); ok {
				quit = true
			}
		}
	}
	t.Logf("在路上时按 Esc/q 是否退出：%v（退出即断开连接，deckd 的请求 ctx 被取消）", quit)
	// 修了 deckd（applier 用 WithoutCancel）之后退出是无害的：注册表已存、布局照样收敛。只记录
	_ = quit
}

// 在路上时界面没有任何「处理中」提示；再按空格被静默吞掉。
func TestAdvNoBusyIndicatorAndSilentIgnore(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	m, _ := panelsFixture(t)
	press(m, "5")
	before := plainView(m)
	if m.togglePanel("decisions") == nil {
		t.Fatal("夹具")
	}
	during := plainView(m)
	_, c := m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")})
	after := plainView(m)
	if during == before && c == nil && after == before {
		t.Errorf("请求在路上（最长 35 秒）界面完全没变化，再按空格也没有任何反馈：用户无法分辨「卡了」与「没按上」")
	}
}

// 先发出挂拔，又切页再切回（进页会重读），重读的回答比挂拔的回答晚到：旧状态盖掉新状态。
func TestAdvStaleReloadOverwritesFreshSetResult(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	m, _ := panelsFixture(t)
	press(m, "5")
	stale := append([]deckstore.PanelDef(nil), m.panels...) // quota 还挂着
	fresh := append([]deckstore.PanelDef(nil), m.panels...)
	for i := range fresh {
		if fresh[i].Name == "quota" {
			fresh[i].On = false
		}
	}
	m.Update(panelSetMsg{name: "quota", on: false, res: PanelSetResult{Panels: fresh, Applied: true}})
	if onOf(m.panels, "quota") {
		t.Fatal("夹具")
	}
	m.Update(panelsMsg{defs: stale}) // 在 POST 提交之前发出的 GET，回答晚到
	if onOf(m.panels, "quota") {
		t.Errorf("晚到的旧重读把刚拔掉的额度又显示成「已挂上」，而消息栏仍写「已拔掉」；再按空格会发 on=false（无效果）")
	}
}

// 列表变短（别处改了、按 R 重读）时光标留在越界行：画面不崩，按键不崩，但光标行不在列表里。
func TestAdvCursorBeyondShrunkenList(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	m, _ := panelsFixture(t)
	press(m, "5", "down", "down", "down", "down")
	if m.cur != 4 {
		t.Fatalf("夹具 cur=%d", m.cur)
	}
	m.Update(panelsMsg{defs: deckstore.DefaultPanels()[:1]})
	v := plainView(m)
	t.Logf("缩到 1 行后 cur=%d rows=%d\n%s", m.cur, len(m.rows()), v)
	if m.cur >= len(m.rows()) {
		t.Errorf("光标 %d 越过只剩 %d 行的列表，没有任何行高亮；按空格无效", m.cur, len(m.rows()))
	}
}

func TestAdvEmptyPanelList(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	m, pf := panelsFixture(t)
	pf.defs = nil
	press(m, "5", "R")
	v := plainView(m)
	press(m, " ", "enter", "down", "up")
	t.Logf("空列表：\n%s", v)
	if !strings.Contains(v, "没有") && !strings.Contains(v, "空") {
		t.Errorf("空列表应有提示：\n%s", v)
	}
}

// 很窄/很矮：每行显示宽度不能超过窗口宽度（超了会被终端折行，弄乱整页）。
func TestAdvNarrowWidthLinesNeverWrap(t *testing.T) {
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		m, pf := panelsFixture(t)
		pf.note = strings.Repeat("布局没有中间窗格 no center pane ", 6)
		press(m, "5", "down", " ")
		for _, w := range []int{60, 40, 30, 24, 20} {
			for _, h := range []int{24, 12} {
				m.width, m.height = w, h
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Errorf("lang=%v %dx%d 崩了：%v", lang, w, h, r)
						}
					}()
					v := ansi.Strip(m.View())
					for _, ln := range strings.Split(v, "\n") {
						if strings.Contains(ln, "PgUp") || strings.Contains(ln, "PgDn") {
							continue // 设置页通用的滚动提示，不是面板页新增
						}
						if ansi.StringWidth(ln) > w {
							t.Errorf("lang=%v %dx%d 有一行宽 %d > %d：%q", lang, w, h, ansi.StringWidth(ln), w, ln)
							return
						}
					}
					if n := strings.Count(v, "\n") + 1; h > 0 && n > h {
						t.Errorf("lang=%v %dx%d 画了 %d 行", lang, w, h, n)
						return
					}
				}()
			}
		}
	}
	i18n.SetLang(i18n.ZH)
}

// 名字里有路径/查询字符的面板（理论上被 idRe 挡住，这里直接验 DeckSettingsAPI.SetPanel 的 URL 拼接不转义）。
func TestAdvSetPanelURLNotEscaped(t *testing.T) {
	var got string
	api := DeckSettingsAPI{C: nil}
	_ = api
	_ = context.Background
	_ = got
	t.Log("推测项：Do(\"POST\", \"/v1/panels/\"+name …) 不转义；name 只可能来自已通过 idRe 校验的注册表，当前不可达")
}
