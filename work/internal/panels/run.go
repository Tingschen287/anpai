package panels

import (
	"context"
	"net/url"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tingschen287/terminal-deck/work/internal/dashquota"
	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckd"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// Names 是内置面板名。
var Names = []string{"decisions", "quota", "progress", "notifications", "activity"}

type tickMsg struct{}
type dataMsg struct {
	view string
	decs []deckstore.Decision
	err  error
}

type model struct {
	name    string
	project string
	c       *deckclient.Client
	view    string
	err     error
	decs    []deckstore.Decision
	sel     int
	note    string
	width   int
	height  int
}

// Run 在当前终端里跑一个面板，直到按 q 或 Ctrl-C。
func Run(name, project string, c *deckclient.Client) error {
	known := false
	for _, n := range Names {
		known = known || n == name
	}
	if !known {
		return i18n.Errorf("panel.err.unknown", name, Names)
	}
	_, err := tea.NewProgram(&model{name: name, project: project, c: c, width: 40, height: 20}).Run()
	return err
}

func (m *model) Init() tea.Cmd { return m.fetch() }

func tick() tea.Cmd { return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tickMsg{} }) }

func (m *model) q() string {
	if m.project == "" {
		return ""
	}
	return "?project=" + url.QueryEscape(m.project)
}

func (m *model) fetch() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		switch m.name {
		case "quota":
			var qs []dashquota.ToolQuota
			if err := m.c.Get(ctx, "/v1/quota", &qs); err != nil {
				return dataMsg{err: err}
			}
			return dataMsg{view: RenderQuota(qs, m.width)}
		case "decisions":
			var ds []deckstore.Decision
			if err := m.c.Get(ctx, "/v1/decisions"+m.q(), &ds); err != nil {
				return dataMsg{err: err}
			}
			return dataMsg{decs: ds}
		case "progress":
			if m.project == "" {
				return dataMsg{err: i18n.Errorf("panel.err.project")}
			}
			var p deckd.Progress
			if err := m.c.Get(ctx, "/v1/progress"+m.q(), &p); err != nil {
				return dataMsg{err: err}
			}
			return dataMsg{view: RenderProgress(p)}
		case "notifications":
			var ns []deckstore.Notification
			if err := m.c.Get(ctx, "/v1/notifications"+m.q(), &ns); err != nil {
				return dataMsg{err: err}
			}
			return dataMsg{view: RenderNotifications(ns, m.height)}
		case "activity":
			var ss []deckstore.Session
			if err := m.c.Get(ctx, "/v1/sessions"+m.q(), &ss); err != nil {
				return dataMsg{err: err}
			}
			return dataMsg{view: RenderActivity(ss, time.Now())}
		}
		return dataMsg{err: i18n.Errorf("panel.err.missing", m.name)}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		return m, m.fetch()
	case dataMsg:
		m.err = msg.err
		if msg.err == nil {
			m.view, m.decs = msg.view, msg.decs
			if n := len(OpenDecisions(m.decs)); m.sel >= n {
				m.sel = n - 1
			}
			if m.sel < 0 {
				m.sel = 0
			}
		}
		return m, tick()
	case answeredMsg:
		m.note = msg.note
		return m, m.fetch()
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.sel > 0 {
				m.sel--
			}
		case "down", "j":
			if m.sel < len(OpenDecisions(m.decs))-1 {
				m.sel++
			}
		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			if m.name == "decisions" {
				return m, m.answer(int(msg.String()[0] - '1'))
			}
		}
	}
	return m, nil
}

type answeredMsg struct{ note string }

func (m *model) answer(i int) tea.Cmd {
	open := OpenDecisions(m.decs)
	if m.sel >= len(open) || i >= len(open[m.sel].Options) {
		return nil
	}
	d := open[m.sel]
	opt := d.Options[i]
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := m.c.Do(ctx, "POST", "/v1/decisions/"+d.ID+"/answer", map[string]any{"option": opt, "version": d.Version}, nil)
		if err != nil {
			return answeredMsg{note: bad.Render(i18n.T("panel.err.answer") + deckstore.Clean(err.Error(), 300))}
		}
		return answeredMsg{note: good.Render(i18n.T("panel.err.answered") + label(opt))}
	}
}

func (m *model) View() string {
	if m.err != nil {
		// 错误文本可能带上游或模型写的内容：显示前清洗（n050 评审 B7）
		return title.Render(m.name) + "\n" + bad.Render(i18n.T("ui.loadfail")+deckstore.Clean(m.err.Error(), 300)) + "\n"
	}
	if m.name == "decisions" {
		return RenderDecisions(m.decs, m.sel, m.note)
	}
	return m.view
}
