package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"


	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/binding"
	"github.com/Tingschen287/terminal-deck/work/internal/dispatch"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

// 这一组测试把界面接到**真实**的 dispatch.Dispatcher 上，只把最上游那一步换成假的。
//
// 为什么要单独有这一组：上一版的评审测试全部只走 fakeDispatcher，
// 于是界面往请求里塞了什么、domain 究竟认不认，两边各测各的，中间那段没人看。
// 那段正是出事的地方——界面拼了一个 "work-deck-reviewer-<模型名>" 当评审者身份，
// domain 拿它和执行者比，永远不相等，独立性检查就此失效，而两边的单测都是绿的。

// recordingLauncher 是真实 Dispatcher 底下那一层假上游。
//
// 它只记下被要求启动什么，并按预设返回会话号；除此之外什么都不做。
type recordingLauncher struct {
	next    string
	calls   []agentdeck.LaunchRequest
	sent    [][2]string
	sendErr error
}

func (l *recordingLauncher) Launch(ctx context.Context, req agentdeck.LaunchRequest) (agentdeck.LaunchResult, error) {
	l.calls = append(l.calls, req)
	return agentdeck.LaunchResult{SessionID: l.next, Success: true}, nil
}

// SendMessage 记下 brief 投给了谁、投了什么。
//
// 这条是界面这一侧最要紧的证据：评审会话起来之后到底有没有收到任务。
// 上一版没有这条记录，于是「派出去的是个空会话」这件事一直没人发现。
func (l *recordingLauncher) SendMessage(ctx context.Context, sessionID, message string) error {
	if l.sendErr != nil {
		return l.sendErr
	}
	l.sent = append(l.sent, [2]string{sessionID, message})
	return nil
}

// realDispatcher 造一个真的派工器：真实拒绝逻辑、真实绑定存储，假上游。
func realDispatcher(t *testing.T, launcher dispatch.Launcher) *dispatch.Dispatcher {
	t.Helper()
	dir := t.TempDir()
	store, err := binding.Open(filepath.Join(dir, "bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	table, err := dispatch.NewRouteTable([]dispatch.Model{
		{ID: "claude-opus-5", Tool: "claude", Class: dispatch.Frontier},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &dispatch.Dispatcher{
		Deck: launcher, Store: store, Table: table,
		LogPath:    filepath.Join(dir, "dispatch-log.jsonl"),
		RoutingLog: filepath.Join(dir, "routing-decisions.jsonl"),
		Now:        func() time.Time { return time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC) },
	}
}

// 界面上按出来的那次派评审，要能被真实 domain 认下来，并且落到一个新会话上。
func TestUIReviewDispatchReachesRealDispatcher(t *testing.T) {
	lau := &recordingLauncher{next: "review-sess"}
	d := realDispatcher(t, lau)

	// 先让绑定存储里真的有一条执行会话，就像这个节点是 Work Deck 派出去的那样。
	if err := d.Store.Put(binding.Binding{
		NodeID: "n1", Attempt: 2, SessionID: "exec-sess",
		Tool: "claude", Model: "claude-opus-5", RouteClass: "frontier",
	}); err != nil {
		t.Fatal(err)
	}

	m := modelWithDispatcherAndBindings(t,
		[]projectctl.Node{submittedNode("n1", 2, "executor-a1")}, d, d.Store)

	m = pressD(m)
	if m.mode != ModePickModel {
		t.Fatalf("按 d 应当进入挑模型，实际提示：%q", m.notice)
	}
	next, cmd := enterWithReason(t, m, "评审用它")
	if cmd == nil {
		t.Fatal("回车应当真的派出评审")
	}
	done, ok := cmd().(dispatchDoneMsg)
	if !ok || !done.ok {
		t.Fatalf("经真实 domain 的派评审失败了：%+v", done)
	}
	if len(lau.calls) != 1 {
		t.Fatalf("应当恰好启动一个评审会话，实际 %d 个", len(lau.calls))
	}

	// 这一条是独立评审给出的：光起会话不算派出评审。
	// 上一版界面从不设置 Message，起来的会话一直是空的，而所有测试都绿着。
	if len(lau.sent) != 1 {
		t.Fatalf("评审会话已创建，但 brief 投递了 %d 次，期望 1 次", len(lau.sent))
	}
	if lau.sent[0][0] != "review-sess" {
		t.Errorf("brief 投给了 %q，期望 review-sess", lau.sent[0][0])
	}
	if lau.sent[0][1] == "" {
		t.Fatal("评审会话已创建，但没有收到任何评审 brief")
	}
	// brief 得说清楚是哪个节点、第几次、以及独立性要求。
	for _, want := range []string{"n1", "attempt", "独立", "VERDICT"} {
		if !strings.Contains(lau.sent[0][1], want) {
			t.Errorf("brief 里没提到 %q：\n%s", want, lau.sent[0][1])
		}
	}
	// launch 那一步带不了 brief：LaunchRequest 没有消息字段（n039），编译期就挡住了。

	// 评审绑定要落在评审那个键上，且不是执行者的会话。
	rb, ok := d.Store.Get(dispatch.ReviewerKey("n1"), 2)
	if !ok {
		t.Fatal("评审派出去了，绑定却没存住")
	}
	if rb.SessionID != "review-sess" {
		t.Errorf("评审绑到了 %q，期望 review-sess", rb.SessionID)
	}
	if eb, _ := d.Store.Get("n1", 2); eb.SessionID != "exec-sess" {
		t.Errorf("执行者的绑定被动了：%+v", eb)
	}
	_ = next
}

// 绑定存储里没有执行会话时，这条路必须在界面上就走不通。
//
// 这是 fail-closed：证明不了独立，就不派。
func TestUIRefusesReviewWhenExecutorSessionIsUnknown(t *testing.T) {
	lau := &recordingLauncher{next: "review-sess"}
	d := realDispatcher(t, lau)

	// 注意：没有往存储里放执行绑定。
	m := modelWithDispatcherAndBindings(t,
		[]projectctl.Node{submittedNode("n1", 2, "executor-a1")}, d, d.Store)

	after := pressD(m)
	if after.mode == ModePickModel {
		t.Fatal("查不到执行会话时不该进入挑模型")
	}
	if !strings.Contains(after.notice, "证明不了独立性") {
		t.Errorf("没说清为什么不派：%q", after.notice)
	}
	if len(lau.calls) != 0 {
		t.Error("被拒绝的派评审不该真的去启动会话")
	}
}

// 就算绕过界面那道预检直接送到 domain，独立性也不能靠请求里的身份字段放行。
//
// 这条是上一版那个 bug 的回归测试：界面能填的字段，domain 一律不采信。
func TestRealDispatcherIgnoresCallerSuppliedReviewerIdentity(t *testing.T) {
	lau := &recordingLauncher{next: "review-sess"}
	d := realDispatcher(t, lau)

	// 没有执行绑定。无论请求里把执行者写成谁，都必须拒。
	for _, actor := range []string{"executor-a1", "work-deck-reviewer-claude-opus-5"} {
		_, err := d.DispatchReviewer(context.Background(), dispatch.ReviewRequest{
			NodeID: "n1", Attempt: 2, RequiredClass: dispatch.Frontier,
			ModelID: "claude-opus-5", ProjectPath: "/tmp/p",
			ExecutorActor: actor, IndependentRequired: true, DispatchedBy: "work-deck", ChosenBy: "owner", ChoiceReason: "测试里指定",
			Message: "请评审 n1 的 attempt 2",
		})
		if err == nil {
			t.Errorf("执行者身份填成 %q 就放行了", actor)
			continue
		}
		if !strings.Contains(err.Error(), "证明不了评审独立") {
			t.Errorf("拒绝理由不对：%v", err)
		}
	}
	if len(lau.calls) != 0 {
		t.Error("以上都该在启动会话之前就被拒")
	}
}
