package ui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/binding"
	"github.com/Tingschen287/terminal-deck/work/internal/dispatch"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
	"github.com/Tingschen287/terminal-deck/work/internal/routing"
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
// 界面能做的事：看；派工与派评审；进入、预览、交还会话；看评审结论。对 Graph 只有一个写入口——
// 派工前经 projectctl start 开工（n042）；验收与其他状态变更一律不写，评审结论由 owner 自己运行命令记账。
type Model struct {
	reader  *projectctl.Reader
	snap    projectctl.Snapshot
	width   int
	height  int
	loading bool

	// 派工能力。没配置时为 nil，界面照常看，按 d 会说清楚缺什么。
	dispatcher Dispatcher
	// starter 在派工前对 Graph 开工（n042）。
	starter Starter
	// script 是控制器脚本路径。执行 brief 要把读合同与提交的完整命令写给执行者（n047）。
	script string
	// overlays 是派工后本地已知、快照还没刷到的 attempt（见 ledgerOverlay）。
	overlays map[string]ledgerOverlay
	// 光标停在哪个节点上。
	cursor int
	mode   Mode
	// 当前节点可选的模型，按档位从低到高排。
	candidates []dispatch.Model
	pick       int
	// 这次挑模型是为了派评审，不是派工。
	pickingReviewer bool
	// askingReason 为真：挑中的不是推荐（或这次没有推荐），正在等你写理由。reason 是已输入的内容。
	askingReason bool
	reason       string
	// askingTrust 为真：挑中的工具在 launch 前会改 owner 的 Codex 全局配置，正在等 owner 按 y 确认（n044）。
	// trustWarning 是摆给他看的那段话。
	askingTrust  bool
	trustWarning string
	// 上一次动作的结果，直接显示给人看，成功失败都显示。
	notice   string
	noticeOK bool

	// 进入会话与预览。没配置时为 nil，按键会说清楚缺什么。
	attacher Attacher
	// enterNote 是这次进入前 adapter 给的降级说明（按 T 退到次一等来源时才有）。
	// 进入前的 notice 在真终端上只停一帧，所以回来之后再说一遍。
	enterNote string
	bindings  Bindings
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

	// 写入权记账（n039）：进入前登记接管，回来后挂介入摘要，按 H + y 确认交还。
	// routing 是这一次挑模型时路由算出的因子（n039）。
	routing *routing.Decision

	lw        leaseWiring
	attachCtx *attachContext
	armed     armedHandback
}

// Dispatcher 是界面用到的那一点点派工能力。
//
// 收窄成接口，界面测试就不必牵扯真实上游，也一眼看得出界面只会派工，
// 不会去动别的东西。
type Dispatcher interface {
	Dispatch(ctx context.Context, req dispatch.Request) (binding.Binding, error)
	DispatchReviewer(ctx context.Context, req dispatch.ReviewRequest) (binding.Binding, error)
	Candidates(required dispatch.RouteClass) []dispatch.Model
	// Route 算出这次派单的全部因子与推荐（n039），供挑模型时摆给 owner 看。
	Route(nodeID string, attempt int, purpose string, required dispatch.RouteClass) routing.Decision
}

// Starter 是界面对 Graph 唯一的写入口：派工前让 projectctl 开工，拿账本给的 attempt（n042）。
type Starter interface {
	Start(ctx context.Context, r projectctl.StartRequest) (projectctl.StartResult, error)
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

// WithStarter 接上 Graph 开工入口。没接时 ready / rework 的节点不派：派了也对不上账本的 attempt。
func (m Model) WithStarter(s Starter) Model {
	m.starter = s
	return m
}

// WithScript 告诉界面控制器脚本在哪：执行 brief 要写出读合同与提交的完整命令（n047）。
func (m Model) WithScript(script string) Model {
	m.script = script
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
		var hbNote string
		m, hbNote = m.afterAttach(msg.sessionID, msg.err)
		if hbNote != "" {
			m.notice += "。" + hbNote
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
		if msg.nodeID != "" && msg.attempt > 0 {
			if m.overlays == nil {
				m.overlays = map[string]ledgerOverlay{}
			}
			m.overlays[msg.nodeID] = ledgerOverlay{attempt: msg.attempt, running: msg.started}
			m = m.applyOverlays()
		}
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
			m = m.applyOverlays()
		}
		return m, nil
	case snapshotMsg:
		m.snap = projectctl.Snapshot(msg)
		m = m.applyOverlays()
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
	// 账本此刻已知的情况（n042）：nodeID 的 attempt 已由 projectctl 建立（started），
	// 或已有 attempt 被派出会话。attempt 为 0 表示账本没有变化。
	nodeID  string
	attempt int
	started bool
}

// ledgerOverlay 是派工后本地已知、但快照还没刷到的账本事实（n040 终审断点 1，n042 attempt 1 评审）。
// 刷新是异步的：刷新回来之前按 p / a / T，界面用的还是旧快照的 attempt，会说「还没开过工」。
// 所以派工回执一到就把 attempt（开工了还有 running）叠到快照上，之后每份快照都再叠一次，
// 直到快照自己的 attempt 追上来——派工前就已发出的那次刷新晚到，也盖不掉它。
type ledgerOverlay struct {
	attempt int
	running bool
}

// applyOverlays 把本地已知的账本事实叠到当前快照上；快照追上的就撤掉。
func (m Model) applyOverlays() Model {
	if len(m.overlays) == 0 {
		return m
	}
	nodes := append([]projectctl.Node(nil), m.snap.Nodes...)
	for i, n := range nodes {
		ov, ok := m.overlays[n.ID]
		if !ok {
			continue
		}
		if a, known := n.Attempt.Get(); known && a >= ov.attempt {
			delete(m.overlays, n.ID)
			continue
		}
		nodes[i].Attempt = projectctl.Known(ov.attempt)
		if ov.running {
			nodes[i].State = projectctl.Known(projectctl.StateRunning)
		}
	}
	m.snap.Nodes = nodes
	return m
}

func (m Model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.armed.sessionID != "" && m.mode == ModeBrowse {
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m.onArmedKey(msg.String()), nil
	}
	if m.qv.show && m.queueOn() && m.mode == ModeBrowse {
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m.onQueueKey(msg)
	}
	switch m.mode {
	case ModePickModel:
		if m.askingTrust {
			return m.onTrustKey(msg)
		}
		if m.askingReason {
			return m.onReasonKey(msg)
		}
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
			if m.pick >= 0 && m.pick < len(m.candidates) && m.needsReason(m.candidates[m.pick].ID) {
				m.askingReason, m.reason = true, ""
				return m, nil
			}
			return m.proceedDispatch()
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
	case "H":
		return m.beginHandback(), nil
	case "v":
		return m.showVerdict(), nil
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
			// 主界面上 Esc 不退出（n047）：它在别处都是「关掉、回去」，这里却直接退出且不问，
			// n045 attempt 2 的界面进程就是被一次顺手的 Esc 关掉的。退出只认 q。
			m.notice, m.noticeOK = "Esc 在这里不退出；要退出按 q", true
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
	var busyErr error
	if m, busyErr = m.beforeAttach(sessionID, "upstream"); busyErr != nil {
		m.notice, m.noticeOK = "先不进："+busyErr.Error(), false
		return m, nil
	}
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
	var busyErr error
	if m, busyErr = m.beforeAttach(sessionID, "tmux"); busyErr != nil {
		m.notice, m.noticeOK = "先不进："+busyErr.Error(), false
		return m, nil
	}
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
	// ready / rework 先让 projectctl 开工、拿账本的 attempt 再派；running 只在账本已有 attempt、
	// 而这个 attempt 还没绑会话时能派（上次开工成功、派工没成的补派）。n042，n040 终审断点 4。
	reviewing := state == projectctl.StateSubmitted
	switch {
	case reviewing:
	case state == projectctl.StateReady || state == projectctl.StateRework:
		// 要先开工；有没有开工入口放到最后查，别盖住档位、候选这些更根本的拒绝理由。
	case state == projectctl.StateRunning:
		attempt, ok := node.Attempt.Get()
		if !ok {
			m.notice = fmt.Sprintf("%s 正在 running，但不知道是第几次 attempt（%s），对不上账本，不派", node.ID, node.Attempt.Reason())
			m.noticeOK = false
			return m, nil
		}
		if m.bindings != nil {
			if b, bound := m.bindings.Get(node.ID, attempt); bound {
				m.notice = fmt.Sprintf("%s attempt %d 已经绑在会话 %s 上，不重复派。按 p 看、按 a 进去", node.ID, attempt, b.SessionID)
				m.noticeOK = false
				return m, nil
			}
		}
	default:
		m.notice = fmt.Sprintf("%s 现在是 %s。ready / rework 的能开工派工，running 且还没绑会话的能补派，submitted 的能派评审，别的状态都不行",
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
	// 路由因子：只留可选的候选，推荐的排第一；一个可选的都没有就不进挑选。
	purpose := "executor"
	if reviewing {
		purpose = "reviewer"
	}
	att, _ := node.Attempt.Get()
	dec := m.dispatcher.Route(node.ID, att, purpose, dispatch.RouteClass(required))
	cands = selectableFirst(cands, dec)
	if len(cands) == 0 {
		m.notice = "所有候选都被排除，不派：" + dec.Reason + excludedSummary(dec)
		m.noticeOK = false
		return m, nil
	}
	if !reviewing && (state == projectctl.StateReady || state == projectctl.StateRework) && m.starter == nil {
		m.notice = fmt.Sprintf("%s 要先在 Graph 开工才能派，但没接 Graph 开工入口，不派", node.ID)
		m.noticeOK = false
		return m, nil
	}
	m.routing = &dec
	m.candidates = cands
	m.pick = 0
	m.pickingReviewer = reviewing
	m.mode = ModePickModel
	m.notice = ""
	return m, nil
}

// needsReason：挑中的不是推荐、或这次没有推荐时，要你写一句理由才派。路由没算出来也要理由。
func (m Model) needsReason(model string) bool {
	return m.routing == nil || m.routing.NeedsReason(model)
}

// onReasonKey 是写理由那一步：打字、退格、回车派、Esc 回去重挑。
func (m Model) onReasonKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEsc:
		m.askingReason, m.reason = false, ""
		return m, nil
	case tea.KeyEnter:
		if strings.TrimSpace(m.reason) == "" {
			m.notice, m.noticeOK = "理由不能为空：写一句为什么选它，或按 Esc 回去重挑", false
			return m, nil
		}
		return m.proceedDispatch()
	case tea.KeyBackspace:
		if r := []rune(m.reason); len(r) > 0 {
			m.reason = string(r[:len(r)-1])
		}
		return m, nil
	case tea.KeySpace:
		m.reason += " "
		return m, nil
	case tea.KeyRunes:
		m.reason += string(msg.Runes)
		return m, nil
	}
	return m, nil
}

// proceedDispatch 是挑好模型（要理由的已写完理由）之后的最后一关：挑中的工具在 launch 前会改 owner 的
// Codex 全局配置时，先把这件事摆出来，等 owner 按 y（n043 owner 验收补充发现）。其余直接派。
func (m Model) proceedDispatch() (tea.Model, tea.Cmd) {
	if m.pick >= 0 && m.pick < len(m.candidates) {
		if w := codexTrustWarning(m.candidates[m.pick], m.snap.ProjectRoot); w != "" {
			m.askingReason = false
			m.askingTrust, m.trustWarning = true, w
			return m, nil
		}
	}
	return m.confirmDispatch()
}

// codexTrustWarning 是派这个模型之前要 owner 知道的那件事；上游不会改 Codex 配置时为空。
func codexTrustWarning(c dispatch.Model, projectRoot string) string {
	switch agentdeck.CheckCodexTrust(c.Tool) {
	case agentdeck.CodexTrustYes:
		return fmt.Sprintf("upstream agent-deck 启动 %s 前，会把工作目录 %s 写进 %s 的 trust 列表。"+
			"这改的是你的 Codex 全局配置，而且上游是把整份文件解码再重新写回，原文件的注释与排版不保留。",
			c.Tool, projectRoot, agentdeck.CodexTrustPath)
	case agentdeck.CodexTrustMaybe:
		return fmt.Sprintf("工具 %s 是自定义工具：上游配置若声明它与 codex 兼容（或它的命令就是 codex），"+
			"launch 前会把工作目录 %s 写进 %s 的 trust 列表，并整份重写那个文件。Work Deck 看不到上游配置，判断不了。",
			c.Tool, projectRoot, agentdeck.CodexTrustPath)
	}
	return ""
}

// onTrustKey 是确认 Codex trust 写入那一步：y 派出，Esc / n 回去重挑，别的键不派。
func (m Model) onTrustKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "y":
		m.askingTrust, m.trustWarning = false, ""
		return m.confirmDispatch()
	case "esc", "n":
		m.askingTrust, m.trustWarning, m.reason = false, "", ""
		m.notice, m.noticeOK = "没派：回到挑模型", false
		return m, nil
	}
	m.notice, m.noticeOK = "按 y 确认派出，或按 Esc 回去重挑", false
	return m, nil
}

// unconfirmedAdvice 是「送达未确认」时告诉 owner 的下一步（n042）。
// Work Deck 看画面分不清「已经在跑」与「还停在输入框」，所以不自动重发，交给人看一眼。
const unconfirmedAdvice = "上游没证实对方接到了任务文本（可能到了画面没提交，也可能没到）。按 p 看画面：已经在跑就不用管；停在输入框就按 a 进去按回车；画面上没有就按 a 进去手工发。"

// confirmDispatch 用挑中的模型真的派一次工。
func (m Model) confirmDispatch() (tea.Model, tea.Cmd) {
	node, ok := m.selected()
	if !ok || m.pick < 0 || m.pick >= len(m.candidates) {
		m.mode = ModeBrowse
		return m, nil
	}
	chosen := m.candidates[m.pick]
	// 选择者就是在界面上按回车的 owner；理由是他刚写的（采纳推荐时为空）。
	chosenBy, reason := "owner", strings.TrimSpace(m.reason)
	m.askingReason, m.reason = false, ""
	attempt, hasAttempt := node.Attempt.Get()
	state, _ := node.State.Get()
	// 开工与否：ready / rework 由 projectctl start 给 attempt（派工器在会话起来之后调，n044）；其余状态用账本已有的 attempt。
	// 不知道 attempt 就不派——不再默认成 1（n040 终审断点 4：恢复过的节点会与账本错配）。
	needStart := !m.pickingReviewer && (state == projectctl.StateReady || state == projectctl.StateRework)
	if !needStart && !hasAttempt {
		m.mode = ModeBrowse
		m.pickingReviewer = false
		m.notice, m.noticeOK = fmt.Sprintf("%s 不知道是第几次 attempt（%s），对不上账本，不派", node.ID, node.Attempt.Reason()), false
		return m, nil
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
			ChosenBy:     chosenBy,
			ChoiceReason: reason,
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
			if b.Delivery == binding.DeliveryUnconfirmed {
				return dispatchDoneMsg{ok: false, text: fmt.Sprintf(
					"已为 %s attempt %d 派出评审 → 会话 %s（%s / %s），但 brief 送达未确认。%s",
					node.ID, b.Attempt, b.SessionID, b.Model, b.RouteClass, unconfirmedAdvice)}
			}
			return dispatchDoneMsg{ok: true, text: fmt.Sprintf(
				"已为 %s attempt %d 派出评审 → 会话 %s（%s / %s）",
				node.ID, b.Attempt, b.SessionID, b.Model, b.RouteClass)}
		}
	}

	d, starter := m.dispatcher, m.starter
	start := projectctl.StartRequest{
		NodeID: node.ID, Actor: "workdeck-" + chosen.ID, Role: node.OwnerRole,
		RouteClass: string(chosen.Class), Model: chosen.ID,
		Reason: "Work Deck 派工：owner 在界面上选定 " + chosen.ID,
	}
	if reason != "" {
		start.Reason += "；理由：" + reason
	}
	// 执行 brief 里的 actor 必须是账本里这次 attempt 的执行者，否则 submit 会被控制器拒绝（n047）：
	// 要开工的用这次 start 的 actor；已经开工的（补派）用账本记着的。
	actor := start.Actor
	if !needStart {
		actor, _ = node.Actor.Get()
	}
	root, script := m.snap.ProjectRoot, m.script
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		req := dispatch.Request{
			NodeID:        node.ID,
			Attempt:       attempt,
			RequiredClass: dispatch.RouteClass(node.RouteClass),
			ModelID:       chosen.ID,
			ProjectPath:   m.snap.ProjectRoot,
			Title:         node.ID,
			// 任务文本：没有它，派出去的就是个空会话（n039 attempt 3 评审）。登记写入权后经仲裁投递。
			// 按最终的 attempt 生成：开工派单要等 start 之后才知道号（n044）。
			MessageFor:   func(a int) string { return executorBrief(node, a, root, script, actor) },
			DispatchedBy: "work-deck",
			ChosenBy:     chosenBy,
			ChoiceReason: reason,
		}
		// 开工交给派工器在会话起来之后调（n044，n043 复审新问题 2）：launch 失败时 Graph 一点不动。
		// 这里记下开工拿到的号：派工后面几步失败时，节点已经 running，界面要照实显示。
		started, startedAttempt := "", 0
		if needStart {
			req.Attempt = 0
			req.Start = func(ctx context.Context) (dispatch.Started, error) {
				res, err := starter.Start(ctx, start)
				if err != nil {
					// 控制器说得清「确定没开工」的才这样标；别的错误派工器一律当结果不明（n044 attempt 3）。
					var ns *projectctl.ErrStartNotStarted
					if errors.As(err, &ns) {
						return dispatch.Started{}, dispatch.NotStarted(err)
					}
					return dispatch.Started{}, err
				}
				startedAttempt = res.Attempt
				started = fmt.Sprintf("Graph 已开工 attempt %d（%s）", res.Attempt, res.EventID)
				if res.Reconciled {
					started = fmt.Sprintf("Graph 已开工 attempt %d（回执没拿到，对账确认）", res.Attempt)
				}
				return dispatch.Started{Attempt: res.Attempt, EventID: res.EventID}, nil
			}
		}
		b, err := d.Dispatch(ctx, req)
		if err != nil {
			if startedAttempt > 0 {
				return dispatchDoneMsg{ok: false, text: started + "，但派工没成：" + err.Error(),
					nodeID: node.ID, attempt: startedAttempt, started: true}
			}
			if dispatch.IsStartUncertain(err) {
				// 开工结果不明：不说「被拒」，节点可能已经 running（n044 attempt 2 评审 blocker 2）。
				return dispatchDoneMsg{ok: false, text: "派工没成：" + err.Error()}
			}
			return dispatchDoneMsg{ok: false, text: "派工被拒：" + err.Error()}
		}
		prefix := ""
		if started != "" {
			prefix = started + "；"
		}
		if b.Supersedes != "" {
			prefix += fmt.Sprintf("注意：绑定存储里这个 attempt 原有一条旧绑定（会话 %s，属于别的账本历史），已挪进留档、没删，它若还在跑请手工处理；", b.Supersedes)
		}
		if b.Delivery == binding.DeliveryUnconfirmed {
			return dispatchDoneMsg{ok: false, text: fmt.Sprintf(
				"%s已派 %s attempt %d → 会话 %s（%s / %s），但执行 brief 送达未确认。%s",
				prefix, b.NodeID, b.Attempt, b.SessionID, b.Model, b.RouteClass, unconfirmedAdvice),
				nodeID: node.ID, attempt: b.Attempt, started: startedAttempt > 0 || state == projectctl.StateRunning}
		}
		return dispatchDoneMsg{ok: true, text: fmt.Sprintf(
			"%s已派 %s attempt %d → 会话 %s（%s / %s），执行 brief 已送达",
			prefix, b.NodeID, b.Attempt, b.SessionID, b.Model, b.RouteClass),
			nodeID: node.ID, attempt: b.Attempt, started: startedAttempt > 0 || state == projectctl.StateRunning}
	}
}

// View 画当前这一屏。
func (m Model) View() string {
	if m.loading && m.snap.ProjectRoot == "" {
		return "正在读取 Graph…\n"
	}
	return RenderScreen(m.snap, m.width, Screen{
		Cursor:          m.cursor,
		Picking:         m.mode == ModePickModel,
		PickingReviewer: m.pickingReviewer,
		AskingReason:    m.askingReason,
		Reason:          m.reason,
		AskingTrust:     m.askingTrust,
		TrustWarning:    m.trustWarning,
		Candidates:      m.candidates,
		Routing:         m.routing,
		Pick:            m.pick,
		Notice:          m.notice,
		NoticeOK:        m.noticeOK,
		Preview:         m.preview,
		PreviewFrom:     m.previewFrom,
		Detail:          m.detail,
		DetailOf:        m.detailOf,
		ShowAll:         m.showAll,
		EnterHint:       m.enterHint(),
		QueueOn:         m.queueOn(),
		LeaseLines:      m.leaseLines(),
		QueueOpen:       m.queueOpen(),
		Queue:           m.queueScreen(),
	})
}

// enterHint 是常驻在屏幕上的前缀键用法。没有接管能力时不显示。
func (m Model) enterHint() string {
	if m.attacher == nil {
		return ""
	}
	return m.attacher.NestingAdvice()
}
