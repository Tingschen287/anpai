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
	note    func() string // 答复后的提示。存的是怎么画，不是画好的字：语言在显示中途换了，下一帧就是新语言（返工 2，M1）
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
			// 没指定 -project：跟着中间窗格正在看的会话所属的项目（n069，deckd 按当前看的会话解析）；
			// 指定了就按指定的，优先生效（owner 的 panels.yaml 里就写着 -project）
			var p deckd.Progress
			if err := m.c.Get(ctx, "/v1/progress"+m.q(), &p); err != nil {
				return dataMsg{err: err}
			}
			return dataMsg{view: RenderProgressW(p, m.width)}
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
		m.syncLang(ctx) // 同上：本地直接返回的分支也要对语言
		return dataMsg{err: i18n.Errorf("panel.err.missing", m.name)}
	}
}

// syncLang 向 deckd 对一次界面语言（GET /v1/lang）。连不上就保持现有语言。
func (m *model) syncLang(ctx context.Context) {
	if m.c != nil {
		m.c.SyncLang(ctx)
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

type answeredMsg struct{ note func() string }

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
		// 提示不在这里翻译成字存起来：存下的是怎么画，每一帧按当前语言重画（错误本身带码，Show 取当前语言）
		if err != nil {
			return answeredMsg{note: func() string {
				return bad.Render(i18n.T("panel.err.answer") + deckstore.Display(deckclient.Show(err), 300))
			}}
		}
		return answeredMsg{note: func() string { return good.Render(i18n.T("panel.err.answered") + label(opt)) }}
	}
}

func (m *model) View() string {
	if m.err != nil {
		// 错误文本可能带上游或模型写的内容：显示前清洗（n050 评审 B7）
		return title.Render(m.name) + "\n" + bad.Render(i18n.T("ui.loadfail")+deckstore.Display(deckclient.Show(m.err), 300)) + "\n"
	}
	if m.name == "decisions" {
		note := ""
		if m.note != nil {
			note = m.note()
		}
		return RenderDecisions(m.decs, m.sel, note)
	}
	return m.view
}
