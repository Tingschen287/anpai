package ui

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/binding"
	"github.com/Tingschen287/terminal-deck/work/internal/dispatch"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
	"github.com/Tingschen287/terminal-deck/work/internal/routing"
)

// 心跳不许压着取数堆。
//
// 真实项目上一轮完整取数要近两分钟，而心跳是 5 秒一拍。不挡一下的话，
// 取数会一轮压一轮，每轮又各开几个控制器进程，很快就把机器占满。
// 这个问题由 n035 的两位独立评审者各自发现并列为观察项。
func TestTickDoesNotStackFetchesWhileOneIsInFlight(t *testing.T) {
	m := New(nil)

	// 刚造出来就该算在途：Init 必定发起一轮取数。
	if !m.inflight {
		t.Fatal("新建的界面应当已经登记了在途取数")
	}

	// 在途时收到心跳，不该再起一轮，但心跳本身要续上。
	next, cmd := m.Update(tickMsg(time.Now()))
	m2 := next.(Model)
	if !m2.inflight {
		t.Error("在途状态被心跳弄丢了")
	}
	if cmd == nil {
		t.Error("心跳必须续上，否则界面从此不再自动刷新")
	}

	// 取数回来后解除在途。
	next, _ = m2.Update(snapshotMsg{})
	m3 := next.(Model)
	if m3.inflight {
		t.Error("取数回来后应当解除在途")
	}
	if m3.loading {
		t.Error("取数回来后不该还停在加载态")
	}

	// 空闲时的心跳才真正起一轮新的。
	next, cmd = m3.Update(tickMsg(time.Now()))
	m4 := next.(Model)
	if !m4.inflight {
		t.Error("空闲时的心跳应当起一轮新取数并登记在途")
	}
	if cmd == nil {
		t.Error("空闲时的心跳也要续上")
	}
}

// 手动刷新同样不许叠。
func TestManualRefreshIsIgnoredWhileOneIsInFlight(t *testing.T) {
	m := New(nil)
	if !m.inflight {
		t.Fatal("前提不成立")
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd != nil {
		t.Error("在途时按 r 不该再起一轮取数")
	}
	if !next.(Model).inflight {
		t.Error("在途状态不该被按键清掉")
	}
}

// fakeDispatcher 记下被请求的派工，并按脚本回应。
type fakeDispatcher struct {
	cands   []dispatch.Model
	got     []dispatch.Request
	reviews []dispatch.ReviewRequest
	// err 是上游起不来：真派工器这时不开工（n044）。
	err error
	// errAfterStart 是开工之后才失败（例如任务文本送不进去）：节点已经 running。
	errAfterStart error
}

func (f *fakeDispatcher) Candidates(required dispatch.RouteClass) []dispatch.Model {
	var out []dispatch.Model
	for _, m := range f.cands {
		if m.Class.Meets(required) {
			out = append(out, m)
		}
	}
	return out
}

// Route 用真实的路由判断，候选取自 cands；没有额度来源，额度一律 unknown。
func (f *fakeDispatcher) Route(nodeID string, attempt int, purpose string, required dispatch.RouteClass) routing.Decision {
	var cs []routing.Candidate
	for _, m := range f.cands {
		cs = append(cs, routing.Candidate{ID: m.ID, Tool: m.Tool, Class: string(m.Class), Caps: m.Caps})
	}
	return routing.Decide(routing.Request{NodeID: nodeID, Attempt: attempt, Purpose: purpose,
		RequiredClass: string(required), RequiredCapsSource: "测试：没有能力要求"}, cs, nil, time.Now())
}

func (f *fakeDispatcher) DispatchReviewer(ctx context.Context, req dispatch.ReviewRequest) (binding.Binding, error) {
	f.reviews = append(f.reviews, req)
	if f.err != nil {
		return binding.Binding{}, f.err
	}
	return binding.Binding{
		NodeID: dispatch.ReviewerKey(req.NodeID), Attempt: req.Attempt,
		SessionID: "review-sess", Model: req.ModelID, RouteClass: string(req.RequiredClass),
	}, nil
}

// Dispatch 照真派工器的顺序走（n044）：上游起不来就不开工；起来了才调 Start 拿 attempt；
// 任务文本按最终的 attempt 生成。got 记的是走到投递那一步的请求（Attempt、Message 已是最终值）。
func (f *fakeDispatcher) Dispatch(ctx context.Context, req dispatch.Request) (binding.Binding, error) {
	if f.err != nil {
		return binding.Binding{}, f.err
	}
	if req.Start != nil {
		a, err := req.Start(ctx)
		if err != nil {
			// 同真派工器：标了 NotStarted 的是确定没开工，别的一律是结果不明。
			if !dispatch.IsNotStarted(err) {
				return binding.Binding{}, &dispatch.StartUncertainError{Cause: err}
			}
			return binding.Binding{}, fmt.Errorf("Graph 开工失败（确定没开工）：%w。刚起的会话 sess-x 没拿到任务说明，已停掉", err)
		}
		req.Attempt = a.Attempt
	}
	if req.MessageFor != nil {
		req.Message = req.MessageFor(req.Attempt)
	}
	f.got = append(f.got, req)
	if f.errAfterStart != nil {
		return binding.Binding{}, f.errAfterStart
	}
	return binding.Binding{
		NodeID: req.NodeID, Attempt: req.Attempt, SessionID: "sess-x",
		Model: req.ModelID, RouteClass: string(req.RequiredClass),
	}, nil
}

func nodeWith(id string, state projectctl.State, class string) projectctl.Node {
	return projectctl.Node{
		ID: id, Title: id, RouteClass: class,
		State:   projectctl.Known(state),
		Attempt: projectctl.Unknown[int]("没开工过"),
	}
}

// fakeStarter 记下每一次开工请求，并按账本的样子回一个 attempt（默认 1）。
type fakeStarter struct {
	got     []projectctl.StartRequest
	attempt int
	err     error
}

func (f *fakeStarter) Start(ctx context.Context, r projectctl.StartRequest) (projectctl.StartResult, error) {
	f.got = append(f.got, r)
	if f.err != nil {
		return projectctl.StartResult{}, f.err
	}
	a := f.attempt
	if a == 0 {
		a = 1
	}
	return projectctl.StartResult{NodeID: r.NodeID, State: "running", Attempt: a, EventID: "evt_fake"}, nil
}

func modelWithDispatcher(t *testing.T, nodes []projectctl.Node, d Dispatcher) Model {
	t.Helper()
	m := New(nil).WithDispatcher(d).WithStarter(&fakeStarter{})
	m.snap = projectctl.Snapshot{Nodes: nodes, ProjectRoot: "/tmp/proj"}
	m.inflight = false
	m.loading = false
	return m
}

// modelWithDispatcherAndBindings 除了派工能力，还接上绑定存储。
//
// 要求独立评审的节点必须走这个：界面要先证明执行者跑在哪个会话，才谈得上派评审。
func modelWithDispatcherAndBindings(t *testing.T, nodes []projectctl.Node, d Dispatcher, b Bindings) Model {
	t.Helper()
	m := modelWithDispatcher(t, nodes, d)
	m.bindings = b
	return m
}

// execBound 造一条「这个节点这次 attempt 跑在某个会话里」的绑定。
func execBound(nodeID string, attempt int, session string) fakeBindings {
	return fakeBindings{
		fmt.Sprintf("%s#%d", nodeID, attempt): {NodeID: nodeID, Attempt: attempt, SessionID: session},
	}
}

func pressD(m Model) Model {
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	return next.(Model)
}

// 按了键没反应比报错更难查：每种拒绝都要把原因摆到屏幕上。
func TestDispatchRefusalsAlwaysExplainThemselves(t *testing.T) {
	fd := &fakeDispatcher{cands: []dispatch.Model{
		{ID: "claude-opus-5", Tool: "claude", Class: dispatch.Frontier},
	}}

	cases := []struct {
		name   string
		nodes  []projectctl.Node
		disp   Dispatcher
		expect string
	}{
		{
			name:   "节点不是 ready",
			nodes:  []projectctl.Node{nodeWith("n1", projectctl.StateAccepted, "frontier")},
			disp:   fd,
			expect: "submitted 的能派评审",
		},
		{
			name:   "没配派工能力",
			nodes:  []projectctl.Node{nodeWith("n1", projectctl.StateReady, "frontier")},
			disp:   nil,
			expect: "没有配置派工",
		},
		{
			name:   "节点没写 route class",
			nodes:  []projectctl.Node{nodeWith("n1", projectctl.StateReady, "")},
			disp:   fd,
			expect: "拒绝派工，不猜",
		},
		{
			name:   "档位表里没有够格的模型",
			nodes:  []projectctl.Node{nodeWith("n1", projectctl.StateReady, "human")},
			disp:   fd,
			expect: "拒绝降档",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := modelWithDispatcher(t, c.nodes, c.disp)
			if c.disp == nil {
				m.dispatcher = nil
			}
			after := pressD(m)
			if after.mode == ModePickModel {
				t.Fatal("不该进入挑模型界面")
			}
			if after.noticeOK {
				t.Error("拒绝不该标成成功")
			}
			if !strings.Contains(after.notice, c.expect) {
				t.Errorf("提示里没说清原因，期望含 %q，实际 %q", c.expect, after.notice)
			}
			// 屏幕上真的要看得见。
			out := RenderScreen(after.snap, 100, Screen{
				Cursor: after.cursor, Notice: after.notice, NoticeOK: after.noticeOK,
			})
			if !strings.Contains(out, c.expect) {
				t.Errorf("拒绝原因没画到屏幕上：%q", out)
			}
		})
	}
}

// 状态还不知道的节点不许派。
func TestDispatchRefusesNodeWithUnknownState(t *testing.T) {
	n := nodeWith("n1", projectctl.StateReady, "frontier")
	n.State = projectctl.Unknown[projectctl.State]("查询中")
	m := modelWithDispatcher(t, []projectctl.Node{n}, &fakeDispatcher{
		cands: []dispatch.Model{{ID: "m", Tool: "t", Class: dispatch.Frontier}},
	})
	after := pressD(m)
	if after.mode == ModePickModel {
		t.Fatal("状态未知时不该进入挑模型界面")
	}
	if !strings.Contains(after.notice, "状态还不知道") {
		t.Errorf("没说清为什么不派：%q", after.notice)
	}
}

// 正常路径：挑模型，回车，派出去。
func TestDispatchHappyPathPassesTheChosenModel(t *testing.T) {
	fd := &fakeDispatcher{cands: []dispatch.Model{
		{ID: "claude-sonnet-5", Tool: "claude", Class: dispatch.Balanced},
		{ID: "claude-opus-5", Tool: "claude", Class: dispatch.Frontier},
	}}
	m := modelWithDispatcher(t, []projectctl.Node{
		nodeWith("n1", projectctl.StateReady, "balanced"),
	}, fd)

	m = pressD(m)
	if m.mode != ModePickModel {
		t.Fatal("ready 节点按 d 应当进入挑模型界面")
	}
	if len(m.candidates) != 2 {
		t.Fatalf("候选数是 %d，期望 2", len(m.candidates))
	}
	// 第一个候选应当是刚好够用的那档，不是最贵的。
	if m.candidates[0].Class != dispatch.Balanced {
		t.Errorf("第一个候选是 %s，期望刚好够用的 balanced", m.candidates[0].Class)
	}

	// 往下挑一个，回车。
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(Model)
	m, cmd := enterWithReason(t, m, "测试 挑贵的那个")
	if cmd == nil {
		t.Fatal("回车应当真的发起一次派工")
	}
	msg := cmd()
	done, ok := msg.(dispatchDoneMsg)
	if !ok {
		t.Fatalf("派工应当回报结果，实际是 %T", msg)
	}
	if !done.ok {
		t.Fatalf("派工不该失败：%s", done.text)
	}
	if len(fd.got) != 1 {
		t.Fatalf("应当只派一次，实际 %d 次", len(fd.got))
	}
	if fd.got[0].ModelID != "claude-opus-5" {
		t.Errorf("派出去的模型是 %q，期望挑中的 claude-opus-5", fd.got[0].ModelID)
	}
	if fd.got[0].NodeID != "n1" || fd.got[0].Attempt != 1 {
		t.Errorf("派工请求不对：%+v", fd.got[0])
	}
	if !strings.Contains(fd.got[0].Message, "n1 的执行者") {
		t.Errorf("执行派工没带任务文本：%q", fd.got[0].Message)
	}
	if fd.got[0].ChosenBy != "owner" || fd.got[0].ChoiceReason != "测试 挑贵的那个" {
		t.Errorf("选择者与理由没传下去：%q %q", fd.got[0].ChosenBy, fd.got[0].ChoiceReason)
	}
}

// enterWithReason 按回车；没采纳推荐时界面要先收理由，就打字再回车。
func enterWithReason(t *testing.T, m Model, reason string) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if !m.askingReason {
		return m, cmd
	}
	if cmd != nil {
		t.Fatal("要写理由时回车不该直接派出")
	}
	for _, r := range reason {
		k := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		if r == ' ' {
			k = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		}
		next, _ = m.Update(k)
		m = next.(Model)
	}
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return next.(Model), cmd
}

// 没有推荐时挑一个：先问理由；空理由不派；Esc 回去重挑；屏幕上说清为什么要理由。
func TestPickingWithoutARecommendationAsksForAReason(t *testing.T) {
	fd := &fakeDispatcher{cands: []dispatch.Model{{ID: "claude-opus-5", Tool: "claude", Class: dispatch.Frontier}}}
	m := modelWithDispatcher(t, []projectctl.Node{nodeWith("n1", projectctl.StateReady, "frontier")}, fd)
	m = pressD(m)
	if m.mode != ModePickModel {
		t.Fatalf("前提：应当进入挑模型：%q", m.notice)
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if cmd != nil || !m.askingReason {
		t.Fatal("没有推荐时回车应当先问理由，不直接派")
	}
	out := RenderScreen(m.snap, 120, Screen{Picking: true, Candidates: m.candidates, Routing: m.routing, AskingReason: m.askingReason, Reason: m.reason})
	if !strings.Contains(out, "写一句为什么选 claude-opus-5") {
		t.Errorf("屏幕上没问理由：\n%s", out)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	m = next.(Model)
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if cmd != nil || !strings.Contains(m.notice, "理由不能为空") {
		t.Errorf("空白理由不该派出：%q", m.notice)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if m.askingReason || m.mode != ModePickModel {
		t.Error("Esc 应当回到挑模型，不是退出")
	}
	m, cmd = enterWithReason(t, m, "唯一够档的")
	if cmd == nil {
		t.Fatal("写了理由应当派出")
	}
	cmd()
	if len(fd.got) != 1 || fd.got[0].ChoiceReason != "唯一够档的" {
		t.Errorf("理由没传给派工器：%+v", fd.got)
	}
}

// Esc 取消要说一声，不能默默回去。
func TestEscapeCancelsAndSaysSo(t *testing.T) {
	m := modelWithDispatcher(t, []projectctl.Node{
		nodeWith("n1", projectctl.StateReady, "frontier"),
	}, &fakeDispatcher{cands: []dispatch.Model{{ID: "m", Tool: "t", Class: dispatch.Frontier}}})
	m = pressD(m)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	after := next.(Model)
	if after.mode != ModeBrowse {
		t.Error("Esc 应当退出挑模型界面")
	}
	if !strings.Contains(after.notice, "已取消") {
		t.Errorf("取消了却没说一声：%q", after.notice)
	}
}

// fakeAttacher 记下被请求的会话，按脚本回应。
type fakeAttacher struct {
	attached []string
	previews []string
	cmdErr   error
	// direct 记下 T 那条路被走了几次，directErr 让它失败。
	direct    []string
	directErr error
	// directNote 是 adapter 退到次一等来源时给出的说明。
	directNote string
	// directCtx 留下界面传进来的那个 context，好在外面验它的生命周期。
	directCtx context.Context
	text      string
	textErr   error
	advice    string
}

// DirectAttachCommand 是按 T 走的那条。directNote 模拟 adapter 退到次一等来源。
func (f *fakeAttacher) DirectAttachCommand(ctx context.Context, sessionID string) (*exec.Cmd, string, error) {
	f.direct = append(f.direct, sessionID)
	f.directCtx = ctx
	if f.directErr != nil {
		return nil, "", f.directErr
	}
	return exec.Command("true"), f.directNote, nil
}

func (f *fakeAttacher) AttachCommand(ctx context.Context, sessionID string) (*exec.Cmd, error) {
	f.attached = append(f.attached, sessionID)
	if f.cmdErr != nil {
		return nil, f.cmdErr
	}
	return exec.Command("true"), nil
}

func (f *fakeAttacher) PaneOutputPlain(ctx context.Context, sessionID string) (string, error) {
	f.previews = append(f.previews, sessionID)
	return f.text, f.textErr
}

func (f *fakeAttacher) NestingAdvice() string {
	if f.advice == "" {
		return "进入后按 C-b d 退出会话。"
	}
	return f.advice
}

// fakeBindings 是一张写死的绑定表。
type fakeBindings map[string]binding.Binding

func (b fakeBindings) Get(nodeID string, attempt int) (binding.Binding, bool) {
	v, ok := b[fmt.Sprintf("%s#%d", nodeID, attempt)]
	return v, ok
}

func runningNode(id string, attempt int) projectctl.Node {
	n := nodeWith(id, projectctl.StateRunning, "frontier")
	n.Attempt = projectctl.Known(attempt)
	return n
}

func modelWithAttacher(t *testing.T, nodes []projectctl.Node, a Attacher, b Bindings) Model {
	t.Helper()
	m := New(nil).WithAttacher(a, b)
	m.snap = projectctl.Snapshot{Nodes: nodes, ProjectRoot: "/tmp/proj"}
	m.inflight, m.loading = false, false
	return m
}

func press(m Model, r rune) (Model, tea.Cmd) {
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	return next.(Model), cmd
}

// 没派过工的节点进不去，而且要说清为什么，不能按了没反应。
func TestAttachRefusalsExplainThemselves(t *testing.T) {
	fa := &fakeAttacher{}
	cases := []struct {
		name   string
		nodes  []projectctl.Node
		binds  Bindings
		expect string
	}{
		{
			name:   "没有绑定会话",
			nodes:  []projectctl.Node{runningNode("n1", 1)},
			binds:  fakeBindings{},
			expect: "先按 d 派工",
		},
		{
			name:   "attempt 未知",
			nodes:  []projectctl.Node{nodeWith("n1", projectctl.StateRunning, "frontier")},
			binds:  fakeBindings{},
			expect: "先按 d 派工",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := modelWithAttacher(t, c.nodes, fa, c.binds)
			after, cmd := press(m, 'a')
			if cmd != nil {
				t.Error("被拒绝时不该真的去交出终端")
			}
			if after.noticeOK {
				t.Error("拒绝不该标成成功")
			}
			if !strings.Contains(after.notice, c.expect) {
				t.Errorf("没说清原因，期望含 %q，实际 %q", c.expect, after.notice)
			}
			// 拒绝必须带出路。只说「不行」和按了没反应差不多。
			if !strings.Contains(after.notice, "按 d") {
				t.Errorf("拒绝没告诉人下一步该做什么：%q", after.notice)
			}
		})
	}
}

// 没配进入能力时，按 a 要说缺什么。
func TestAttachWithoutAttacherSaysWhatIsMissing(t *testing.T) {
	m := New(nil)
	m.snap = projectctl.Snapshot{Nodes: []projectctl.Node{runningNode("n1", 1)}}
	m.inflight, m.loading = false, false
	after, cmd := press(m, 'a')
	if cmd != nil {
		t.Error("没配进入能力时不该交出终端")
	}
	if !strings.Contains(after.notice, "没有配置进入会话") {
		t.Errorf("没说清缺什么：%q", after.notice)
	}
}

// 正常进入：要先把前缀键的事讲清楚，再交出终端。
func TestAttachTellsAboutPrefixKeyBeforeHandingOverTheTerminal(t *testing.T) {
	fa := &fakeAttacher{advice: "两层 tmux，按两次 C-b"}
	binds := fakeBindings{"n1#1": {NodeID: "n1", Attempt: 1, SessionID: "sess-7"}}
	m := modelWithAttacher(t, []projectctl.Node{runningNode("n1", 1)}, fa, binds)

	after, cmd := press(m, 'a')
	if cmd == nil {
		t.Fatal("正常情况应当交出终端")
	}
	if len(fa.attached) != 1 || fa.attached[0] != "sess-7" {
		t.Errorf("进错了会话：%v", fa.attached)
	}
	// 进去之后就看不见 Work Deck 了，所以话必须在进去之前说。
	// 这里只证明 notice 字段与渲染结果里有这句话；真终端上它只停一帧，
	// 人能读到的是常驻那一行，见 TestPrefixKeyAdviceIsOnScreenBeforeAnyKeyPress。
	if !strings.Contains(after.notice, "按两次 C-b") {
		t.Errorf("进入前没讲前缀键：%q", after.notice)
	}
	out := RenderScreen(after.snap, 100, Screen{Notice: after.notice, NoticeOK: after.noticeOK})
	if !strings.Contains(out, "按两次 C-b") {
		t.Error("前缀键提示没画到屏幕上")
	}
}

// 前缀键用法要在按下 a/T 之前就常驻在屏幕上。
//
// 上一条测试断言的 notice 与交出终端发生在同一次 Update：bubbletea 交出终端时
// 先停渲染器再退出全屏界面，那行 notice 在真终端上最多停一帧，回来后又被「已返回」盖掉。
// 所以人真正能读到的是这一行。
func TestPrefixKeyAdviceIsOnScreenBeforeAnyKeyPress(t *testing.T) {
	fa := &fakeAttacher{advice: "两层 tmux，按两次 C-b"}
	m := modelWithAttacher(t, []projectctl.Node{runningNode("n1", 1)}, fa, fakeBindings{})
	if !strings.Contains(m.View(), "按两次 C-b") {
		t.Error("还没按键，屏幕上就该有前缀键用法")
	}
	if strings.Contains(New(nil).View(), "C-b") {
		t.Error("没有接管能力时不该提示进入会话的按键")
	}
}

// 常驻那一行要在最窄的 60 列里也整句看得到。
//
// Bubble Tea 会截掉超出终端宽度的单行。真实的嵌套提示里「按两次」在第 70 多列，
// 不折行的话 60 列终端上恰好看不到关键那半句。
func TestPrefixKeyAdviceFitsANarrowTerminal(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-test/default,1,0") // 让 adapter 按嵌套给出原话
	advice := agentdeck.NestingAdvice("")
	if !strings.Contains(advice, "要操作内层得按两次") {
		t.Fatalf("adapter 的嵌套提示变了，这条测试要跟着改：%q", advice)
	}
	fa := &fakeAttacher{advice: advice}
	m := modelWithAttacher(t, []projectctl.Node{runningNode("n1", 1)}, fa, fakeBindings{})
	m.width = 60
	ansi := regexp.MustCompile("\x1b\\[[0-9;]*m")
	plain := ansi.ReplaceAllString(m.View(), "")
	// 只量提示那几行：表盘别的行在 60 列下也会超宽，那不是这条测试的范围。
	var hintLines int
	for _, ln := range strings.Split(plain, "\n") {
		if ln == "" || !strings.Contains(advice, ln) {
			continue
		}
		hintLines++
		if cells := lipgloss.Width(ln); cells > 60 {
			t.Errorf("提示有一行 %d 列，超出 60 列会被截断：%q", cells, ln)
		}
	}
	if hintLines == 0 {
		t.Fatal("屏幕上找不到提示的任何一行")
	}
	if !strings.Contains(strings.ReplaceAll(plain, "\n", ""), "要操作内层得按两次") {
		t.Error("60 列下看不到「按两次」那半句")
	}
}

// 从会话回来要刷新状态，不能停在进去之前那一刻。
func TestReturningFromSessionRefreshesState(t *testing.T) {
	fa := &fakeAttacher{}
	binds := fakeBindings{"n1#1": {NodeID: "n1", Attempt: 1, SessionID: "sess-7"}}
	m := modelWithAttacher(t, []projectctl.Node{runningNode("n1", 1)}, fa, binds)

	next, cmd := m.Update(attachDoneMsg{sessionID: "sess-7"})
	after := next.(Model)
	if cmd == nil {
		t.Error("回来后应当重取一次状态")
	}
	if !after.inflight {
		t.Error("重取应当登记在途")
	}
	if !strings.Contains(after.notice, "已从会话 sess-7 返回") {
		t.Errorf("没说清已经回来了：%q", after.notice)
	}
	if !after.noticeOK {
		t.Error("正常返回不该标成失败")
	}
}

func TestReturningWithErrorSaysSo(t *testing.T) {
	fa := &fakeAttacher{}
	binds := fakeBindings{"n1#1": {NodeID: "n1", Attempt: 1, SessionID: "s"}}
	m := modelWithAttacher(t, []projectctl.Node{runningNode("n1", 1)}, fa, binds)
	next, _ := m.Update(attachDoneMsg{sessionID: "s", err: fmt.Errorf("终端炸了")})
	after := next.(Model)
	if after.noticeOK {
		t.Error("进入出错不该标成成功")
	}
	if !strings.Contains(after.notice, "终端炸了") {
		t.Errorf("没把错误说出来：%q", after.notice)
	}
}

// 预览是只读的，画面要显示出来，并且标明只读。
func TestPreviewShowsPaneTextAndSaysItIsReadOnly(t *testing.T) {
	fa := &fakeAttacher{text: "第一行\n第二行\n第三行"}
	binds := fakeBindings{"n1#1": {NodeID: "n1", Attempt: 1, SessionID: "sess-9"}}
	m := modelWithAttacher(t, []projectctl.Node{runningNode("n1", 1)}, fa, binds)

	_, cmd := press(m, 'p')
	if cmd == nil {
		t.Fatal("按 p 应当去取预览")
	}
	msg := cmd()
	done, ok := msg.(previewDoneMsg)
	if !ok {
		t.Fatalf("应当回报预览结果，实际是 %T", msg)
	}
	if done.err != nil {
		t.Fatalf("预览不该出错：%v", done.err)
	}
	next, _ := m.Update(done)
	after := next.(Model)
	if after.preview == "" {
		t.Fatal("预览内容没存下来")
	}

	out := RenderScreen(after.snap, 100, Screen{
		Preview: after.preview, PreviewFrom: after.previewFrom,
	})
	for _, want := range []string{"第三行", "只读", "不接管", "sess-9"} {
		if !strings.Contains(out, want) {
			t.Errorf("预览画面里缺 %q", want)
		}
	}
	// 预览不该触发任何进入动作。
	if len(fa.attached) != 0 {
		t.Error("预览不该接管会话")
	}
}

func TestPreviewFailureIsShownNotSwallowed(t *testing.T) {
	fa := &fakeAttacher{textErr: fmt.Errorf("会话没了")}
	binds := fakeBindings{"n1#1": {NodeID: "n1", Attempt: 1, SessionID: "s"}}
	m := modelWithAttacher(t, []projectctl.Node{runningNode("n1", 1)}, fa, binds)
	_, cmd := press(m, 'p')
	next, _ := m.Update(cmd())
	after := next.(Model)
	if after.preview != "" {
		t.Error("取失败时不该留下旧预览")
	}
	if !strings.Contains(after.notice, "会话没了") {
		t.Errorf("没把错误说出来：%q", after.notice)
	}
}

// 进入与预览两条路上的拒绝出口，全部列在 refusal_paths_test.go 里逐条测。
//
// 这里原先有一张九条的表，宣称「每种拒绝都带出路」。独立评审指出它与源码对不上：
// 预览那条路只列了两个出口，找会话的四个分支在预览侧一个都没测，
// 从会话返回时报错也漏了。实际是 15 个出口、11 个分支。
// 那张表已删，口径和全集见 refusal_paths_test.go 顶部。

// 预览取数失败也要带出路。
func TestPreviewFailureOffersANextStep(t *testing.T) {
	fa := &fakeAttacher{textErr: fmt.Errorf("会话没了")}
	binds := fakeBindings{"n1#1": {NodeID: "n1", Attempt: 1, SessionID: "s"}}
	m := modelWithAttacher(t, []projectctl.Node{runningNode("n1", 1)}, fa, binds)
	_, cmd := press(m, 'p')
	next, _ := m.Update(cmd())
	after := next.(Model)
	if !strings.Contains(after.notice, "按 r") {
		t.Errorf("预览失败没给下一步：%q", after.notice)
	}
}

func submittedNode(id string, attempt int, actor string) projectctl.Node {
	n := nodeWith(id, projectctl.StateSubmitted, "frontier")
	n.Attempt = projectctl.Known(attempt)
	n.Actor = projectctl.Known(actor)
	n.ReviewRequired = true
	n.ReviewRouteClass = "frontier"
	n.ReviewIndependent = true
	return n
}

// 从 submitted 节点派评审，这条路必须在界面上走得通。
//
// 上一版只有 domain 层的 DispatchReviewer，界面根本没有入口，
// owner 选中 submitted 节点按 d 会被当成派工拒掉。
func TestSubmittedNodeDispatchesAReviewerNotAnExecutor(t *testing.T) {
	fd := &fakeDispatcher{cands: []dispatch.Model{
		{ID: "claude-opus-5", Tool: "claude", Class: dispatch.Frontier},
	}}
	m := modelWithDispatcherAndBindings(t, []projectctl.Node{
		submittedNode("n1", 2, "executor-a1"),
	}, fd, execBound("n1", 2, "exec-sess"))

	m = pressD(m)
	if m.mode != ModePickModel {
		t.Fatalf("submitted 节点按 d 应当进入挑模型，实际提示：%q", m.notice)
	}
	if !m.pickingReviewer {
		t.Error("这次挑模型是为了派评审，界面没记住")
	}
	// 屏幕上要说清楚在挑什么。
	out := RenderScreen(m.snap, 100, Screen{
		Picking: true, PickingReviewer: m.pickingReviewer, Candidates: m.candidates,
	})
	if !strings.Contains(out, "派评审") {
		t.Error("挑模型时没说清是在派评审还是派工")
	}

	m, cmd := enterWithReason(t, m, "评审用它")
	if cmd == nil {
		t.Fatal("回车应当真的派出评审")
	}
	msg := cmd()
	done, ok := msg.(dispatchDoneMsg)
	if !ok || !done.ok {
		t.Fatalf("派评审不该失败：%+v", msg)
	}
	// 走的必须是评审那条路，不是派工那条。
	if len(fd.reviews) != 1 {
		t.Fatalf("应当派出一次评审，实际 %d 次", len(fd.reviews))
	}
	if len(fd.got) != 0 {
		t.Error("submitted 节点不该走派工那条路")
	}
	r := fd.reviews[0]
	if r.ExecutorActor != "executor-a1" {
		t.Errorf("没把执行者是谁传下去：%q", r.ExecutorActor)
	}
	if !r.IndependentRequired {
		t.Error("节点要求独立评审，这个要求没传下去")
	}
	if r.RequiredClass != dispatch.Frontier {
		t.Errorf("评审档位是 %s，期望 frontier", r.RequiredClass)
	}
}

// 要求独立评审但不知道执行者是谁，就不能派。
func TestReviewDispatchRefusedWhenExecutorUnknown(t *testing.T) {
	fd := &fakeDispatcher{cands: []dispatch.Model{
		{ID: "claude-opus-5", Tool: "claude", Class: dispatch.Frontier},
	}}
	n := submittedNode("n1", 2, "")
	n.Actor = projectctl.Unknown[string]("状态投影已过期")
	m := modelWithDispatcherAndBindings(t, []projectctl.Node{n}, fd, execBound("n1", 2, "exec-sess"))

	after := pressD(m)
	if after.mode == ModePickModel {
		t.Fatal("执行者未知时不该进入挑模型")
	}
	if !strings.Contains(after.notice, "无法判断独立性") {
		t.Errorf("没说清为什么不派：%q", after.notice)
	}
	if !strings.Contains(after.notice, "按 r") {
		t.Errorf("拒绝没给下一步：%q", after.notice)
	}
}

// 不要求评审的节点，不给派。
func TestNodeThatNeedsNoReviewIsNotDispatchable(t *testing.T) {
	fd := &fakeDispatcher{cands: []dispatch.Model{{ID: "m", Tool: "t", Class: dispatch.Frontier}}}
	n := submittedNode("n1", 1, "a1")
	n.ReviewRequired = false
	after := pressD(modelWithDispatcher(t, []projectctl.Node{n}, fd))
	if after.mode == ModePickModel {
		t.Error("不要求评审的节点不该进入挑模型")
	}
	if !strings.Contains(after.notice, "不要求评审") {
		t.Errorf("没说清原因：%q", after.notice)
	}
}

// 既不是 ready 也不是 submitted 的，两条都不给走，且要说清哪些能走。
func TestOtherStatesExplainWhichStatesAreDispatchable(t *testing.T) {
	fd := &fakeDispatcher{cands: []dispatch.Model{{ID: "m", Tool: "t", Class: dispatch.Frontier}}}
	after := pressD(modelWithDispatcher(t, []projectctl.Node{
		nodeWith("n1", projectctl.StateBlocked, "frontier"),
	}, fd))
	if after.mode == ModePickModel {
		t.Error("blocked 节点不该能派")
	}
	for _, want := range []string{"ready", "submitted"} {
		if !strings.Contains(after.notice, want) {
			t.Errorf("没说清哪些状态能派（缺 %q）：%q", want, after.notice)
		}
	}
}

// 界面给 adapter 的那个 context，在命令真正启动之前就已经取消了。
//
// 这不是缺陷，是分工：20 秒上限管的是「解析目标」这一段——问菜单、跑 list、
// 扫 socket——免得一个僵死的 tmux server 把整个界面钉住。解析一结束，
// beginDirectAttach 就返回，`defer cancel()` 当场执行；而命令要等 Bubble Tea
// 挂起界面之后才跑。
//
// 把这件事写成测试，是因为 attempt 9 在这里栽过：adapter 当时用
// exec.CommandContext 把这个 context 绑给了 attach 命令，于是解析越顺利，
// 失败得越准时——界面显示「直连解析成功，进入时 context canceled」，重试无用。
//
// 所以规矩是：**这个 context 只许用来解析，不许绑给要交出终端的命令。**
// adapter 那边由 TestInteractiveAttachCommandsOutliveTheResolveContext 守着。
func TestResolveDeadlineIsAlreadyDoneWhenTheCommandRuns(t *testing.T) {
	fake := &fakeAttacher{}
	m := modelWithAttacher(t, []projectctl.Node{runningNode("n1", 1)}, fake, execBound("n1", 1, "sess-7"))

	next, cmd := m.beginDirectAttach()
	if cmd == nil {
		t.Fatal("按 T 应当给出一条要执行的命令")
	}
	if !next.(Model).noticeOK {
		t.Fatalf("解析应当成功，实际提示：%s", next.(Model).notice)
	}
	if fake.directCtx == nil {
		t.Fatal("adapter 没拿到 context")
	}
	if fake.directCtx.Err() == nil {
		t.Fatal("解析用的 context 在函数返回后仍未取消，那 20 秒上限就是摆设")
	}
	// 命令此刻才轮到执行——它不能因为上面那个 context 已经取消而跑不起来。
	if _, ok := fake.directCtx.Deadline(); !ok {
		t.Error("解析用的 context 应当带 deadline")
	}
}
