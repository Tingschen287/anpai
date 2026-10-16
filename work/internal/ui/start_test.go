package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tingschen287/terminal-deck/work/internal/binding"
	"github.com/Tingschen287/terminal-deck/work/internal/dispatch"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

func frontierFD() *fakeDispatcher {
	return &fakeDispatcher{cands: []dispatch.Model{{ID: "claude-opus-5", Tool: "claude", Class: dispatch.Frontier}}}
}

// n040 终审断点 4：ready 节点派工前先经 projectctl 开工，派工用账本给的 attempt。
// 恢复过的节点账本给 2，界面不再默认成 1；brief 里的 attempt 也跟着账本走。
func TestReadyNodeStartsInGraphAndUsesTheLedgerAttempt(t *testing.T) {
	fd := frontierFD()
	node := nodeWith("n1", projectctl.StateReady, "frontier")
	node.OwnerRole = "workbench_executor"
	m := modelWithDispatcher(t, []projectctl.Node{node}, fd)
	st := &fakeStarter{attempt: 2}
	m = m.WithStarter(st)
	m = pressD(m)
	m, cmd := enterWithReason(t, m, "唯一够档的")
	if cmd == nil {
		t.Fatal("回车应当开工并派工")
	}
	done := cmd().(dispatchDoneMsg)
	if !done.ok || !strings.Contains(done.text, "Graph 已开工 attempt 2") {
		t.Fatalf("回执应当说明账本开工与 attempt：%+v", done)
	}
	if len(st.got) != 1 {
		t.Fatalf("应当恰好开工一次：%+v", st.got)
	}
	g := st.got[0]
	if g.NodeID != "n1" || g.Role != "workbench_executor" || g.RouteClass != "frontier" || g.Model != "claude-opus-5" || !strings.Contains(g.Reason, "唯一够档的") {
		t.Errorf("开工参数不对：%+v", g)
	}
	if len(fd.got) != 1 || fd.got[0].Attempt != 2 || !strings.Contains(fd.got[0].Message, "第 2 次 attempt") {
		t.Errorf("派工应当用账本的 attempt 2：%+v", fd.got)
	}
}

// 开工失败就不派；上游起不来时 Graph 不动；开工成功、之后失败要说清楚节点已经 running、可以补派（n044）。
func TestStartFailureStopsDispatchAndDispatchFailureSaysHowToRecover(t *testing.T) {
	fd := frontierFD()
	m := modelWithDispatcher(t, []projectctl.Node{nodeWith("n1", projectctl.StateReady, "frontier")}, fd)
	m = m.WithStarter(&fakeStarter{err: &projectctl.ErrStartNotStarted{Cause: errors.New("预算用满")}})
	m = pressD(m)
	_, cmd := enterWithReason(t, m, "x")
	if done := cmd().(dispatchDoneMsg); done.ok || !strings.Contains(done.text, "确定没开工") || len(fd.got) != 0 {
		t.Errorf("确定没开工时不该派：%+v %+v", done, fd.got)
	}

	// 开工结果不明（例如子进程超时）：不说「被拒」，说可能已开工、怎么核对（n044 attempt 2 评审 blocker 2）。
	fdU := frontierFD()
	m = modelWithDispatcher(t, []projectctl.Node{nodeWith("n1", projectctl.StateReady, "frontier")}, fdU)
	m = m.WithStarter(&fakeStarter{err: errors.New("子进程超时")})
	m = pressD(m)
	_, cmd = enterWithReason(t, m, "x")
	if done := cmd().(dispatchDoneMsg); done.ok || strings.Contains(done.text, "派工被拒") || !strings.Contains(done.text, "派工没成") ||
		!strings.Contains(done.text, "可能已经开工") || len(fdU.got) != 0 {
		t.Errorf("开工结果不明要照实说：%+v", done)
	}

	fd2 := frontierFD()
	fd2.err = errors.New("上游挂了")
	st2 := &fakeStarter{attempt: 2}
	m = modelWithDispatcher(t, []projectctl.Node{nodeWith("n1", projectctl.StateReady, "frontier")}, fd2)
	m = m.WithStarter(st2)
	m = pressD(m)
	_, cmd = enterWithReason(t, m, "x")
	if done := cmd().(dispatchDoneMsg); done.ok || done.started || strings.Contains(done.text, "Graph 已开工") || len(st2.got) != 0 {
		t.Errorf("上游起不来时不该开工，界面也不该说开工了：%+v starts=%d", done, len(st2.got))
	}

	fd3 := frontierFD()
	fd3.errAfterStart = errors.New("任务文本没送进去")
	m = modelWithDispatcher(t, []projectctl.Node{nodeWith("n1", projectctl.StateReady, "frontier")}, fd3)
	m = m.WithStarter(&fakeStarter{attempt: 2})
	m = pressD(m)
	_, cmd = enterWithReason(t, m, "x")
	done := cmd().(dispatchDoneMsg)
	if done.ok || !done.started || done.attempt != 2 || !strings.Contains(done.text, "Graph 已开工 attempt 2") {
		t.Errorf("开工之后失败要照实显示节点已 running（attempt 2）：%+v", done)
	}
}

// running：不知道 attempt 就不派；已经绑了会话不重复派；没绑的用账本的 attempt 补派，不再开工。
func TestRunningNodeOnlyRedispatchesAKnownUnboundAttempt(t *testing.T) {
	unknown := nodeWith("n1", projectctl.StateRunning, "frontier")
	m := modelWithDispatcher(t, []projectctl.Node{unknown}, frontierFD())
	if m = pressD(m); m.mode == ModePickModel || !strings.Contains(m.notice, "不知道是第几次 attempt") {
		t.Errorf("attempt 未知时不该进挑模型：%q", m.notice)
	}

	m = modelWithDispatcherAndBindings(t, []projectctl.Node{runningNode("n1", 3)}, frontierFD(), execBound("n1", 3, "s3"))
	if m = pressD(m); m.mode == ModePickModel || !strings.Contains(m.notice, "已经绑在会话 s3") {
		t.Errorf("已绑会话时不该重复派：%q", m.notice)
	}

	fd := frontierFD()
	st := &fakeStarter{}
	m = modelWithDispatcherAndBindings(t, []projectctl.Node{runningNode("n1", 3)}, fd, fakeBindings{})
	m = m.WithStarter(st)
	m = pressD(m)
	_, cmd := enterWithReason(t, m, "补派")
	if done := cmd().(dispatchDoneMsg); !done.ok || len(st.got) != 0 || len(fd.got) != 1 || fd.got[0].Attempt != 3 {
		t.Errorf("补派应当用账本的 attempt 3、不再开工：%+v starts=%d %+v", done, len(st.got), fd.got)
	}
}

// 派评审不知道 attempt 就不派，不再默认成 1。
func TestReviewWithUnknownAttemptIsRefused(t *testing.T) {
	n := submittedNode("n1", 1, "exec")
	n.ReviewIndependent = false
	n.Attempt = projectctl.Unknown[int]("投影过期")
	fd := frontierFD()
	m := modelWithDispatcher(t, []projectctl.Node{n}, fd)
	m = pressD(m)
	if m.mode != ModePickModel {
		t.Fatalf("前提：不要求独立的评审可以进挑模型：%q", m.notice)
	}
	m, cmd := enterWithReason(t, m, "x")
	if cmd != nil || len(fd.reviews) != 0 || !strings.Contains(m.notice, "不知道是第几次 attempt") {
		t.Errorf("attempt 未知时不该派评审：%q %+v", m.notice, fd.reviews)
	}
}

// 送达未确认：界面不说「已送达」，给出下一步。
func TestUnconfirmedDeliveryIsFlaggedOnScreen(t *testing.T) {
	fd := &unconfirmedDispatcher{frontierFD()}
	m := modelWithDispatcher(t, []projectctl.Node{nodeWith("n1", projectctl.StateReady, "frontier")}, fd)
	m = pressD(m)
	_, cmd := enterWithReason(t, m, "x")
	done := cmd().(dispatchDoneMsg)
	if done.ok || !strings.Contains(done.text, "送达未确认") || strings.Contains(done.text, "已送达") || !strings.Contains(done.text, "按 p 看画面") {
		t.Errorf("送达未确认要醒目提示并给下一步：%+v", done)
	}
}

type unconfirmedDispatcher struct{ *fakeDispatcher }

func (u *unconfirmedDispatcher) Dispatch(ctx context.Context, req dispatch.Request) (binding.Binding, error) {
	b, err := u.fakeDispatcher.Dispatch(ctx, req)
	b.Delivery = binding.DeliveryUnconfirmed
	return b, err
}

// n042 attempt 1 评审 blocker 1：派工回执一到，同一进程立刻按 p 就能看到新会话，不等刷新；
// 派工前就发出的那次刷新晚到（旧快照：ready、attempt 未知），也盖不掉；快照追上后覆盖撤掉。
func TestObserveRightAfterDispatchWithoutWaitingForRefresh(t *testing.T) {
	lau := &recordingLauncher{next: "new-sess"}
	d := realDispatcher(t, lau)
	stale := nodeWith("n1", projectctl.StateReady, "frontier")
	m := modelWithDispatcherAndBindings(t, []projectctl.Node{stale}, d, d.Store)
	att := &fakeAttacher{text: "画面"}
	m.attacher = att
	m = pressD(m)
	m, cmd := enterWithReason(t, m, "唯一够档的")
	next, _ := m.Update(cmd())
	m = next.(Model)

	pressP := func(m Model) Model {
		next, c := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
		m = next.(Model)
		if c != nil {
			if msg := c(); msg != nil {
				next, _ = m.Update(msg)
				m = next.(Model)
			}
		}
		return m
	}
	m = pressP(m)
	if len(att.previews) != 1 || att.previews[0] != "new-sess" {
		t.Fatalf("派工回执后立刻按 p 应当预览新会话：previews=%v notice=%q", att.previews, m.notice)
	}

	// 晚到的旧快照：覆盖照样生效。
	next, _ = m.Update(snapshotMsg(projectctl.Snapshot{Nodes: []projectctl.Node{stale}, ProjectRoot: "/tmp/proj"}))
	m = next.(Model)
	if a, ok := m.snap.Nodes[0].Attempt.Get(); !ok || a != 1 {
		t.Errorf("旧快照不该盖掉派工后的 attempt：%v %v", a, ok)
	}
	if st, _ := m.snap.Nodes[0].State.Get(); st != projectctl.StateRunning {
		t.Errorf("开工后应当显示 running：%v", st)
	}
	if m = pressP(m); len(att.previews) != 2 {
		t.Errorf("旧快照到了之后按 p 仍应能预览：%v", att.previews)
	}

	// 快照追上（账本也是 attempt 1）：覆盖撤掉。
	fresh := runningNode("n1", 1)
	next, _ = m.Update(snapshotMsg(projectctl.Snapshot{Nodes: []projectctl.Node{fresh}, ProjectRoot: "/tmp/proj"}))
	m = next.(Model)
	if len(m.overlays) != 0 {
		t.Errorf("快照追上后覆盖应当撤掉：%v", m.overlays)
	}
}

// 派工顶替了旧绑定（账本重建前留下的）：回执点名旧会话，提醒 owner 它可能还在跑（n044 attempt 2 评审 blocker 1）。
func TestSupersededBindingIsCalledOutOnScreen(t *testing.T) {
	fd := &supersedingDispatcher{frontierFD()}
	m := modelWithDispatcher(t, []projectctl.Node{nodeWith("n1", projectctl.StateReady, "frontier")}, fd)
	m = pressD(m)
	_, cmd := enterWithReason(t, m, "x")
	done := cmd().(dispatchDoneMsg)
	if !done.ok || !strings.Contains(done.text, "s-old") || !strings.Contains(done.text, "已挪进留档") {
		t.Errorf("顶替旧绑定要在回执里点名：%+v", done)
	}
}

type supersedingDispatcher struct{ *fakeDispatcher }

func (s *supersedingDispatcher) Dispatch(ctx context.Context, req dispatch.Request) (binding.Binding, error) {
	b, err := s.fakeDispatcher.Dispatch(ctx, req)
	b.Supersedes = "s-old"
	return b, err
}
