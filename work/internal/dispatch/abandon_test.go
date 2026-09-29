package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/binding"
)

// seqLauncher 把 launch / send / abandon 与 Graph 开工按发生顺序记进同一张表（n044）。
// 只看「有没有调」看不出顺序；本轮要证明的正是顺序：launch → 核工具 → start → send。
type seqLauncher struct {
	seq        []string
	res        agentdeck.LaunchResult
	launchErr  error
	sendErr    error
	abandonErr error
}

func (l *seqLauncher) Launch(ctx context.Context, req agentdeck.LaunchRequest) (agentdeck.LaunchResult, error) {
	l.seq = append(l.seq, "launch")
	if l.launchErr != nil {
		return agentdeck.LaunchResult{}, l.launchErr
	}
	return l.res, nil
}

func (l *seqLauncher) SendMessage(ctx context.Context, sessionID, message string) error {
	l.seq = append(l.seq, "send "+sessionID)
	return l.sendErr
}

func (l *seqLauncher) Abandon(ctx context.Context, sessionID string) error {
	l.seq = append(l.seq, "abandon "+sessionID)
	return l.abandonErr
}

// startRequest 是一次要开工的派单：attempt 留 0，Start 记进同一张顺序表并给出 attempt。
func startRequest(l *seqLauncher, attempt int, startErr error) Request {
	req := goodRequest()
	req.Attempt = 0
	req.Start = func(context.Context) (Started, error) {
		l.seq = append(l.seq, "start")
		return Started{Attempt: attempt, EventID: "evt_test"}, startErr
	}
	return req
}

func agentLaunch(id string) agentdeck.LaunchResult {
	return agentdeck.LaunchResult{SessionID: id, Success: true, Tool: "claude"}
}

func assertSeq(t *testing.T, l *seqLauncher, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(l.seq, want) {
		t.Errorf("调用顺序是 %v，期望 %v", l.seq, want)
	}
}

// 成功的开工派单：先起会话，会话起来才开工，开工拿到的 attempt 就是绑定的 attempt，最后才送任务。
func TestStartHappensAfterLaunchAndBeforeSend(t *testing.T) {
	l := &seqLauncher{res: agentLaunch("s1")}
	d, logPath := newDispatcher(t, l)
	b, err := d.Dispatch(context.Background(), startRequest(l, 3, nil))
	if err != nil {
		t.Fatalf("应当派成：%v", err)
	}
	assertSeq(t, l, "launch", "start", "send s1")
	if b.Attempt != 3 {
		t.Errorf("绑定应当用账本给的 attempt 3：%+v", b)
	}
	if got, ok := d.Store.Get("n099_demo", 3); !ok || got.SessionID != "s1" {
		t.Errorf("绑定存储里应当有 attempt 3 → s1：%+v %v", got, ok)
	}
	logs := readLog(t, logPath)
	if last := logs[len(logs)-1]; !last.OK || last.Attempt != 3 || last.RoutingAt.IsZero() {
		t.Errorf("流水应当记 attempt 3 和对应路由记录的时间：%+v", last)
	}
}

// launch 失败：Graph 一点不动（不调 start），也没有会话要停。
func TestLaunchFailureNeverStartsTheGraph(t *testing.T) {
	l := &seqLauncher{launchErr: errors.New("上游起不来")}
	d, _ := newDispatcher(t, l)
	_, err := d.Dispatch(context.Background(), startRequest(l, 2, nil))
	if err == nil || !strings.Contains(err.Error(), "Graph 没有开工") {
		t.Fatalf("launch 失败应当拒绝并说明 Graph 没动：%v", err)
	}
	assertSeq(t, l, "launch")
	if len(d.Store.List()) != 0 {
		t.Error("不该落绑定")
	}
}

// start 确定没开工：停掉刚起的空会话，任务不送、不落绑定，说清节点状态不变。
// 结果不明（没标 NotStarted 的错误）：同样停掉空会话，但不说没开工，请 owner 刷新核对（n044 attempt 2 评审 blocker 2）。
func TestStartFailureStopsTheFreshSession(t *testing.T) {
	l := &seqLauncher{res: agentLaunch("s1")}
	d, _ := newDispatcher(t, l)
	_, err := d.Dispatch(context.Background(), startRequest(l, 0, NotStarted(errors.New("控制器拒绝开工"))))
	if err == nil || !strings.Contains(err.Error(), "确定没开工") || !strings.Contains(err.Error(), "已停掉") || IsStartUncertain(err) {
		t.Fatalf("确定没开工时应当拒绝，并说明会话已停：%v", err)
	}
	assertSeq(t, l, "launch", "start", "abandon s1")

	l = &seqLauncher{res: agentLaunch("s1")}
	d, logPath := newDispatcher(t, l)
	_, err = d.Dispatch(context.Background(), startRequest(l, 0, errors.New("子进程超时")))
	if err == nil || !IsStartUncertain(err) || !strings.Contains(err.Error(), "可能已经开工") || !strings.Contains(err.Error(), "按 d 补派") ||
		strings.Contains(err.Error(), "确定没开工") || !strings.Contains(err.Error(), "已停掉") {
		t.Fatalf("结果不明时不能说没开工，要说可能已开工、怎么核对：%v", err)
	}
	assertSeq(t, l, "launch", "start", "abandon s1")
	if len(d.Store.List()) != 0 {
		t.Error("不该落绑定")
	}
	logs := readLog(t, logPath)
	if last := logs[len(logs)-1]; last.OK || !last.SessionStopped || last.SessionID != "s1" {
		t.Errorf("流水应当记下被停掉的会话：%+v", last)
	}
}

// 停会话本身失败：照实说，请人处理。
func TestAbandonFailureIsReported(t *testing.T) {
	l := &seqLauncher{res: agentLaunch("s1"), abandonErr: errors.New("有人连着")}
	d, logPath := newDispatcher(t, l)
	_, err := d.Dispatch(context.Background(), startRequest(l, 0, NotStarted(errors.New("控制器拒绝开工"))))
	if err == nil || !strings.Contains(err.Error(), "也没停掉") || !strings.Contains(err.Error(), "有人连着") {
		t.Fatalf("停不掉要说清楚：%v", err)
	}
	logs := readLog(t, logPath)
	if last := logs[len(logs)-1]; last.SessionStopped {
		t.Errorf("没停掉就不能记成已停：%+v", last)
	}
}

// 开工之后投递失败：会话没拿到任务，停掉；节点已是 running，说清楚按 d 补派同一个 attempt。
func TestSendFailureAfterStartStopsTheSessionAndSaysRunning(t *testing.T) {
	l := &seqLauncher{res: agentLaunch("s1"), sendErr: errors.New("send_failed")}
	d, _ := newDispatcher(t, l)
	_, err := d.Dispatch(context.Background(), startRequest(l, 2, nil))
	if err == nil || !strings.Contains(err.Error(), "Graph 已开工 attempt 2") || !strings.Contains(err.Error(), "按 d 补派") {
		t.Fatalf("应当说明节点已开工、怎么补派：%v", err)
	}
	assertSeq(t, l, "launch", "start", "send s1", "abandon s1")
	if len(d.Store.List()) != 0 {
		t.Error("不该落绑定")
	}
}

// 送达未确认不停会话：它可能已经在跑（n042 规矩不变）。
func TestUnconfirmedDeliveryAfterStartIsBoundNotStopped(t *testing.T) {
	l := &seqLauncher{res: agentLaunch("s1"), sendErr: &agentdeck.ErrDeliveryUnconfirmed{SessionID: "s1", Detail: "typed", Reached: true}}
	d, _ := newDispatcher(t, l)
	b, err := d.Dispatch(context.Background(), startRequest(l, 2, nil))
	if err != nil || b.Delivery != binding.DeliveryUnconfirmed || b.Attempt != 2 {
		t.Fatalf("送达未确认应当落绑定并记 unconfirmed：%+v %v", b, err)
	}
	assertSeq(t, l, "launch", "start", "send s1")
}

// 要开工的派单不许预填 attempt：号只能来自 projectctl start。
func TestStartDispatchRefusesAPrefilledAttempt(t *testing.T) {
	l := &seqLauncher{res: agentLaunch("s1")}
	d, _ := newDispatcher(t, l)
	req := startRequest(l, 2, nil)
	req.Attempt = 1
	if _, err := d.Dispatch(context.Background(), req); err == nil || !strings.Contains(err.Error(), "不许预填 attempt") {
		t.Fatalf("应当拒绝预填的 attempt：%v", err)
	}
	assertSeq(t, l)
}

func staleBinding() binding.Binding {
	return binding.Binding{NodeID: "n099_demo", Attempt: 2, SessionID: "s-old", Tool: "claude", Model: "claude-opus-5",
		RouteClass: "frontier", ProjectPath: "/tmp/p", Profile: "_t", DispatchedBy: "earlier", StartEvent: "evt_old_ledger"}
}

// 绑定存储里已有账本刚分配的 attempt（例如夹具重建前留下的旧绑定）：不覆盖、不删，挪进留档；新会话绑上，
// 绑定与流水都点名被顶替的旧会话（n044 attempt 2 评审 blocker 1：旧会话可能还在跑，owner 要找得到它）。
func TestStaleBindingForTheStartedAttemptIsSupersededNotLost(t *testing.T) {
	l := &seqLauncher{res: agentLaunch("s-new")}
	d, logPath := newDispatcher(t, l)
	if err := d.Store.Put(staleBinding()); err != nil {
		t.Fatal(err)
	}
	b, err := d.Dispatch(context.Background(), startRequest(l, 2, nil))
	if err != nil {
		t.Fatalf("旧绑定不该挡住这次派工：%v", err)
	}
	assertSeq(t, l, "launch", "start", "send s-new")
	if got, _ := d.Store.Get("n099_demo", 2); got.SessionID != "s-new" || got.StartEvent != "evt_test" || got.Supersedes != "s-old" || b.Supersedes != "s-old" {
		t.Errorf("绑定应当换成新会话、带账本身份、点名被顶替的旧会话：%+v", got)
	}
	sup := d.Store.SupersededList()
	if len(sup) != 1 || sup[0].SessionID != "s-old" || sup[0].StartEvent != "evt_old_ledger" || sup[0].By != "s-new" {
		t.Errorf("旧绑定应当原样挪进留档：%+v", sup)
	}
	logs := readLog(t, logPath)
	if last := logs[len(logs)-1]; !last.OK || last.ReplacedSession != "s-old" {
		t.Errorf("流水应当记下被顶替的旧会话：%+v", last)
	}
}

// 旧绑定在送任务之前就挪走：之后送任务失败，这个键是空的——节点 running、没有绑定，界面的 d 补派走得通，
// 不会因为旧绑定还占着键而被拒（n044 attempt 2 评审 blocker 1 的后半）。
func TestStaleBindingIsMovedBeforeSendSoRedispatchStaysPossible(t *testing.T) {
	l := &seqLauncher{res: agentLaunch("s-new"), sendErr: errors.New("send_failed")}
	d, _ := newDispatcher(t, l)
	if err := d.Store.Put(staleBinding()); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Dispatch(context.Background(), startRequest(l, 2, nil)); err == nil || !strings.Contains(err.Error(), "按 d 补派") {
		t.Fatalf("送任务失败应当拒绝并说明怎么补派：%v", err)
	}
	if _, ok := d.Store.Get("n099_demo", 2); ok {
		t.Error("旧绑定不该还占着这个键：界面会因此拒绝补派")
	}
	if sup := d.Store.SupersededList(); len(sup) != 1 || sup[0].SessionID != "s-old" {
		t.Errorf("旧绑定应当在留档里：%+v", sup)
	}
	// 补派同一个 attempt（不再开工）照常能派。
	l.sendErr = nil
	l.res = agentLaunch("s-retry")
	req := goodRequest()
	req.Attempt = 2
	if b, err := d.Dispatch(context.Background(), req); err != nil || b.SessionID != "s-retry" {
		t.Errorf("补派应当成功：%+v %v", b, err)
	}
}

// start 超时（调用方的 context 已取消）：停会话另起 context，照样停得掉（n044 attempt 1 评审 blocker 2）。
func TestStartTimeoutStillStopsTheFreshSession(t *testing.T) {
	l := &ctxSeqLauncher{seqLauncher: seqLauncher{res: agentLaunch("s1")}}
	d, logPath := newDispatcher(t, l)
	ctx, cancel := context.WithCancel(context.Background())
	req := goodRequest()
	req.Attempt = 0
	req.Start = func(context.Context) (Started, error) {
		cancel() // 模拟整次派工的 context 在 start 期间到期
		return Started{}, ctx.Err()
	}
	_, err := d.Dispatch(ctx, req)
	if err == nil || !strings.Contains(err.Error(), "已停掉") {
		t.Fatalf("start 超时也应当停掉刚起的会话：%v", err)
	}
	if l.abandonCtxErr != nil {
		t.Errorf("停会话拿到的是已取消的 context：%v", l.abandonCtxErr)
	}
	logs := readLog(t, logPath)
	if last := logs[len(logs)-1]; !last.SessionStopped {
		t.Errorf("流水应当记成已停：%+v", last)
	}
}

// ctxSeqLauncher 额外记下停会话时 context 的状态；停会话时若 context 已取消，像真实探测一样失败。
type ctxSeqLauncher struct {
	seqLauncher
	abandonCtxErr error
}

func (l *ctxSeqLauncher) Abandon(ctx context.Context, sessionID string) error {
	l.abandonCtxErr = ctx.Err()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return l.seqLauncher.Abandon(ctx, sessionID)
}

// 路由记录写在起会话之前，那时还没有 attempt：标 attempt_pending，派工流水用 routing_at 对上它。
func TestRoutingRecordOfAStartDispatchIsMarkedPending(t *testing.T) {
	l := &seqLauncher{res: agentLaunch("s1")}
	d, logPath := newDispatcher(t, l)
	if _, err := d.Dispatch(context.Background(), startRequest(l, 4, nil)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(d.RoutingLog)
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		At             string `json:"at"`
		Attempt        int    `json:"attempt"`
		AttemptPending bool   `json:"attempt_pending"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Attempt != 0 || !rec.AttemptPending {
		t.Errorf("路由记录应当是 attempt 0 且标待分配：%+v", rec)
	}
	logs := readLog(t, logPath)
	if last := logs[len(logs)-1]; last.Attempt != 4 || last.RoutingAt.Format("2006-01-02T15:04:05Z07:00") != rec.At {
		t.Errorf("流水的 routing_at 应当等于路由记录的 at：流水 %+v，路由 at %s", last, rec.At)
	}
}

// shell 在 launch 之前就拒：一个会话都不起，Graph 也不开工（n043 复审新问题 1）。执行与评审两条路一样。
func TestShellIsRefusedBeforeAnythingHappens(t *testing.T) {
	tbl, err := NewRouteTable([]Model{{ID: "shell", Tool: "shell", Class: Frontier, NoModel: true}})
	if err != nil {
		t.Fatal(err)
	}
	l := &seqLauncher{res: agentLaunch("s1")}
	d, _ := newDispatcher(t, l)
	d.Table = tbl
	req := startRequest(l, 2, nil)
	req.ModelID = "shell"
	if _, err := d.Dispatch(context.Background(), req); err == nil || !strings.Contains(err.Error(), "不是 agent") {
		t.Fatalf("shell 应当被拒：%v", err)
	}
	rev := goodReview()
	rev.ModelID, rev.IndependentRequired = "shell", false
	if _, err := d.DispatchReviewer(context.Background(), rev); err == nil || !strings.Contains(err.Error(), "不是 agent") {
		t.Fatalf("shell 当不了评审：%v", err)
	}
	assertSeq(t, l)
}

// 配成 bash 的会话，上游起出来是 shell：launch 之后按回执拒，停掉它，Graph 不开工、任务不送。
// 这里故意把 bash 声明成 agent（配错了），才走得到 launch 之后那一道。
func TestDisguisedShellIsStoppedBeforeStart(t *testing.T) {
	tbl, err := NewRouteTable([]Model{{ID: "bash", Tool: "bash", Class: Frontier, NoModel: true, DeclaredAgent: true}})
	if err != nil {
		t.Fatal(err)
	}
	l := &seqLauncher{res: agentdeck.LaunchResult{SessionID: "s1", Success: true, Tool: "shell", Command: "bash"}}
	d, _ := newDispatcher(t, l)
	d.Table = tbl
	req := startRequest(l, 2, nil)
	req.ModelID = "bash"
	_, err = d.Dispatch(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "Graph 没有开工") || !strings.Contains(err.Error(), "已停掉") {
		t.Fatalf("应当拒绝、停掉并说明 Graph 没动：%v", err)
	}
	assertSeq(t, l, "launch", "abandon s1")

	l.seq = nil
	rev := goodReview()
	rev.ModelID, rev.IndependentRequired = "bash", false
	if _, err := d.DispatchReviewer(context.Background(), rev); err == nil || !strings.Contains(err.Error(), "已停掉") {
		t.Fatalf("评审这条路同样要停：%v", err)
	}
	assertSeq(t, l, "launch", "abandon s1")
}

// 评审撞上执行者的会话：那是执行者在用的，不停。
func TestReviewerCollisionNeverStopsTheExecutorSession(t *testing.T) {
	fake := &fakeLauncher{}
	d, _ := newDispatcher(t, fake)
	dispatchExecutorFirst(t, d, fake, "exec-sess")
	fake.res = agentdeck.LaunchResult{SessionID: "exec-sess", Success: true}
	if _, err := d.DispatchReviewer(context.Background(), goodReview()); err == nil {
		t.Fatal("撞会话必须拒绝")
	}
	if len(fake.abandoned) != 0 {
		t.Errorf("执行者的会话不该被停：%v", fake.abandoned)
	}
}

// 开工派单的任务文本按账本给的 attempt 生成：brief 里写的号要和绑定一致。
func TestStartDispatchBuildsTheBriefWithTheLedgerAttempt(t *testing.T) {
	fake := &fakeLauncher{res: agentdeck.LaunchResult{SessionID: "s1", Success: true}}
	d, _ := newDispatcher(t, fake)
	req := goodRequest()
	req.Attempt, req.Message = 0, ""
	req.Start = func(context.Context) (Started, error) { return Started{Attempt: 5, EventID: "evt_5"}, nil }
	req.MessageFor = func(a int) string { return "第 " + string(rune('0'+a)) + " 次 attempt" }
	b, err := d.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if b.Attempt != 5 || len(fake.sent) != 1 || fake.sent[0].Message != "第 5 次 attempt" {
		t.Errorf("任务文本应当按 attempt 5 生成：%+v %+v", b, fake.sent)
	}
}

// n044 attempt 1 评审 blocker 1：自定义工具没在配置里声明 agent 就不派，一个会话都不起；
// 声明了、但上游起的实际命令是 bash（上游回执写的是自定义名字），launch 之后认出来，停掉，Graph 不开工。
func TestCustomToolNeedsAnAgentDeclarationAndARealAgentCommand(t *testing.T) {
	tbl, err := NewRouteTable([]Model{
		{ID: "undeclared", Tool: "n044sh", Class: Frontier, NoModel: true},
		{ID: "declared", Tool: "n044sh", Class: Frontier, NoModel: true, DeclaredAgent: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	l := &seqLauncher{res: agentdeck.LaunchResult{SessionID: "s1", Success: true, Tool: "n044sh", Command: "n044sh", ResolvedCommand: "bash"}}
	d, _ := newDispatcher(t, l)
	d.Table = tbl
	req := startRequest(l, 2, nil)
	req.ModelID = "undeclared"
	if _, err := d.Dispatch(context.Background(), req); err == nil || !strings.Contains(err.Error(), "没声明") {
		t.Fatalf("没声明 agent 的自定义工具应当拒在起会话之前：%v", err)
	}
	assertSeq(t, l)

	req = startRequest(l, 2, nil)
	req.ModelID = "declared"
	if _, err := d.Dispatch(context.Background(), req); err == nil || !strings.Contains(err.Error(), "shell 解释器") || !strings.Contains(err.Error(), "已停掉") {
		t.Fatalf("实际命令是 bash 的会话应当停掉：%v", err)
	}
	assertSeq(t, l, "launch", "abandon s1")

	l.seq = nil
	rev := goodReview()
	rev.ModelID, rev.IndependentRequired = "declared", false
	if _, err := d.DispatchReviewer(context.Background(), rev); err == nil || !strings.Contains(err.Error(), "shell 解释器") {
		t.Fatalf("评审这条路同样要认出来：%v", err)
	}
	assertSeq(t, l, "launch", "abandon s1")
}
