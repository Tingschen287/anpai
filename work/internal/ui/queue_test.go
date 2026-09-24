package ui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
	"github.com/Tingschen287/terminal-deck/work/internal/queue"
)

type fakeInbox struct {
	st       agentdeck.WriterStatus
	recs     []agentdeck.InboxRecord
	procDown bool
}

func (f *fakeInbox) NotifyDaemonRunning(context.Context) (bool, error) { return !f.procDown, nil }
func (f *fakeInbox) ProfileName() string                               { return "_wd" }

func (f *fakeInbox) InboxWriterStatus(context.Context) (agentdeck.WriterStatus, error) {
	return f.st, nil
}
func (f *fakeInbox) InboxExport(context.Context) ([]agentdeck.InboxRecord, error) {
	return f.recs, nil
}

type fakeSend struct {
	to   []string
	msgs []string
}

func (f *fakeSend) DeliverAnswer(_ context.Context, rec agentdeck.InboxRecord, msg string) error {
	f.to = append(f.to, rec.ChildSessionID)
	f.msgs = append(f.msgs, msg)
	return nil
}

func queueModel(t *testing.T, up queue.Upstream, del queue.Deliverer) Model {
	t.Helper()
	store, err := queue.Open(filepath.Join(t.TempDir(), "queue.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(nil).WithQueue(store, up, del, "/x/projectctl.py")
	m.snap = projectctl.Snapshot{ProjectRoot: "/tmp/proj", Nodes: []projectctl.Node{runningNode("n1", 1)}}
	m.inflight, m.loading = false, false
	return m
}

// 跑一个 tea.Cmd，把它产出的消息喂回界面。
func runCmd(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		t.Fatal("期望有一个命令要跑")
	}
	next, _ := m.Update(cmd())
	return next.(Model)
}

func polled(t *testing.T, m Model) Model {
	return runCmd(t, m, m.pollQueue())
}

// notify-daemon 没在跑：主界面必须告警，绝不能说「队列为空」。
func TestMainScreenAlertsWhenDaemonIsNotRunning(t *testing.T) {
	m := polled(t, queueModel(t, &fakeInbox{st: agentdeck.WriterStatus{Running: false}}, nil))
	out := m.View()
	if !strings.Contains(out, "notify-daemon 没在运行") {
		t.Errorf("daemon 不在时主界面没告警：\n%s", out)
	}
	if strings.Contains(out, "决策队列为空") {
		t.Error("daemon 不在时不能把队列说成空的")
	}
}

// 还没取过运行时来源时，同样不能说空。
func TestQueueIsNotCalledEmptyBeforeTheFirstPoll(t *testing.T) {
	out := queueModel(t, &fakeInbox{st: agentdeck.WriterStatus{Running: true}}, nil).View()
	if strings.Contains(out, "决策队列为空") || !strings.Contains(out, "还没取过") {
		t.Errorf("第一次取数前应当说明队列不完整：\n%s", out)
	}
}

// 只有 daemon 在跑、读取成功、确实没有条目时，才说空。
func TestQueueIsCalledEmptyOnlyWhenTheSourcesAreTrustworthy(t *testing.T) {
	m := polled(t, queueModel(t, &fakeInbox{st: agentdeck.WriterStatus{Running: true}}, nil))
	if out := m.View(); !strings.Contains(out, "决策队列为空") {
		t.Errorf("来源可信且没有条目时应当说空：\n%s", out)
	}
}

// 没配置上游时，队列照开，但要说明运行时来源取不到。
func TestQueueWithoutUpstreamSaysWhatIsMissing(t *testing.T) {
	m := polled(t, queueModel(t, nil, nil))
	if out := m.View(); !strings.Contains(out, "没有配置 upstream agent-deck") {
		t.Errorf("没配置上游时要说明：\n%s", out)
	}
}

// 回答要两步：按数字只是选中，按 y 才投递；投递到发起会话。
func TestAnsweringNeedsConfirmationAndReachesTheSession(t *testing.T) {
	rec := agentdeck.InboxRecord{ChildSessionID: "c1", ChildTitle: "worker", Profile: "_wd",
		FromStatus: "running", ToStatus: "waiting", Timestamp: time.Now(), LastOutputHash: "h"}
	send := &fakeSend{}
	m := polled(t, queueModel(t, &fakeInbox{st: agentdeck.WriterStatus{Running: true}, recs: []agentdeck.InboxRecord{rec}}, send))
	if out := m.View(); !strings.Contains(out, "1 条待你拍板") {
		t.Fatalf("主界面没报待决条数：\n%s", out)
	}
	m, _ = press(m, 'i')
	if out := m.View(); !strings.Contains(out, "会话在等你输入：worker") || !strings.Contains(out, "← 推荐") {
		t.Fatalf("队列视图没画出条目与推荐：\n%s", out)
	}
	m, cmd := press(m, '1')
	if cmd != nil || len(send.msgs) != 0 {
		t.Fatal("按数字不该直接投递")
	}
	if !strings.Contains(m.notice, "按 y 确认") || !strings.Contains(m.notice, "c1") {
		t.Errorf("确认提示没说清要发给谁：%q", m.notice)
	}
	m, cmd = press(m, 'y')
	m = runCmd(t, m, cmd)
	if len(send.to) != 1 || send.to[0] != "c1" || send.msgs[0] != "继续" {
		t.Fatalf("没投递到发起会话：%v %v", send.to, send.msgs)
	}
	if !strings.Contains(m.notice, "已向会话 c1 发送") {
		t.Errorf("投递结果没告诉人：%q", m.notice)
	}
}

// 按数字后按别的键就取消，什么都不投。
func TestAnyOtherKeyCancelsThePendingAnswer(t *testing.T) {
	rec := agentdeck.InboxRecord{ChildSessionID: "c1", Profile: "_wd", FromStatus: "running", ToStatus: "waiting", LastOutputHash: "h"}
	send := &fakeSend{}
	m := polled(t, queueModel(t, &fakeInbox{st: agentdeck.WriterStatus{Running: true}, recs: []agentdeck.InboxRecord{rec}}, send))
	m, _ = press(m, 'i')
	m, _ = press(m, '1')
	m, cmd := press(m, 'n')
	if cmd != nil || len(send.msgs) != 0 {
		t.Error("取消后不该投递")
	}
}

// 完整快照到了，Graph 的门进同一个队列。
func TestSnapshotBringsGraphGatesIntoTheQueue(t *testing.T) {
	m := polled(t, queueModel(t, &fakeInbox{st: agentdeck.WriterStatus{Running: true}}, nil))
	gate := projectctl.Node{ID: "g1", Kind: "human_gate", Risk: "high",
		State: projectctl.Known(projectctl.StateReady), Attempt: projectctl.Unknown[int]("没开过工")}
	next, _ := m.Update(snapshotMsg(projectctl.Snapshot{ProjectRoot: "/tmp/proj", Nodes: []projectctl.Node{gate}}))
	m = next.(Model)
	m, _ = press(m, 'i')
	if out := m.View(); !strings.Contains(out, "Graph 门等你：g1") || !strings.Contains(out, "不给推荐") {
		t.Errorf("Graph 门没进队列或给了推荐：\n%s", out)
	}
}

// daemon 刚退出：心跳还在 90 秒窗口内，但进程不在。主界面必须告警，不能说空。
func TestMainScreenAlertsWhenHeartbeatIsFreshButProcessIsGone(t *testing.T) {
	m := polled(t, queueModel(t, &fakeInbox{st: agentdeck.WriterStatus{Running: true}, procDown: true}, nil))
	out := m.View()
	if !strings.Contains(out, "进程表里找不到") || strings.Contains(out, "决策队列为空") {
		t.Errorf("心跳新鲜但进程不在时必须告警、不能说空：\n%s", out)
	}
}

// 队列文件打不开：主界面常驻告警；按 i 只看到告警，不崩。
func TestQueueFileErrorIsAStandingAlert(t *testing.T) {
	m := New(nil).WithQueueError(errors.New("第 1 条不完整"))
	m.snap = projectctl.Snapshot{ProjectRoot: "/tmp/proj", Nodes: []projectctl.Node{runningNode("n1", 1)}}
	m.inflight, m.loading = false, false
	if out := m.View(); !strings.Contains(out, "决策队列打不开") || strings.Contains(out, "决策队列为空") {
		t.Fatalf("队列打不开时主界面必须告警：\n%s", out)
	}
	m, _ = press(m, 'i')
	m, _ = press(m, '1')
	if out := m.View(); !strings.Contains(out, "决策队列打不开") {
		t.Errorf("队列视图里也要告警：\n%s", out)
	}
	m, _ = press(m, 'i')
	if m.qv.show {
		t.Error("按 i 应当能退出队列视图")
	}
}

// 已回答的会话条目在界面上不能再选中投递。
func TestAnsweredItemCannotBePickedAgain(t *testing.T) {
	rec := agentdeck.InboxRecord{ChildSessionID: "c1", Profile: "_wd", FromStatus: "running", ToStatus: "waiting", LastOutputHash: "h"}
	send := &fakeSend{}
	m := polled(t, queueModel(t, &fakeInbox{st: agentdeck.WriterStatus{Running: true}, recs: []agentdeck.InboxRecord{rec}}, send))
	m, _ = press(m, 'i')
	m, _ = press(m, '1')
	m, cmd := press(m, 'y')
	m = runCmd(t, m, cmd)
	m, cmd = press(m, '1')
	if cmd != nil || m.qv.pendingID != "" || !strings.Contains(m.notice, "已经回答过") {
		t.Errorf("已回答的条目不该再进入确认：pending=%q notice=%q", m.qv.pendingID, m.notice)
	}
	if len(send.msgs) != 1 {
		t.Errorf("只该投递一次，实际 %d 次", len(send.msgs))
	}
}

// 别的 profile 的记录不进队列，但界面要说清楚有几条没收。
func TestForeignRecordsAreCountedNotQueued(t *testing.T) {
	theirs := agentdeck.InboxRecord{ChildSessionID: "c9", Profile: "work", FromStatus: "running", ToStatus: "waiting", LastOutputHash: "h"}
	m := polled(t, queueModel(t, &fakeInbox{st: agentdeck.WriterStatus{Running: true}, recs: []agentdeck.InboxRecord{theirs}}, nil))
	out := m.View()
	if !strings.Contains(out, "另有 1 条 inbox 记录属于别的 profile") {
		t.Errorf("没收进队列的记录要说出来：\n%s", out)
	}
	if m.queueOpen() != 0 {
		t.Error("别的 profile 的记录不该进队列")
	}
}

// 中断的投递：界面上 R 清标记后能重答并投递一次，K 记为已收到；两个键本身都不发消息。
func TestInFlightItemCanBeSettledFromTheQueueView(t *testing.T) {
	rec := agentdeck.InboxRecord{ChildSessionID: "c1", Profile: "_wd", FromStatus: "running", ToStatus: "waiting", LastOutputHash: "h"}
	send := &fakeSend{}
	m := polled(t, queueModel(t, &fakeInbox{st: agentdeck.WriterStatus{Running: true}, recs: []agentdeck.InboxRecord{rec}}, send))
	id := m.qw.store.Items()[0].ID
	m.qw.store.MarkInFlight(id, "continue", time.Now()) // 模拟上次投递途中崩溃
	m, _ = press(m, 'i')
	if out := m.View(); !strings.Contains(out, "按 K（已收到）或 R") {
		t.Fatalf("中断的条目要告诉人怎么结案：\n%s", out)
	}
	m, cmd := press(m, '1')
	if cmd != nil || m.qv.pendingID != "" {
		t.Fatal("中断的条目不该直接进入确认")
	}
	m, cmd = press(m, 'R')
	if cmd != nil || len(send.msgs) != 0 {
		t.Fatal("R 只清标记，不该投递")
	}
	m, _ = press(m, '1')
	m, cmd = press(m, 'y')
	m = runCmd(t, m, cmd)
	if len(send.msgs) != 1 {
		t.Fatalf("清标记后重新选并确认，应当投递一次，实际 %d", len(send.msgs))
	}

	// 另一条：K 记为已收到，不发消息。
	store := m.qw.store
	rec2 := rec
	rec2.ChildSessionID, rec2.LastOutputHash = "c2", "h2"
	store.Merge(queue.SourceSession, queue.FromInbox([]agentdeck.InboxRecord{rec2}, time.Now()), time.Now())
	var id2 string
	for i, it := range store.Items() {
		if it.Source.SessionID == "c2" {
			id2, m.qv.cursor = it.ID, i
		}
	}
	store.MarkInFlight(id2, "continue", time.Now())
	m, _ = press(m, 'K')
	if it, _ := store.Get(id2); it.Status != queue.StatusAnswered || len(send.msgs) != 1 {
		t.Errorf("K 应当记为已回答且不投递：status=%s sends=%d", it.Status, len(send.msgs))
	}
}

// Graph 合法地变成零节点时，旧门转为已不等；零节点但带着取数问题时不动。
func TestEmptyGraphResolvesOldGatesButFailedFetchDoesNot(t *testing.T) {
	m := polled(t, queueModel(t, &fakeInbox{st: agentdeck.WriterStatus{Running: true}}, nil))
	gate := projectctl.Node{ID: "g1", Kind: "human_gate", Risk: "high",
		State: projectctl.Known(projectctl.StateReady), Attempt: projectctl.Unknown[int]("没开过工")}
	next, _ := m.Update(snapshotMsg(projectctl.Snapshot{ProjectRoot: "/tmp/proj", Nodes: []projectctl.Node{gate}}))
	m = next.(Model)
	next, _ = m.Update(snapshotMsg(projectctl.Snapshot{ProjectRoot: "/tmp/proj", Problems: []string{"brief 失败"}}))
	m = next.(Model)
	if m.queueOpen() != 1 {
		t.Fatalf("取数失败的空快照不该让门离开队列，待拍板 %d", m.queueOpen())
	}
	next, _ = m.Update(snapshotMsg(projectctl.Snapshot{ProjectRoot: "/tmp/proj"}))
	m = next.(Model)
	if m.queueOpen() != 0 {
		t.Errorf("合法的空 Graph 应当让旧门转为已不等，待拍板 %d", m.queueOpen())
	}
}
