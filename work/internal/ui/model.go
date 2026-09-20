package ui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

// refreshInterval 是自动刷新的间隔。
//
// 每次刷新要为每个节点跑一次 projectctl，太密会把机器跑满，所以定在几秒一次，
// 想立刻看最新的按 r。
const refreshInterval = 5 * time.Second

type snapshotMsg projectctl.Snapshot

type tickMsg time.Time

// Model 是 Work Deck 的界面状态。
//
// 它只持有一份快照和窗口尺寸。界面上没有任何能改动 Graph 的入口：
// 本轮的 Work Deck 是一块表盘，不是一个控制台。
type Model struct {
	reader  *projectctl.Reader
	snap    projectctl.Snapshot
	width   int
	height  int
	loading bool
}

// New 造一个绑定到指定项目的界面。
func New(reader *projectctl.Reader) Model {
	return Model{reader: reader, width: 100, height: 40, loading: true}
}

// Init 一上来就取一次，然后开始定时刷新。
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.fetch(), tick())
}

func (m Model) fetch() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		return snapshotMsg(m.reader.Snapshot(ctx))
	}
}

func tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Update 处理按键、窗口变化和取数结果。
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "r":
			m.loading = true
			return m, m.fetch()
		}
		return m, nil
	case snapshotMsg:
		m.snap = projectctl.Snapshot(msg)
		m.loading = false
		return m, nil
	case tickMsg:
		return m, tea.Batch(m.fetch(), tick())
	}
	return m, nil
}

// View 画当前这一屏。
func (m Model) View() string {
	if m.loading && m.snap.ProjectRoot == "" {
		return "正在读取 Graph…\n"
	}
	return Render(m.snap, m.width)
}
