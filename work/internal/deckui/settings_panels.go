package deckui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 「面板」页（n069，owner 试用：右栏面板无法在界面里挂上、拔掉；原型里「+ 添加面板」菜单与每块面板的 ×）。
// 列出面板注册表（panels.yaml）里的每一块：空格或 ⏎ 挂上、拔掉。改动经 deckd 的 POST /v1/panels/{名字} 存盘
// （deckd 是 panels.yaml 的唯一写入者），deckd 同时把改动作用到正在运行的布局，不用重建。
// 不走设置页的整份自动保存：面板注册表是另一份文件，改一块存一块。

// PanelsAPI 是设置接口可选的能力（真接口有；测试里的假接口按需加，没有它「面板」页只说明用不了）。
type PanelsAPI interface {
	Panels(ctx context.Context) ([]deckstore.PanelDef, error)
	// SetPanel 挂上或拔掉一块面板，返回存下后的注册表与布局那边的结果。
	SetPanel(ctx context.Context, name string, on bool) (PanelSetResult, error)
}

// PanelSetResult 是 POST /v1/panels/{名字} 的回答。
type PanelSetResult struct {
	Panels   []deckstore.PanelDef `json:"panels"`
	Applied  bool                 `json:"applied"` // 已作用到正在运行的布局
	Attached []string             `json:"attached"`
	Removed  []string             `json:"removed"`
	// Note 是布局没跟上的原因（带码，界面按语言翻译）；已存下的注册表不回滚
	Note     string `json:"note,omitempty"`
	NoteCode string `json:"note_code,omitempty"`
	NoteArgs []any  `json:"note_args,omitempty"`
}

func (a DeckSettingsAPI) Panels(ctx context.Context) ([]deckstore.PanelDef, error) {
	var out []deckstore.PanelDef
	return out, a.C.Get(ctx, "/v1/panels", &out)
}

func (a DeckSettingsAPI) SetPanel(ctx context.Context, name string, on bool) (PanelSetResult, error) {
	var out PanelSetResult
	err := a.C.Do(ctx, "POST", "/v1/panels/"+name, map[string]any{"on": on}, &out)
	return out, err
}

// panelNameKey 是内置面板的显示名（与原型一致）；自定义面板（panels.yaml 里手写的）没有，显示它的名字。
var panelNameKey = map[string]i18n.Key{
	"decisions":     "panel.name.decisions",
	"progress":      "panel.name.progress",
	"quota":         "panel.name.quota",
	"activity":      "panel.name.activity",
	"notifications": "panel.name.notifications",
}

func panelTitle(name string) string {
	if k, ok := panelNameKey[name]; ok {
		return T(k)
	}
	return sanitizeFull(name)
}

type panelsMsg struct {
	defs []deckstore.PanelDef
	err  error
}

type panelSetMsg struct {
	name string
	on   bool
	res  PanelSetResult
	err  error
}

func (m *Settings) panelsAPI() (PanelsAPI, bool) {
	p, ok := m.API.(PanelsAPI)
	return p, ok
}

// fetchPanels 读面板注册表（读完设置后、进「面板」页时、按 R 重读时）。
func (m *Settings) fetchPanels() tea.Cmd {
	p, ok := m.panelsAPI()
	if !ok {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		d, err := p.Panels(ctx)
		return panelsMsg{defs: d, err: err}
	}
}

// togglePanel 挂上或拔掉光标所在的面板。同一时刻只发一个请求：回来之前再按不理（连按会让两次请求各按各的旧值）。
func (m *Settings) togglePanel(name string) tea.Cmd {
	p, ok := m.panelsAPI()
	if !ok || m.panelBusy {
		return nil
	}
	on := true
	for _, d := range m.panels {
		if d.Name == name {
			on = !d.On
		}
	}
	m.panelBusy = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		res, err := p.SetPanel(ctx, name, on)
		return panelSetMsg{name: name, on: on, res: res, err: err}
	}
}

// panelsUpdate 处理面板页的消息；返回是否处理了。
func (m *Settings) panelsUpdate(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case panelsMsg:
		if msg.err != nil {
			err := msg.err
			m.panelsErr = later(func() string { return T("panels.load.fail", errLong(err)) })
			return nil, true
		}
		m.panels, m.panelsErr, m.panelsLoaded = msg.defs, nil, true
		return nil, true
	case panelSetMsg:
		m.panelBusy = false
		if msg.err != nil {
			err, name := msg.err, msg.name
			m.msg = later(func() string { return T("panels.set.fail", panelTitle(name), errLong(err)) })
			return m.fetchPanels(), true // 失败时按服务器上的现状重读，不显示一个没成的状态
		}
		m.panels, m.panelsLoaded, m.panelsErr = msg.res.Panels, true, nil
		name, on, res := msg.name, msg.on, msg.res
		m.msg = later(func() string { return panelResultText(name, on, res) })
		return nil, true
	}
	return nil, false
}

// panelResultText 是挂拔之后的一句话：已存并作用到布局；或已存但布局没跟上（说明原因与下一步）。
func panelResultText(name string, on bool, res PanelSetResult) string {
	title := panelTitle(name)
	if res.Note != "" || res.NoteCode != "" {
		why := deckclient.Text(res.NoteCode, res.NoteArgs, res.Note)
		return T("panels.set.notapplied", title, oneLine(deckstore.Display(why, 300)))
	}
	if on {
		return T("panels.set.on", title)
	}
	return T("panels.set.off", title)
}

func (m *Settings) panelsPage(b *strings.Builder, row func(string) string) {
	b.WriteString(" " + bold + T("panels.title") + reset + "\n" + m.note(T("panels.about")) + "\n")
	if _, ok := m.panelsAPI(); !ok {
		b.WriteString(dimOn + "   " + T("panels.unavailable") + reset + "\n")
		return
	}
	if !m.panelsErr.empty() {
		b.WriteString(orange + "   " + oneLine(deckstore.Display(m.panelsErr.String(), msgCap)) + reset + "\n")
		return
	}
	if !m.panelsLoaded {
		b.WriteString(dimOn + "   " + T("panels.loading") + reset + "\n")
		return
	}
	if len(m.panels) == 0 {
		b.WriteString(dimOn + "   " + T("panels.empty") + reset + "\n")
	}
	for _, d := range m.panels {
		mark := dimOn + "○ " + T("panels.state.off") + reset
		if d.On {
			mark = green + "●" + reset + " " + bold + T("panels.state.on") + reset
		}
		where := panelRegionText(d.Region)
		b.WriteString(row(fmt.Sprintf("%s  %s  %s%s%s", mark, panelTitle(d.Name), dimOn, where, reset)) + "\n")
	}
}

// panelRegionText 是面板所在的栏。
func panelRegionText(region string) string {
	if region == "bottom" {
		return T("panels.region.bottom")
	}
	return T("panels.region.right")
}
