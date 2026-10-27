package deckui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// n069：F9 设置的「面板」页。

// panelFake 在设置的假接口上加面板接口：记下每次挂拔，并按「deckd 存下后的注册表」回答。
type panelFake struct {
	*fakeSettingsAPI
	mu      sync.Mutex
	defs    []deckstore.PanelDef
	calls   []string
	failSet error
	note    string
}

func (p *panelFake) Panels(ctx context.Context) ([]deckstore.PanelDef, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]deckstore.PanelDef(nil), p.defs...), nil
}

func (p *panelFake) SetPanel(ctx context.Context, name string, on bool) (PanelSetResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failSet != nil {
		return PanelSetResult{}, p.failSet
	}
	p.calls = append(p.calls, name+"="+map[bool]string{true: "on", false: "off"}[on])
	for i := range p.defs {
		if p.defs[i].Name == name {
			p.defs[i].On = on
		}
	}
	return PanelSetResult{Panels: append([]deckstore.PanelDef(nil), p.defs...), Applied: p.note == "", Note: p.note}, nil
}

func panelsFixture(t *testing.T) (*Settings, *panelFake) {
	t.Helper()
	m0, api := settingsFixture()
	pf := &panelFake{fakeSettingsAPI: api, defs: deckstore.DefaultPanels()}
	m := &Settings{API: pf, Installed: m0.Installed, width: 100, height: 40}
	_, cmd := m.Update(m.load())
	run(m, cmd)
	return m, pf
}

func TestPanelsPageListsPanelsAndTogglesWithSpace(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	m, pf := panelsFixture(t)
	press(m, "5")
	if m.page != pagePanels {
		t.Fatalf("数字键 5 是面板页：%d", m.page)
	}
	v := plainView(m)
	for _, want := range []string{"需要你决策", "项目进度", "额度", "agent 在干嘛", "通知", "已挂上", "未挂", "右栏", "下栏", "空格或 ⏎ 挂上/拔掉"} {
		if !strings.Contains(v, want) {
			t.Fatalf("面板页缺 %q：\n%s", want, v)
		}
	}
	if len(m.rows()) != 5 {
		t.Fatalf("5 块面板 5 行：%d", len(m.rows()))
	}
	// 光标在「需要你决策」，下移到「额度」按空格拔掉；再按 ⏎ 挂回
	press(m, "down", " ")
	if len(pf.calls) != 1 || pf.calls[0] != "quota=off" {
		t.Fatalf("应拔掉额度：%v", pf.calls)
	}
	if !strings.Contains(plainView(m), "已拔掉「额度」，布局已更新") {
		t.Fatalf("结果应说明：\n%s", plainView(m))
	}
	press(m, "enter")
	if len(pf.calls) != 2 || pf.calls[1] != "quota=on" {
		t.Fatalf("⏎ 应挂回：%v", pf.calls)
	}
	if !onOf(m.panels, "quota") {
		t.Fatal("界面里的状态应是服务器回的")
	}
	// 项目进度（默认没挂）挂上
	press(m, "down", " ")
	if pf.calls[2] != "progress=on" || !strings.Contains(plainView(m), "已挂上「项目进度」") {
		t.Fatalf("%v\n%s", pf.calls, plainView(m))
	}
	// 挂拔不走设置页的整份保存
	if len(m.API.(*panelFake).fakeSettingsAPI.saved) != 0 {
		t.Fatal("面板是另一份文件，不该触发设置保存")
	}
}

func onOf(defs []deckstore.PanelDef, name string) bool {
	for _, d := range defs {
		if d.Name == name {
			return d.On
		}
	}
	return false
}

func TestPanelsPageEnglishHasNoHan(t *testing.T) {
	i18n.SetLang(i18n.EN)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	m, _ := panelsFixture(t)
	press(m, "5", " ")
	v := plainView(m)
	for _, want := range []string{"Panels", "Needs your decision", "Project progress", "Quota", "Attached", "right column", "bottom bar", "Detached"} {
		if !strings.Contains(v, want) {
			t.Fatalf("英文面板页缺 %q：\n%s", want, v)
		}
	}
	for _, r := range v {
		if r >= 0x4e00 && r <= 0x9fff {
			t.Fatalf("英文面板页不该有汉字：\n%s", v)
		}
	}
}

// 布局没跟上：已存下（deckd 不回滚），界面如实说明原因。
func TestPanelsPageShowsWhenLayoutDidNotFollow(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	m, pf := panelsFixture(t)
	pf.note = "布局里没有中间窗格"
	press(m, "5", "down", " ")
	if v := plainView(m); !strings.Contains(v, "改动已保存，但布局没有跟上：布局里没有中间窗格") {
		t.Fatalf("应说明布局没跟上：\n%s", v)
	}
	if onOf(m.panels, "quota") {
		t.Fatal("已存下的状态仍要显示")
	}
}

func TestPanelsPageSetFailureRereadsAndSaysWhy(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	m, pf := panelsFixture(t)
	pf.failSet = errors.New("连不上 deckd")
	press(m, "5", " ")
	v := plainView(m)
	if !strings.Contains(v, "没改成") || !strings.Contains(v, "连不上 deckd") {
		t.Fatalf("失败应说明：\n%s", v)
	}
	if !onOf(m.panels, "decisions") {
		t.Fatal("失败后显示服务器上的现状，不显示一个没成的状态")
	}
	if m.panelBusy {
		t.Fatal("失败后应能再按")
	}
}

// 回来之前再按不理：连按不会发两个各按各的旧值的请求。
func TestPanelsPageIgnoresPressWhileBusy(t *testing.T) {
	m, pf := panelsFixture(t)
	press(m, "5", "down")
	cmd := m.togglePanel("quota")
	if cmd == nil || !m.panelBusy {
		t.Fatal("第一次应发出请求")
	}
	if m.togglePanel("quota") != nil {
		t.Fatal("在路上时再按应不理")
	}
	run(m, cmd)
	if m.panelBusy || len(pf.calls) != 1 {
		t.Fatalf("回来后应能再按：busy=%v calls=%v", m.panelBusy, pf.calls)
	}
}

// 接口不支持面板时页面说明用不了，不崩。
func TestPanelsPageWithoutPanelAPI(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	m, _ := settingsFixture()
	press(m, "5")
	if v := plainView(m); !strings.Contains(v, "连不上面板接口") || len(m.rows()) != 0 {
		t.Fatalf("\n%s", v)
	}
	press(m, " ", "enter") // 没有行：不崩
}

func TestPanelsPageNarrowAndShortWindow(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	m, _ := panelsFixture(t)
	press(m, "5")
	for _, sz := range [][2]int{{80, 24}, {40, 12}, {30, 10}} {
		m.width, m.height = sz[0], sz[1]
		v := ansi.Strip(m.View())
		if n := strings.Count(v, "\n"); n > sz[1] {
			t.Fatalf("%dx%d 画了 %d 行", sz[0], sz[1], n)
		}
		if sz[0] >= 80 && !strings.Contains(v, "额度") {
			t.Fatalf("%dx%d 看不到面板：\n%s", sz[0], sz[1], v)
		}
	}
}

func TestPanelTitleForCustomPanelIsSanitized(t *testing.T) {
	if got := panelTitle("my-panel"); got != "my-panel" {
		t.Fatal(got)
	}
	if got := panelTitle("a\x1b[31mred"); strings.Contains(got, "\x1b") {
		t.Fatalf("自定义面板名要清洗：%q", got)
	}
}
