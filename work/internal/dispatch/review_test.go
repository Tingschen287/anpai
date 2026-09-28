package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
)

func goodReview() ReviewRequest {
	return ReviewRequest{
		NodeID: "n099_demo", Attempt: 1, RequiredClass: Frontier,
		ModelID: "claude-opus-5", ProjectPath: "/tmp/p",
		ExecutorActor:       "executor-a1",
		IndependentRequired: true, DispatchedBy: "work-deck", ChosenBy: "owner", ChoiceReason: "测试里指定",
		Message: "请评审 n099_demo 的 attempt 1",
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
	// 执行者自己的任务文本也经 SendMessage 送（n039 attempt 4）；清掉它，后面只数评审那一次。
	if len(fake.sent) != 1 || fake.sent[0].SessionID != session {
		t.Fatalf("执行者的任务文本应当恰好送到 %s 一次：%+v", session, fake.sent)
	}
	fake.sent = nil
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

// 两阶段投递：先起空会话，核验会话号，再把 brief 送进去。
//
// 独立评审一句话戳破了上一版：「所谓派出评审实际只创建空会话」。
// 界面从不设置 Message，而那一版还加了一条守卫禁止 brief 随 launch 出去，
// 等于把「评审会话永远收不到任务」这件事固化成了设计。
func TestBriefIsDeliveredOnlyAfterTheSessionIsVerified(t *testing.T) {
	fake := &fakeLauncher{}
	d, _ := newDispatcher(t, fake)
	dispatchExecutorFirst(t, d, fake, "exec-sess")

	fake.res = agentdeck.LaunchResult{SessionID: "review-sess", Success: true}
	if _, err := d.DispatchReviewer(context.Background(), goodReview()); err != nil {
		t.Fatalf("正常派评审不该失败：%v", err)
	}

	// launch 那一步带不了 brief：LaunchRequest 没有消息字段（n039），编译期就挡住了。
	// 核验之后必须真的送达，而且送到评审那个会话。
	if len(fake.sent) != 1 {
		t.Fatalf("brief 应当恰好投递一次，实际 %d 次", len(fake.sent))
	}
	if fake.sent[0].SessionID != "review-sess" {
		t.Errorf("brief 投给了 %q，期望 review-sess", fake.sent[0].SessionID)
	}
	if fake.sent[0].Message == "" {
		t.Error("评审会话已创建，但收到的是空 brief")
	}
}

// 没有 brief 就不派：说不清要对方做什么，派出去的只是个空会话。
func TestReviewWithoutABriefIsRefused(t *testing.T) {
	fake := &fakeLauncher{res: agentdeck.LaunchResult{SessionID: "r1", Success: true}}
	d, _ := newDispatcher(t, fake)
	dispatchExecutorFirst(t, d, fake, "exec-sess")
	before := len(fake.calls)

	// 纯空白与空串同样处理（n039 attempt 4 评审：纯空白 brief 曾被记成已派）。
	for _, msg := range []string{"", " \n\t"} {
		req := goodReview()
		req.Message = msg
		if _, err := d.DispatchReviewer(context.Background(), req); err == nil {
			t.Fatalf("brief 为 %q 时必须拒绝", msg)
		} else if !strings.Contains(err.Error(), "空会话") {
			t.Errorf("拒绝理由不清楚：%v", err)
		}
		if len(fake.calls) != before || len(fake.sent) != 0 {
			t.Errorf("brief 为 %q 时不该启动会话或投递：%+v %+v", msg, fake.calls, fake.sent)
		}
		if _, ok := d.Store.Get(ReviewerKey(req.NodeID), req.Attempt); ok {
			t.Errorf("brief 为 %q 时不该落评审绑定", msg)
		}
	}
}

// brief 送不进去就不算派出：不落绑定，并明确报告那个半成品会话。
func TestBriefDeliveryFailureLeavesNoBinding(t *testing.T) {
	fake := &fakeLauncher{}
	d, _ := newDispatcher(t, fake)
	dispatchExecutorFirst(t, d, fake, "exec-sess")

	fake.res = agentdeck.LaunchResult{SessionID: "review-sess", Success: true}
	fake.sendErr = errors.New("对方一直没就绪")
	_, err := d.DispatchReviewer(context.Background(), goodReview())
	if err == nil {
		t.Fatal("brief 送不进去必须报错")
	}
	if !strings.Contains(err.Error(), "半成品会话") {
		t.Errorf("没说清出了什么事：%v", err)
	}
	if !strings.Contains(err.Error(), "review-sess") {
		t.Errorf("拒绝信息里没给出那个会话号，人没法收拾：%v", err)
	}
	if _, ok := d.Store.Get(ReviewerKey("n099_demo"), 1); ok {
		t.Error("brief 没送到，却记下了一条「已派评审」的绑定")
	}
}

// 撞会话时，那个会话必须还没收到任何 brief。
func TestSessionCollisionHappensBeforeAnyDelivery(t *testing.T) {
	fake := &fakeLauncher{}
	d, _ := newDispatcher(t, fake)
	dispatchExecutorFirst(t, d, fake, "same-sess")

	fake.res = agentdeck.LaunchResult{SessionID: "same-sess", Success: true}
	if _, err := d.DispatchReviewer(context.Background(), goodReview()); err == nil {
		t.Fatal("撞上执行者会话必须拒绝")
	}
	if len(fake.sent) != 0 {
		t.Errorf("撞会话了却还是把 brief 送了出去：%+v", fake.sent)
	}
	if _, ok := d.Store.Get(ReviewerKey("n099_demo"), 1); ok {
		t.Error("撞会话被拒了，评审绑定却存住了")
	}
}
