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
	req.Start = func(context.Context) (int, error) {
		l.seq = append(l.seq, "start")
		return attempt, startErr
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

// start 失败：停掉刚起的空会话，任务不送、不落绑定。
func TestStartFailureStopsTheFreshSession(t *testing.T) {
	l := &seqLauncher{res: agentLaunch("s1")}
	d, logPath := newDispatcher(t, l)
	_, err := d.Dispatch(context.Background(), startRequest(l, 0, errors.New("控制器拒绝开工")))
	if err == nil || !strings.Contains(err.Error(), "Graph 开工失败") || !strings.Contains(err.Error(), "已停掉") {
		t.Fatalf("start 失败应当拒绝，并说明会话已停：%v", err)
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
	_, err := d.Dispatch(context.Background(), startRequest(l, 0, errors.New("控制器拒绝开工")))
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

// 绑定存储里已有账本刚分配的 attempt（例如夹具重建后留下的旧绑定）：不覆盖，停掉新会话。
func TestStaleBindingForTheStartedAttemptIsNotOverwritten(t *testing.T) {
	l := &seqLauncher{res: agentLaunch("s-new")}
	d, _ := newDispatcher(t, l)
	if err := d.Store.Put(binding.Binding{NodeID: "n099_demo", Attempt: 2, SessionID: "s-old", Tool: "claude", Model: "claude-opus-5", RouteClass: "frontier", ProjectPath: "/tmp/p", Profile: "_t", DispatchedBy: "earlier"}); err != nil {
		t.Fatal(err)
	}
	_, err := d.Dispatch(context.Background(), startRequest(l, 2, nil))
	if err == nil || !strings.Contains(err.Error(), "旧绑定") || !strings.Contains(err.Error(), "s-old") {
		t.Fatalf("应当拒绝并点名旧绑定：%v", err)
	}
	assertSeq(t, l, "launch", "start", "abandon s-new")
	if got, _ := d.Store.Get("n099_demo", 2); got.SessionID != "s-old" {
		t.Errorf("旧绑定不该被覆盖：%+v", got)
	}
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
func TestDisguisedShellIsStoppedBeforeStart(t *testing.T) {
	tbl, err := NewRouteTable([]Model{{ID: "bash", Tool: "bash", Class: Frontier, NoModel: true}})
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
	req.Start = func(context.Context) (int, error) { return 5, nil }
	req.MessageFor = func(a int) string { return "第 " + string(rune('0'+a)) + " 次 attempt" }
	b, err := d.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if b.Attempt != 5 || len(fake.sent) != 1 || fake.sent[0].Message != "第 5 次 attempt" {
		t.Errorf("任务文本应当按 attempt 5 生成：%+v %+v", b, fake.sent)
	}
}
