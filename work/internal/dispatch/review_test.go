package dispatch

import (
	"context"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
)

func goodReview() ReviewRequest {
	return ReviewRequest{
		NodeID: "n099_demo", Attempt: 1, RequiredClass: Frontier,
		ModelID: "claude-opus-5", ProjectPath: "/tmp/p",
		ExecutorActor: "executor-a1", ReviewerActor: "reviewer-b1",
		IndependentRequired: true, DispatchedBy: "work-deck",
	}
}

// 这是本节点最要紧的一条：自己评自己等于没评，而账本上会留下一条
// 看起来完全合规的验收记录，那比没有评审更糟。
func TestReviewerCannotBeTheExecutor(t *testing.T) {
	fake := &fakeLauncher{res: agentdeck.LaunchResult{SessionID: "s", Success: true}}
	d, logPath := newDispatcher(t, fake)

	req := goodReview()
	req.ReviewerActor = req.ExecutorActor // 同一个会话
	if _, err := d.DispatchReviewer(context.Background(), req); err == nil {
		t.Fatal("评审者就是执行者时必须拒绝")
	} else if !strings.Contains(err.Error(), "就是执行者本人") {
		t.Errorf("拒绝理由不清楚：%v", err)
	}
	if len(fake.calls) != 0 {
		t.Error("被拒绝的派评审不该真的去启动会话")
	}
	log := readLog(t, logPath)
	if len(log) != 1 || log[0].OK {
		t.Errorf("拒绝没记进流水：%+v", log)
	}
}

// 不知道执行者是谁，就没法判断独立性，同样要拒绝。
func TestUnknownExecutorBlocksIndependentReview(t *testing.T) {
	fake := &fakeLauncher{}
	d, _ := newDispatcher(t, fake)
	req := goodReview()
	req.ExecutorActor = ""
	if _, err := d.DispatchReviewer(context.Background(), req); err == nil {
		t.Fatal("执行者未知时必须拒绝独立评审")
	} else if !strings.Contains(err.Error(), "无法判断独立性") {
		t.Errorf("拒绝理由不清楚：%v", err)
	}
	if len(fake.calls) != 0 {
		t.Error("不该真的去启动会话")
	}
}

// 节点不要求独立时，同一个会话可以放行——但这必须是节点说了算，不是默认。
func TestSameActorIsAllowedWhenNodeDoesNotRequireIndependence(t *testing.T) {
	fake := &fakeLauncher{res: agentdeck.LaunchResult{SessionID: "s", Success: true}}
	d, _ := newDispatcher(t, fake)
	req := goodReview()
	req.IndependentRequired = false
	req.ReviewerActor = req.ExecutorActor
	if _, err := d.DispatchReviewer(context.Background(), req); err != nil {
		t.Fatalf("节点不要求独立时不该拒绝：%v", err)
	}
}

func TestReviewerRefusesUnknownModelAndDowngrade(t *testing.T) {
	fake := &fakeLauncher{}
	d, _ := newDispatcher(t, fake)

	unknown := goodReview()
	unknown.ModelID = "没人认识的模型"
	if _, err := d.DispatchReviewer(context.Background(), unknown); err == nil {
		t.Error("评审模型未知必须拒绝")
	}

	down := goodReview()
	down.ModelID = "claude-sonnet-5" // balanced，节点要 frontier
	if _, err := d.DispatchReviewer(context.Background(), down); err == nil {
		t.Error("评审降档必须拒绝")
	} else if !strings.Contains(err.Error(), "评审不得降档") {
		t.Errorf("拒绝理由不清楚：%v", err)
	}
	if len(fake.calls) != 0 {
		t.Error("被拒绝的都不该真的去启动会话")
	}
}

func TestReviewerBindingIsKeptApartFromExecutorBinding(t *testing.T) {
	fake := &fakeLauncher{res: agentdeck.LaunchResult{SessionID: "exec-sess", Success: true}}
	d, _ := newDispatcher(t, fake)

	// 先派执行者。
	if _, err := d.Dispatch(context.Background(), goodRequest()); err != nil {
		t.Fatal(err)
	}
	// 再派评审，换一个会话号。
	fake.res = agentdeck.LaunchResult{SessionID: "review-sess", Success: true}
	rb, err := d.DispatchReviewer(context.Background(), goodReview())
	if err != nil {
		t.Fatalf("正常派评审不该失败：%v", err)
	}

	// 两条绑定要各自存在，评审不能把执行的顶掉。
	eb, ok := d.Store.Get("n099_demo", 1)
	if !ok || eb.SessionID != "exec-sess" {
		t.Errorf("执行者的绑定被动了：%+v", eb)
	}
	got, ok := d.Store.Get(ReviewerKey("n099_demo"), 1)
	if !ok || got.SessionID != "review-sess" {
		t.Errorf("评审的绑定不对：%+v", got)
	}
	if rb.SessionID == eb.SessionID {
		t.Error("评审和执行落到了同一个会话")
	}
}

// 上游若把评审派进了执行者正在用的会话，要拒绝并说出来。
func TestReviewerLandingInExecutorSessionIsRefused(t *testing.T) {
	fake := &fakeLauncher{res: agentdeck.LaunchResult{SessionID: "same-sess", Success: true}}
	d, _ := newDispatcher(t, fake)

	if _, err := d.Dispatch(context.Background(), goodRequest()); err != nil {
		t.Fatal(err)
	}
	// 上游返回同一个会话号——不该发生，但发生了就必须拦住。
	if _, err := d.DispatchReviewer(context.Background(), goodReview()); err == nil {
		t.Fatal("评审落到执行者会话时必须拒绝")
	} else if !strings.Contains(err.Error(), "执行者正在用的会话") {
		t.Errorf("拒绝理由不清楚：%v", err)
	}
}

func TestDuplicateReviewerDispatchIsRefused(t *testing.T) {
	fake := &fakeLauncher{res: agentdeck.LaunchResult{SessionID: "r1", Success: true}}
	d, _ := newDispatcher(t, fake)
	if _, err := d.DispatchReviewer(context.Background(), goodReview()); err != nil {
		t.Fatal(err)
	}
	fake.res = agentdeck.LaunchResult{SessionID: "r2", Success: true}
	if _, err := d.DispatchReviewer(context.Background(), goodReview()); err == nil {
		t.Error("同一次 attempt 重复派评审必须拒绝")
	}
}
