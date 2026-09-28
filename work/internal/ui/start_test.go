package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

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

// 开工失败就不派；开工成功、派工失败要说清楚节点已经 running、可以补派。
func TestStartFailureStopsDispatchAndDispatchFailureSaysHowToRecover(t *testing.T) {
	fd := frontierFD()
	m := modelWithDispatcher(t, []projectctl.Node{nodeWith("n1", projectctl.StateReady, "frontier")}, fd)
	m = m.WithStarter(&fakeStarter{err: errors.New("预算用满")})
	m = pressD(m)
	_, cmd := enterWithReason(t, m, "x")
	if done := cmd().(dispatchDoneMsg); done.ok || !strings.Contains(done.text, "Graph 开工失败") || len(fd.got) != 0 {
		t.Errorf("开工失败时不该派：%+v %+v", done, fd.got)
	}

	fd2 := frontierFD()
	fd2.err = errors.New("上游挂了")
	m = modelWithDispatcher(t, []projectctl.Node{nodeWith("n1", projectctl.StateReady, "frontier")}, fd2)
	m = pressD(m)
	_, cmd = enterWithReason(t, m, "x")
	if done := cmd().(dispatchDoneMsg); done.ok || !strings.Contains(done.text, "running") || !strings.Contains(done.text, "补派") {
		t.Errorf("开工后派工失败要说明怎么补：%+v", done)
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
