package ui

import (
	"context"
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
	st   agentdeck.WriterStatus
	recs []agentdeck.InboxRecord
}

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
	rec := agentdeck.InboxRecord{ChildSessionID: "c1", FromStatus: "running", ToStatus: "waiting", LastOutputHash: "h"}
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
