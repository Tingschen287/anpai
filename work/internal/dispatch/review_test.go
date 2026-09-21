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
		ExecutorActor:       "executor-a1",
		IndependentRequired: true, DispatchedBy: "work-deck",
	}
}

// dispatchExecutorFirst 先把执行者派出去，让绑定存储里有一条执行会话。
//
// 要求独立评审的节点必须先有这个，否则派评审一律被拒——这正是本包的规矩。
func dispatchExecutorFirst(t *testing.T, d *Dispatcher, fake *fakeLauncher, session string) {
	t.Helper()
	fake.res = agentdeck.LaunchResult{SessionID: session, Success: true}
	if _, err := d.Dispatch(context.Background(), goodRequest()); err != nil {
		t.Fatalf("先派执行者失败：%v", err)
	}
}

// 这是本节点最要紧的一条：自己评自己等于没评，而账本上会留下一条
// 看起来完全合规的验收记录，那比没有评审更糟。
//
// 判据是上游给回来的会话号，不是调用方自称的身份。
func TestReviewerCannotLandInTheExecutorSession(t *testing.T) {
	fake := &fakeLauncher{}
	d, logPath := newDispatcher(t, fake)
	dispatchExecutorFirst(t, d, fake, "same-sess")

	// 上游把评审派回了执行者那个会话——不该发生，但发生了就必须拦住。
	fake.res = agentdeck.LaunchResult{SessionID: "same-sess", Success: true}
	if _, err := d.DispatchReviewer(context.Background(), goodReview()); err == nil {
		t.Fatal("评审落到执行者会话时必须拒绝")
	} else if !strings.Contains(err.Error(), "执行者正在用的会话") {
		t.Errorf("拒绝理由不清楚：%v", err)
	}

	log := readLog(t, logPath)
	if len(log) == 0 || log[len(log)-1].OK {
		t.Errorf("拒绝没记进流水：%+v", log)
	}
}

// 查不到执行者在哪个会话，就证明不了评审独立，必须拒绝——不是跳过检查。
//
// 这条是 fail-closed 的核心。上一版这里是开的：独立性只比两个字符串，
// 而界面拼了一个 "work-deck-reviewer-<模型名>" 当评审者身份，
// 它永远不等于执行者，于是检查永远通过。
func TestIndependentReviewIsRefusedWhenExecutorSessionIsUnknown(t *testing.T) {
	fake := &fakeLauncher{res: agentdeck.LaunchResult{SessionID: "r1", Success: true}}
	d, _ := newDispatcher(t, fake)

	// 注意：没有先派执行者，绑定存储里是空的。
	if _, err := d.DispatchReviewer(context.Background(), goodReview()); err == nil {
		t.Fatal("查不到执行会话时必须拒绝独立评审")
	} else if !strings.Contains(err.Error(), "证明不了评审独立") {
		t.Errorf("拒绝理由不清楚：%v", err)
	}
	if len(fake.calls) != 0 {
		t.Error("证明不了独立就不该去启动会话")
	}
}

// 调用方自己填的身份不作数：填什么都绕不过「执行会话必须已知」这一关。
//
// 这是上一版那个 bug 的回归测试。
func TestCallerSuppliedIdentityCannotFakeIndependence(t *testing.T) {
	fake := &fakeLauncher{res: agentdeck.LaunchResult{SessionID: "r1", Success: true}}
	d, _ := newDispatcher(t, fake)

	for _, actor := range []string{
		"executor-a1",
		"work-deck-reviewer-claude-opus-5", // 上一版界面拼出来的那种
		"某个一看就和执行者不一样的名字",
	} {
		req := goodReview()
		req.ExecutorActor = actor
		if _, err := d.DispatchReviewer(context.Background(), req); err == nil {
			t.Errorf("执行者身份填成 %q 就放行了：独立性被调用方自己说了算", actor)
		}
	}
	if len(fake.calls) != 0 {
		t.Error("以上都该在启动会话之前就被拒")
	}
}

// 不知道执行者是谁，同样没法谈独立性。
func TestUnknownExecutorActorBlocksIndependentReview(t *testing.T) {
	fake := &fakeLauncher{}
	d, _ := newDispatcher(t, fake)
	dispatchExecutorFirst(t, d, fake, "exec-sess")

	req := goodReview()
	req.ExecutorActor = ""
	if _, err := d.DispatchReviewer(context.Background(), req); err == nil {
		t.Fatal("执行者未知时必须拒绝独立评审")
	} else if !strings.Contains(err.Error(), "无法判断独立性") {
		t.Errorf("拒绝理由不清楚：%v", err)
	}
	if len(fake.calls) != 1 {
		t.Error("除了先前那次派执行，不该再启动会话")
	}
}

// 节点不要求独立时放行，且不需要先有执行会话——但这必须是节点说了算，不是默认。
func TestNonIndependentReviewDoesNotNeedAnExecutorSession(t *testing.T) {
	fake := &fakeLauncher{res: agentdeck.LaunchResult{SessionID: "r1", Success: true}}
	d, _ := newDispatcher(t, fake)
	req := goodReview()
	req.IndependentRequired = false
	if _, err := d.DispatchReviewer(context.Background(), req); err != nil {
		t.Fatalf("节点不要求独立时不该拒绝：%v", err)
	}
}

func TestReviewerRefusesUnknownModelAndDowngrade(t *testing.T) {
	fake := &fakeLauncher{}
	d, _ := newDispatcher(t, fake)
	dispatchExecutorFirst(t, d, fake, "exec-sess")
	before := len(fake.calls)

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
	if len(fake.calls) != before {
		t.Error("被拒绝的都不该真的去启动会话")
	}
}

func TestReviewerBindingIsKeptApartFromExecutorBinding(t *testing.T) {
	fake := &fakeLauncher{}
	d, _ := newDispatcher(t, fake)
	dispatchExecutorFirst(t, d, fake, "exec-sess")

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

func TestDuplicateReviewerDispatchIsRefused(t *testing.T) {
	fake := &fakeLauncher{}
	d, _ := newDispatcher(t, fake)
	dispatchExecutorFirst(t, d, fake, "exec-sess")

	fake.res = agentdeck.LaunchResult{SessionID: "r1", Success: true}
	if _, err := d.DispatchReviewer(context.Background(), goodReview()); err != nil {
		t.Fatal(err)
	}
	fake.res = agentdeck.LaunchResult{SessionID: "r2", Success: true}
	if _, err := d.DispatchReviewer(context.Background(), goodReview()); err == nil {
		t.Error("同一次 attempt 重复派评审必须拒绝")
	}
}

// 要求独立评审时，评审 brief 不许随会话启动一起投递。
//
// 独立评审者提的：撞会话这一判断天然在 launch 之后，会话号早于此不存在。
// 若 brief 随 launch 送出，万一真撞上，任务已经进了执行者的会话，
// 后面报错也撤不回来。把 brief 摘掉，撞上的后果就只剩一个空会话。
func TestIndependentReviewRefusesToShipTheBriefWithTheLaunch(t *testing.T) {
	fake := &fakeLauncher{}
	d, _ := newDispatcher(t, fake)
	dispatchExecutorFirst(t, d, fake, "exec-sess")
	before := len(fake.calls)

	req := goodReview()
	req.Message = "请评审 n099_demo 的 attempt 1"
	if _, err := d.DispatchReviewer(context.Background(), req); err == nil {
		t.Fatal("要求独立评审时，brief 随 launch 投递必须拒绝")
	} else if !strings.Contains(err.Error(), "不能随会话启动一起投递") {
		t.Errorf("拒绝理由不清楚：%v", err)
	}
	if len(fake.calls) != before {
		t.Error("被拒绝的派评审不该真的去启动会话")
	}
}

// 撞上执行者会话时，被拒、绑定不落，且送出去的那个会话是空的。
//
// 说清楚这条测什么、不测什么：`Message` 为空这一点，在上一条守卫在位时
// 是恒真的——拿掉守卫这条断言也不会红（试过）。它是纵深的第二道，
// 不是守卫有效的独立证据；守卫的证据是上一个测试。
// 这里真正独立守住的是另外两件：撞会话必须拒，以及拒了就不许落绑定。
func TestSessionCollisionCannotCarryTheBrief(t *testing.T) {
	fake := &fakeLauncher{}
	d, _ := newDispatcher(t, fake)
	dispatchExecutorFirst(t, d, fake, "same-sess")

	fake.res = agentdeck.LaunchResult{SessionID: "same-sess", Success: true}
	if _, err := d.DispatchReviewer(context.Background(), goodReview()); err == nil {
		t.Fatal("撞上执行者会话必须拒绝")
	}
	// 最后那次 launch 就是撞上的那次，它不能带着评审任务。
	last := fake.calls[len(fake.calls)-1]
	if last.Message != "" {
		t.Errorf("撞会话时把评审任务也送进去了：%q", last.Message)
	}
	// 绑定不许落下。
	if _, ok := d.Store.Get(ReviewerKey("n099_demo"), 1); ok {
		t.Error("撞会话被拒了，评审绑定却存住了")
	}
}

// 节点不要求独立时，brief 可以随 launch 走——这条约束只为独立评审而设。
func TestNonIndependentReviewMayShipTheBrief(t *testing.T) {
	fake := &fakeLauncher{res: agentdeck.LaunchResult{SessionID: "r1", Success: true}}
	d, _ := newDispatcher(t, fake)
	req := goodReview()
	req.IndependentRequired = false
	req.Message = "看一下这个"
	if _, err := d.DispatchReviewer(context.Background(), req); err != nil {
		t.Fatalf("不要求独立时不该拒绝：%v", err)
	}
	if fake.calls[len(fake.calls)-1].Message != "看一下这个" {
		t.Error("不要求独立时 brief 应当照常送出")
	}
}
