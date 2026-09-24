package ui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
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
	// 这次挑模型是为了派评审，不是派工。
	pickingReviewer bool
	// 上一次动作的结果，直接显示给人看，成功失败都显示。
	notice   string
	noticeOK bool

	// 进入会话与预览。没配置时为 nil，按键会说清楚缺什么。
	attacher Attacher
	// enterNote 是这次进入前 adapter 给的降级说明（按 T 退到次一等来源时才有）。
	// 进入前的 notice 在真终端上只停一帧，所以回来之后再说一遍。
	enterNote string
	bindings Bindings
	// 预览内容，非空时占据屏幕下半部分。
	preview     string
	previewFrom string
	// 展开的完整理由。
	detail   string
	detailOf string
	// 是否把已验收的节点也列出来。
	showAll bool
	// inflight 表示上一轮取数还没回来。
	// 真实项目上一次完整取数要近两分钟，而心跳是 5 秒一次；不挡一下的话，
	// 取数会一轮压一轮，每一轮又各开几个控制器进程，很快就把机器占满。
	inflight bool

	// 决策队列。qw.store 为 nil 表示没接队列。
	qw queueWiring
	qv queueView
}

// Dispatcher 是界面用到的那一点点派工能力。
//
// 收窄成接口，界面测试就不必牵扯真实上游，也一眼看得出界面只会派工，
// 不会去动别的东西。
type Dispatcher interface {
	Dispatch(ctx context.Context, req dispatch.Request) (binding.Binding, error)
	DispatchReviewer(ctx context.Context, req dispatch.ReviewRequest) (binding.Binding, error)
	Candidates(required dispatch.RouteClass) []dispatch.Model
}

// Attacher 是界面用到的「进入会话」与「瞄一眼」两件事。
//
// 收窄成接口，界面测试不必牵扯真实上游；也一眼看得出界面只会进入和预览，
// 不会去停会话、删会话。
type Attacher interface {
	// AttachCommand 造一条进入会话的命令，但不执行——执行要由界面挂起自己之后做。
	AttachCommand(ctx context.Context, sessionID string) (*exec.Cmd, error)
	// DirectAttachCommand 造一条绕开上游 CLI、直连会话所在 tmux 的命令。
	//
	// 这是 owner 按 T 时走的那条。为什么要有它：上游的 session attach 是另一个
	// 进程去问 headless 服务的，而那个服务对菜单快照做内存缓存，只在服务自己的
	// 写接口或 hook 状态文件 watcher 触发时失效——CLI 刚 launch 出来的会话，
	// 同一个服务实例可能一直看不到。那种时候 tmux 里的会话好好的，只是上游那条路问不到它。
	//
	// 第二个返回值是给人看的说明：adapter 若退到了次一等的取数来源，
	// 这里会讲清楚退到了哪、代价是什么。空串表示走的是最准的那条。
	// 降级不许静默——界面要把这句话原样显示出来。
	DirectAttachCommand(ctx context.Context, sessionID string) (*exec.Cmd, string, error)
	// PaneOutputPlain 取会话当前画面的纯文本，只读，不接管。
	PaneOutputPlain(ctx context.Context, sessionID string) (string, error)
	// NestingAdvice 是进去之前要讲清楚的前缀键用法。
	NestingAdvice() string
}

// Bindings 让界面知道某个节点的某次 attempt 跑在哪个会话里。
type Bindings interface {
	Get(nodeID string, attempt int) (binding.Binding, bool)
}

// WithAttacher 给界面接上进入与预览能力。
func (m Model) WithAttacher(a Attacher, b Bindings) Model {
	m.attacher = a
	m.bindings = b
	return m
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
	cmds := []tea.Cmd{m.fetchFast(), m.fetch(), tick()}
	if m.qw.store != nil {
		cmds = append(cmds, m.pollQueue(), queueTick())
	}
	return tea.Batch(cmds...)
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
	case attachDoneMsg:
		// 回来了。状态不能停在进去之前，立刻重取一次。
		if msg.err != nil {
			m.notice, m.noticeOK = "从会话返回，但进入过程报错："+msg.err.Error()+
				"。按 p 看一眼那个会话还在不在，按 T 绕开上游直连 tmux，或按 r 刷新", false
		} else {
			m.notice, m.noticeOK = "已从会话 "+msg.sessionID+" 返回", true
		}
		if m.enterNote != "" {
			m.notice += "。进入时的说明：" + m.enterNote
			m.enterNote = ""
		}
		if !m.inflight {
			m.inflight = true
			return m, m.fetch()
		}
		return m, nil
	case previewDoneMsg:
		if msg.err != nil {
			m.preview, m.previewFrom = "", ""
			m.notice, m.noticeOK = "预览取不到："+msg.err.Error()+"。按 r 刷新，或确认那个会话还活着", false
			return m, nil
		}
		m.preview, m.previewFrom = msg.text, msg.sessionID
		m.notice, m.noticeOK = "", false
		return m, nil
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
		// 先行快照里状态不全，不拿它合并 Graph 门：那会把查询中的门误标成「已不等」。
		if m.loading {
			m.snap = projectctl.Snapshot(msg)
		}
		return m, nil
	case snapshotMsg:
		m.snap = projectctl.Snapshot(msg)
		m.loading = false
		m.inflight = false
		if err := m.mergeGraph(); err != "" {
			m.notice, m.noticeOK = err, false
		}
		return m, nil
	case queueTickMsg:
		return m, tea.Batch(m.pollQueue(), queueTick())
	case queuePolledMsg:
		m.qv.intake, m.qv.polled = msg.intake, true
		if msg.err != nil {
			m.notice, m.noticeOK = "决策队列落盘失败："+msg.err.Error(), false
		}
		return m, nil
	case queueAnsweredMsg:
		m.qv.busy = false
		switch {
		case msg.err != nil:
			m.notice, m.noticeOK = "回答没记上："+msg.err.Error(), false
		case msg.item.Answer != nil && msg.item.Answer.DeliveryError != "":
			m.notice, m.noticeOK = "投递失败，条目留在待拍板："+msg.item.Answer.DeliveryError, false
		case msg.item.Answer != nil:
			m.notice, m.noticeOK = msg.item.Answer.Note, true
		}
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

// attachDoneMsg 是从会话里回来了。
type attachDoneMsg struct {
	sessionID string
	err       error
}

// previewDoneMsg 是一次只读预览的结果。
type previewDoneMsg struct {
	sessionID string
	text      string
	err       error
}

// dispatchDoneMsg 是一次派工的结局，成败都要说出来。
type dispatchDoneMsg struct {
	ok   bool
	text string
}

func (m Model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.qv.show && m.queueOn() && m.mode == ModeBrowse {
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m.onQueueKey(msg)
	}
	switch m.mode {
	case ModePickModel:
		switch msg.String() {
		case "esc", "q":
			m.mode = ModeBrowse
			m.pickingReviewer = false
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
	case "q", "ctrl+c":
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
	case "enter":
		return m.toggleDetail()
	case "tab":
		m.showAll = !m.showAll
		return m, nil
	case "a":
		return m.beginAttach()
	case "T":
		return m.beginDirectAttach()
	case "p":
		return m.beginPreview()
	case "i":
		if !m.queueOn() {
			m.notice, m.noticeOK = "没有接决策队列（配置目录不可用，见启动时的提示）", false
			return m, nil
		}
		m.qv.show = true
		if m.qw.store != nil && m.qv.cursor >= len(m.qw.store.Items()) {
			m.qv.cursor = 0
		}
		return m, nil
	case "esc":
		switch {
		case m.preview != "":
			m.preview, m.previewFrom = "", ""
		case m.detail != "":
			m.detail, m.detailOf = "", ""
		default:
			return m, tea.Quit
		}
		return m, nil
	}
	return m, nil
}

// toggleDetail 展开或收起选中节点的完整理由。
//
// 摘要够日常扫一眼，但真要拍板时得看全文。全文很长，所以按需展开，
// 不默认铺在屏幕上。
func (m Model) toggleDetail() (tea.Model, tea.Cmd) {
	if m.detail != "" {
		m.detail, m.detailOf = "", ""
		return m, nil
	}
	node, ok := m.selected()
	if !ok {
		return m, nil
	}
	var parts []string
	if node.Reason != "" {
		parts = append(parts, "因为：\n"+node.Reason)
	}
	if node.RequiredAction != "" {
		parts = append(parts, "要你做：\n"+node.RequiredAction)
	}
	if node.LatestEvidence != "" && node.LatestEvidence != "none" {
		parts = append(parts, "最新证据：\n"+node.LatestEvidence)
	}
	if len(parts) == 0 {
		m.notice, m.noticeOK = node.ID+" 账本里没有额外说明", false
		return m, nil
	}
	m.detail = strings.Join(parts, "\n\n")
	m.detailOf = node.ID
	return m, nil
}

// sessionOf 找出选中节点这次 attempt 跑在哪个会话里。
func (m Model) sessionOf(node projectctl.Node) (string, error) {
	if m.bindings == nil {
		return "", fmt.Errorf("没有配置会话绑定。先写好 ~/.config/work-deck/config.json，再按 d 派工")
	}
	attempt, ok := node.Attempt.Get()
	if !ok {
		return "", fmt.Errorf("%s 还没开过工，没有会话可进。先按 d 派工", node.ID)
	}
	b, ok := m.bindings.Get(node.ID, attempt)
	if !ok {
		return "", fmt.Errorf("%s 没有 Work Deck 派出的会话。先按 d 派工，再按 a 进去",
			node.ID)
	}
	if b.SessionID == "" {
		return "", fmt.Errorf("%s 的绑定里没有会话号，这条绑定是坏的。按 d 重新派一次工", node.ID)
	}
	return b.SessionID, nil
}

// beginAttach 把终端交给选中节点的会话。
func (m Model) beginAttach() (tea.Model, tea.Cmd) {
	node, ok := m.selected()
	if !ok {
		m.notice, m.noticeOK = "没有选中任何节点。用 ↑↓ 选一个再按键", false
		return m, nil
	}
	if m.attacher == nil {
		m.notice, m.noticeOK = "没有配置进入会话。先写好 ~/.config/work-deck/config.json 里的 upstream_binary 与 profile", false
		return m, nil
	}
	sessionID, err := m.sessionOf(node)
	if err != nil {
		m.notice, m.noticeOK = err.Error(), false
		return m, nil
	}

	cmd, err := m.attacher.AttachCommand(context.Background(), sessionID)
	if err != nil {
		m.notice, m.noticeOK = "进不去："+err.Error()+"。可以先按 p 看一眼画面", false
		return m, nil
	}
	// 前缀键的事必须在进去之前讲：进去之后就看不见 Work Deck 了。
	// 这行 notice 在真终端上只停一帧；人读得到的是常驻那一行（Screen.EnterHint）。
	m.enterNote = ""
	m.notice = m.attacher.NestingAdvice()
	m.noticeOK = true
	// 交出终端。回来之后立刻重取一次，屏幕上的状态不能停在进去之前。
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return attachDoneMsg{sessionID: sessionID, err: err}
	})
}

// beginDirectAttach 绕开上游 CLI，直接连到会话所在的 tmux。
//
// 什么时候用得上：按 a 进不去，或者进去又被弹出来，而按 p 能看到画面——
// 那说明会话活着，是上游那条路问不到它。上游 headless 服务对菜单快照做内存缓存，
// CLI 刚起的会话它可能一直看不到，这是实测过的。
//
// 取数在 adapter 里分两处：先问 `GET /api/menu`（直连路径查的 HTTP 来源，给会话名与 socket 名），
// 菜单里没有这个会话时退到 CLI `list --json`，再扫本用户 tmux socket 目录下的 server 把 socket
// 名找回来并证明目标唯一。证明不了就拒绝，不盲连。细节见 adapter 的
// DirectAttachCommand 与 ResolveSocketFor。
func (m Model) beginDirectAttach() (tea.Model, tea.Cmd) {
	node, ok := m.selected()
	if !ok {
		m.notice, m.noticeOK = "没有选中任何节点。用 ↑↓ 选一个再按键", false
		return m, nil
	}
	if m.attacher == nil {
		m.notice, m.noticeOK = "没有配置进入会话。先写好 ~/.config/work-deck/config.json 里的 upstream_binary 与 profile", false
		return m, nil
	}
	sessionID, err := m.sessionOf(node)
	if err != nil {
		m.notice, m.noticeOK = err.Error(), false
		return m, nil
	}
	// 带超时：直连要扫本用户 socket 目录下每个 tmux server，僵死的 server 不该把界面卡住。
	// adapter 内部对单个探测另有更短的上限，这里是整趟的兜底。
	//
	// **这个 deadline 只管解析目标那一段。** 下面 defer 的 cancel 在本函数返回时
	// 就执行，而真正的 attach 是 Bubble Tea 挂起界面之后才跑的——adapter 若把它
	// 绑给要交出终端的命令，解析越顺利失败得越准时（attempt 9 的回归）。
	// 两边各有一条测试守着：TestResolveDeadlineIsAlreadyDoneWhenTheCommandRuns
	// 与 TestInteractiveAttachCommandsOutliveTheResolveContext。
	dctx, dcancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer dcancel()
	cmd, note, err := m.attacher.DirectAttachCommand(dctx, sessionID)
	if err != nil {
		m.notice, m.noticeOK = "直连 tmux 也进不去："+err.Error()+"。可以先按 p 看一眼画面", false
		return m, nil
	}
	m.notice = "绕开上游，直连 tmux。"
	m.enterNote = note
	if note != "" {
		// adapter 退到了次一等的来源，把代价讲清楚再进去。
		m.notice += note
	}
	m.notice += m.attacher.NestingAdvice()
	m.noticeOK = true
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return attachDoneMsg{sessionID: sessionID, err: err}
	})
}

// beginPreview 瞄一眼选中节点的会话，不接管。
func (m Model) beginPreview() (tea.Model, tea.Cmd) {
	node, ok := m.selected()
	if !ok {
		m.notice, m.noticeOK = "没有选中任何节点。用 ↑↓ 选一个再按键", false
		return m, nil
	}
	if m.attacher == nil {
		m.notice, m.noticeOK = "没有配置会话预览。先写好 ~/.config/work-deck/config.json 里的 upstream_binary 与 profile", false
		return m, nil
	}
	sessionID, err := m.sessionOf(node)
	if err != nil {
		m.notice, m.noticeOK = err.Error(), false
		return m, nil
	}
	a := m.attacher
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		text, err := a.PaneOutputPlain(ctx, sessionID)
		return previewDoneMsg{sessionID: sessionID, text: text, err: err}
	}
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
		m.notice = "没有选中任何节点。用 ↑↓ 选一个再按键"
		m.noticeOK = false
		return m, nil
	}
	if m.dispatcher == nil {
		m.notice = "没有配置派工。先写好 ~/.config/work-deck/config.json 里的 upstream_binary、profile 和 models"
		m.noticeOK = false
		return m, nil
	}
	state, known := node.State.Get()
	if !known {
		m.notice = fmt.Sprintf("%s 的状态还不知道（%s），不派。等它查出来再按 d", node.ID, node.State.Reason())
		m.noticeOK = false
		return m, nil
	}

	// submitted 的节点派的是评审，不是执行者。这是两件事，别混。
	reviewing := state == projectctl.StateSubmitted
	if !reviewing && state != projectctl.StateReady {
		m.notice = fmt.Sprintf("%s 现在是 %s。ready 的能派工，submitted 的能派评审，别的状态都不行",
			node.ID, state)
		m.noticeOK = false
		return m, nil
	}

	required := node.RouteClass
	if reviewing {
		if !node.ReviewRequired {
			m.notice = fmt.Sprintf("%s 不要求评审，不派", node.ID)
			m.noticeOK = false
			return m, nil
		}
		required = node.ReviewRouteClass
		if required == "" {
			m.notice = fmt.Sprintf("%s 没写评审要求的 route class，拒绝派评审，不猜。"+
				"去 work-graph.yaml 补 review_policy.route_class", node.ID)
			m.noticeOK = false
			return m, nil
		}
		// 独立性要能证明才派。两样都要有：
		// 执行者是谁（账本上的身份），以及他跑在哪个会话（绑定存储里的会话号）。
		// 后者是派工器最终采信的判据，这里先查一遍，是为了别让人白挑一轮模型。
		if node.ReviewIndependent {
			if _, ok := node.Actor.Get(); !ok {
				m.notice = fmt.Sprintf("%s 要求独立评审，但不知道执行者是谁（%s），无法判断独立性。"+
					"按 r 刷新，或确认状态投影是否过期", node.ID, node.Actor.Reason())
				m.noticeOK = false
				return m, nil
			}
			if m.bindings == nil {
				m.notice = fmt.Sprintf("%s 要求独立评审，但没接绑定存储，查不到执行者在哪个会话，拒绝派评审", node.ID)
				m.noticeOK = false
				return m, nil
			}
			attempt, hasAttempt := node.Attempt.Get()
			if !hasAttempt {
				m.notice = fmt.Sprintf("%s 要求独立评审，但不知道这是第几次 attempt（%s），"+
					"对不上执行会话，拒绝派评审", node.ID, node.Attempt.Reason())
				m.noticeOK = false
				return m, nil
			}
			if b, ok := m.bindings.Get(node.ID, attempt); !ok || b.SessionID == "" {
				m.notice = fmt.Sprintf("%s 要求评审独立于执行者，但绑定存储里没有 attempt %d 的执行会话。"+
					"这次执行不是 Work Deck 派的，界面证明不了独立性，拒绝派评审", node.ID, attempt)
				m.noticeOK = false
				return m, nil
			}
		}
	}
	if required == "" {
		m.notice = fmt.Sprintf("%s 没写 route class，拒绝派工，不猜。去 work-graph.yaml 补上", node.ID)
		m.noticeOK = false
		return m, nil
	}

	cands := m.dispatcher.Candidates(dispatch.RouteClass(required))
	if len(cands) == 0 {
		m.notice = fmt.Sprintf("档位表里没有能满足 %s 的模型，拒绝降档。去配置里补一个够档的模型", required)
		m.noticeOK = false
		return m, nil
	}
	m.candidates = cands
	m.pick = 0
	m.pickingReviewer = reviewing
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

	if m.pickingReviewer {
		executor, _ := node.Actor.Get()
		rreq := dispatch.ReviewRequest{
			NodeID:              node.ID,
			Attempt:             attempt,
			RequiredClass:       dispatch.RouteClass(node.ReviewRouteClass),
			ModelID:             chosen.ID,
			ExecutorActor:       executor,
			IndependentRequired: node.ReviewIndependent,
			ProjectPath:         m.snap.ProjectRoot,
			Title:               node.ID + "-review",
			// brief 是评审者开工时看到的第一段话。没有它，派出去的就是个空会话。
			Message:      reviewBrief(node, attempt),
			DispatchedBy: "work-deck",
		}
		d := m.dispatcher
		m.pickingReviewer = false
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			b, err := d.DispatchReviewer(ctx, rreq)
			if err != nil {
				return dispatchDoneMsg{ok: false, text: "派评审被拒：" + err.Error()}
			}
			return dispatchDoneMsg{ok: true, text: fmt.Sprintf(
				"已为 %s attempt %d 派出评审 → 会话 %s（%s / %s）",
				node.ID, b.Attempt, b.SessionID, b.Model, b.RouteClass)}
		}
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
		Picking:         m.mode == ModePickModel,
		PickingReviewer: m.pickingReviewer,
		Candidates: m.candidates,
		Pick:       m.pick,
		Notice:      m.notice,
		NoticeOK:    m.noticeOK,
		Preview:     m.preview,
		PreviewFrom: m.previewFrom,
		Detail:      m.detail,
		DetailOf:    m.detailOf,
		ShowAll:     m.showAll,
		EnterHint:   m.enterHint(),
		QueueOn:     m.queueOn(),
		QueueOpen:   m.queueOpen(),
		Queue:       m.queueScreen(),
	})
}

// enterHint 是常驻在屏幕上的前缀键用法。没有接管能力时不显示。
func (m Model) enterHint() string {
	if m.attacher == nil {
		return ""
	}
	return m.attacher.NestingAdvice()
}
