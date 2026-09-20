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

// fastSnapshotMsg 是那份不问控制器的先行快照。
type fastSnapshotMsg projectctl.Snapshot

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

// Init 先取一份不问控制器的快照把屏幕点亮，再去补剩下的状态。
//
// 真实项目里逐个问节点状态要一两分钟，不能让人对着空屏等。投影里已经有等 owner
// 的条目、运行中的 attempt 和预算，这些先画出来，其余节点标「查询中」慢慢填。
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.fetchFast(), m.fetch(), tick())
}

func (m Model) fetchFast() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return fastSnapshotMsg(m.reader.FastSnapshot(ctx))
	}
}

func (m Model) fetch() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
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
	case fastSnapshotMsg:
		// 先行快照只在完整快照还没到的时候顶上，不覆盖已经查全的结果。
		if m.loading {
			m.snap = projectctl.Snapshot(msg)
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
