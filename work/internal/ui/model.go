package ui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tingschen287/terminal-deck/work/internal/binding"
	"github.com/Tingschen287/terminal-deck/work/internal/dispatch"
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

// Mode 是界面当前在做什么。
type Mode int

const (
	// ModeBrowse 是平时的样子：看，不动。
	ModeBrowse Mode = iota
	// ModePickModel 是给选中的节点挑模型。
	ModePickModel
)

// Model 是 Work Deck 的界面状态。
//
// 界面能做的事只有两件：看，以及给一个 ready 节点派工。改不了 Graph 状态，
// 写不了账本，接管终端是后面节点的事。
type Model struct {
	reader  *projectctl.Reader
	snap    projectctl.Snapshot
	width   int
	height  int
	loading bool

	// 派工能力。没配置时为 nil，界面照常看，按 d 会说清楚缺什么。
	dispatcher Dispatcher
	// 光标停在哪个节点上。
	cursor int
	mode   Mode
	// 当前节点可选的模型，按档位从低到高排。
	candidates []dispatch.Model
	pick       int
	// 上一次派工的结果，直接显示给人看，成功失败都显示。
	notice   string
	noticeOK bool
	// inflight 表示上一轮取数还没回来。
	// 真实项目上一次完整取数要近两分钟，而心跳是 5 秒一次；不挡一下的话，
	// 取数会一轮压一轮，每一轮又各开几个控制器进程，很快就把机器占满。
	inflight bool
}

// Dispatcher 是界面用到的那一点点派工能力。
//
// 收窄成接口，界面测试就不必牵扯真实上游，也一眼看得出界面只会派工，
// 不会去动别的东西。
type Dispatcher interface {
	Dispatch(ctx context.Context, req dispatch.Request) (binding.Binding, error)
	Candidates(required dispatch.RouteClass) []dispatch.Model
}

// WithDispatcher 给界面接上派工能力。
func (m Model) WithDispatcher(d Dispatcher) Model {
	m.dispatcher = d
	return m
}

// New 造一个绑定到指定项目的界面。
func New(reader *projectctl.Reader) Model {
	// inflight 一开始就是 true：Init 必定发起一轮完整取数，
	// 不在这里登记的话，第一拍心跳会再叠一轮上去。
	return Model{reader: reader, width: 100, height: 40, loading: true, inflight: true}
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
		return m.onKey(msg)
	case dispatchDoneMsg:
		m.mode = ModeBrowse
		m.notice = msg.text
		m.noticeOK = msg.ok
		// 派完立刻重取一次，让绑定和状态尽快反映到屏幕上。
		if !m.inflight {
			m.inflight = true
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
		m.inflight = false
		return m, nil
	case tickMsg:
		// 上一轮还没回来就跳过这一拍，只把心跳续上。
		if m.inflight {
			return m, tick()
		}
		m.inflight = true
		return m, tea.Batch(m.fetch(), tick())
	}
	return m, nil
}

// dispatchDoneMsg 是一次派工的结局，成败都要说出来。
type dispatchDoneMsg struct {
	ok   bool
	text string
}

func (m Model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case ModePickModel:
		switch msg.String() {
		case "esc", "q":
			m.mode = ModeBrowse
			m.notice = "已取消派工"
			m.noticeOK = false
			return m, nil
		case "up", "k":
			if m.pick > 0 {
				m.pick--
			}
			return m, nil
		case "down", "j":
			if m.pick < len(m.candidates)-1 {
				m.pick++
			}
			return m, nil
		case "enter":
			return m.confirmDispatch()
		}
		return m, nil
	}

	switch msg.String() {
	case "q", "esc", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "j":
		if m.cursor < len(m.snap.Nodes)-1 {
			m.cursor++
		}
		return m, nil
	case "r":
		if m.inflight {
			return m, nil
		}
		m.loading = true
		m.inflight = true
		return m, m.fetch()
	case "d":
		return m.beginDispatch()
	}
	return m, nil
}

// selected 返回光标停着的节点。
func (m Model) selected() (projectctl.Node, bool) {
	if m.cursor < 0 || m.cursor >= len(m.snap.Nodes) {
		return projectctl.Node{}, false
	}
	return m.snap.Nodes[m.cursor], true
}

// beginDispatch 给选中的节点挑模型。
//
// 每一步说不通就直接把原因摆出来，不静悄悄什么都不做——按了键没反应
// 比报错更难查。
func (m Model) beginDispatch() (tea.Model, tea.Cmd) {
	node, ok := m.selected()
	if !ok {
		m.notice = "没有选中任何节点"
		m.noticeOK = false
		return m, nil
	}
	if m.dispatcher == nil {
		m.notice = "没有配置派工：需要先写好上游路径、profile 和模型档位表"
		m.noticeOK = false
		return m, nil
	}
	state, known := node.State.Get()
	if !known {
		m.notice = fmt.Sprintf("%s 的状态还不知道（%s），不派", node.ID, node.State.Reason())
		m.noticeOK = false
		return m, nil
	}
	if state != projectctl.StateReady {
		m.notice = fmt.Sprintf("%s 现在是 %s，只有 ready 的节点能派工", node.ID, state)
		m.noticeOK = false
		return m, nil
	}
	if node.RouteClass == "" {
		m.notice = fmt.Sprintf("%s 没写 route class，拒绝派工，不猜", node.ID)
		m.noticeOK = false
		return m, nil
	}

	cands := m.dispatcher.Candidates(dispatch.RouteClass(node.RouteClass))
	if len(cands) == 0 {
		m.notice = fmt.Sprintf("档位表里没有能满足 %s 的模型，拒绝降档派工", node.RouteClass)
		m.noticeOK = false
		return m, nil
	}
	m.candidates = cands
	m.pick = 0
	m.mode = ModePickModel
	m.notice = ""
	return m, nil
}

// confirmDispatch 用挑中的模型真的派一次工。
func (m Model) confirmDispatch() (tea.Model, tea.Cmd) {
	node, ok := m.selected()
	if !ok || m.pick < 0 || m.pick >= len(m.candidates) {
		m.mode = ModeBrowse
		return m, nil
	}
	chosen := m.candidates[m.pick]
	attempt, hasAttempt := node.Attempt.Get()
	if !hasAttempt {
		// ready 节点还没开工，这次派的就是第一次 attempt。
		attempt = 1
	}
	req := dispatch.Request{
		NodeID:        node.ID,
		Attempt:       attempt,
		RequiredClass: dispatch.RouteClass(node.RouteClass),
		ModelID:       chosen.ID,
		ProjectPath:   m.snap.ProjectRoot,
		Title:         node.ID,
		DispatchedBy:  "work-deck",
	}
	d := m.dispatcher
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		b, err := d.Dispatch(ctx, req)
		if err != nil {
			return dispatchDoneMsg{ok: false, text: "派工被拒：" + err.Error()}
		}
		return dispatchDoneMsg{ok: true, text: fmt.Sprintf(
			"已派 %s attempt %d → 会话 %s（%s / %s）",
			b.NodeID, b.Attempt, b.SessionID, b.Model, b.RouteClass)}
	}
}

// View 画当前这一屏。
func (m Model) View() string {
	if m.loading && m.snap.ProjectRoot == "" {
		return "正在读取 Graph…\n"
	}
	return RenderScreen(m.snap, m.width, Screen{
		Cursor:     m.cursor,
		Picking:    m.mode == ModePickModel,
		Candidates: m.candidates,
		Pick:       m.pick,
		Notice:     m.notice,
		NoticeOK:   m.noticeOK,
	})
}
