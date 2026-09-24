package dispatch

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/binding"
	"github.com/Tingschen287/terminal-deck/work/internal/routing"
)

// fakeLauncher 记下被请求的内容，并按脚本返回。
type fakeLauncher struct {
	calls []agentdeck.LaunchRequest
	res   agentdeck.LaunchResult
	err   error
	// sent 记下每一次投递：会话号和文本。
	sent []sentMessage
	// sendErr 让投递失败。
	sendErr error
}

// sentMessage 是一次投递。测试靠它确认评审会话真的收到了 brief。
type sentMessage struct {
	SessionID string
	Message   string
}

func (f *fakeLauncher) Launch(ctx context.Context, req agentdeck.LaunchRequest) (agentdeck.LaunchResult, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return agentdeck.LaunchResult{}, f.err
	}
	return f.res, nil
}

func (f *fakeLauncher) SendMessage(ctx context.Context, sessionID, message string) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, sentMessage{SessionID: sessionID, Message: message})
	return nil
}

func table(t *testing.T) *RouteTable {
	t.Helper()
	tbl, err := NewRouteTable([]Model{
		{ID: "claude-opus-5", Tool: "claude", Class: Frontier},
		{ID: "claude-sonnet-5", Tool: "claude", Class: Balanced},
		{ID: "claude-haiku-4-5", Tool: "claude", Class: Economy},
		{ID: "kimi-code/k3-256k", Tool: "kimi", Class: Balanced},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tbl
}

func newDispatcher(t *testing.T, launcher Launcher) (*Dispatcher, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := binding.Open(filepath.Join(dir, "bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "dispatch-log.jsonl")
	return &Dispatcher{
		Deck: launcher, Store: store, Table: table(t), LogPath: logPath,
		// n039：每次派单前写路由审计记录，没有路径就不派。
		RoutingLog: filepath.Join(dir, "routing-decisions.jsonl"),
		Now:        func() time.Time { return time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC) },
	}, logPath
}

func goodRequest() Request {
	return Request{
		NodeID: "n099_demo", Attempt: 1, RequiredClass: Frontier,
		ModelID: "claude-opus-5", ProjectPath: "/tmp/p",
		Title: "demo", Group: "g", DispatchedBy: "tester",
	}
}

func readLog(t *testing.T, path string) []Outcome {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("读不到派工流水：%v", err)
	}
	defer f.Close()
	var out []Outcome
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var o Outcome
		if err := json.Unmarshal(sc.Bytes(), &o); err != nil {
			t.Fatalf("流水行不是合法 JSON：%v", err)
		}
		out = append(out, o)
	}
	return out
}

func TestDispatchHappyPathRecordsBindingAndLog(t *testing.T) {
	fake := &fakeLauncher{res: agentdeck.LaunchResult{
		SessionID: "sess-9", Success: true, Profile: "_n036fixture",
		Tool: "claude", ModelID: "claude-opus-5",
	}}
	d, logPath := newDispatcher(t, fake)

	b, err := d.Dispatch(context.Background(), goodRequest())
	if err != nil {
		t.Fatalf("正常派工不该失败：%v", err)
	}
	if b.SessionID != "sess-9" {
		t.Errorf("绑定里的会话号是 %q", b.SessionID)
	}
	if b.Model != "claude-opus-5" || b.RouteClass != "frontier" {
		t.Errorf("绑定没记住实际请求的模型与档位：%+v", b)
	}
	// 传给上游的必须是模型的真实 ID 和它对应的工具。
	if len(fake.calls) != 1 {
		t.Fatalf("应当只调一次上游，实际 %d 次", len(fake.calls))
	}
	if fake.calls[0].Model != "claude-opus-5" || fake.calls[0].Tool != "claude" {
		t.Errorf("传给上游的参数不对：%+v", fake.calls[0])
	}

	log := readLog(t, logPath)
	if len(log) != 1 || !log[0].OK || log[0].SessionID != "sess-9" {
		t.Errorf("派工流水没记对：%+v", log)
	}
	if log[0].ModelID != "claude-opus-5" || log[0].ModelClass != "frontier" {
		t.Errorf("流水没记下实际请求的模型：%+v", log[0])
	}
}

// 模型不在档位表里就拒绝，不猜一个默认的。
func TestUnknownModelIsRefusedNotGuessed(t *testing.T) {
	fake := &fakeLauncher{}
	d, logPath := newDispatcher(t, fake)

	req := goodRequest()
	req.ModelID = "某个没人认识的模型"
	if _, err := d.Dispatch(context.Background(), req); err == nil {
		t.Fatal("未知模型必须被拒绝")
	}
	if len(fake.calls) != 0 {
		t.Error("被拒绝的派工不该真的去启动会话")
	}
	log := readLog(t, logPath)
	if len(log) != 1 || log[0].OK || !strings.Contains(log[0].Refusal, "不猜默认值") {
		t.Errorf("拒绝理由没记进流水：%+v", log)
	}
}

func TestEmptyModelIsRefused(t *testing.T) {
	fake := &fakeLauncher{}
	d, _ := newDispatcher(t, fake)
	req := goodRequest()
	req.ModelID = ""
	if _, err := d.Dispatch(context.Background(), req); err == nil {
		t.Fatal("模型为空必须被拒绝")
	}
	if len(fake.calls) != 0 {
		t.Error("被拒绝的派工不该真的去启动会话")
	}
}

// 节点要 frontier，就不许拿 balanced 的模型去顶。
func TestDowngradeIsRefused(t *testing.T) {
	fake := &fakeLauncher{}
	d, logPath := newDispatcher(t, fake)

	req := goodRequest()
	req.ModelID = "claude-sonnet-5" // balanced
	if _, err := d.Dispatch(context.Background(), req); err == nil {
		t.Fatal("降档派工必须被拒绝")
	}
	if len(fake.calls) != 0 {
		t.Error("降档的派工不该真的去启动会话")
	}
	log := readLog(t, logPath)
	if len(log) != 1 || !strings.Contains(log[0].Refusal, "拒绝降档") {
		t.Errorf("降档拒绝没记进流水：%+v", log)
	}
}

// 反过来，拿更高档的模型做低档的活是允许的。
func TestUpgradeIsAllowed(t *testing.T) {
	fake := &fakeLauncher{res: agentdeck.LaunchResult{SessionID: "s", Success: true}}
	d, _ := newDispatcher(t, fake)
	req := goodRequest()
	req.RequiredClass = Balanced
	req.ModelID = "claude-opus-5" // frontier
	if _, err := d.Dispatch(context.Background(), req); err != nil {
		t.Fatalf("用更高档的模型不该被拒绝：%v", err)
	}
}

func TestUnknownRouteClassIsRefused(t *testing.T) {
	fake := &fakeLauncher{}
	d, _ := newDispatcher(t, fake)
	req := goodRequest()
	req.RequiredClass = RouteClass("超级档")
	if _, err := d.Dispatch(context.Background(), req); err == nil {
		t.Fatal("不认识的 route class 必须被拒绝")
	}
	if len(fake.calls) != 0 {
		t.Error("不该真的去启动会话")
	}
}

// 同一次 attempt 不许派两次，否则没人说得清哪个会话才算数。
func TestDuplicateDispatchForSameAttemptIsRefused(t *testing.T) {
	fake := &fakeLauncher{res: agentdeck.LaunchResult{SessionID: "sess-1", Success: true}}
	d, logPath := newDispatcher(t, fake)

	if _, err := d.Dispatch(context.Background(), goodRequest()); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Dispatch(context.Background(), goodRequest()); err == nil {
		t.Fatal("同一次 attempt 重复派工必须被拒绝")
	}
	if len(fake.calls) != 1 {
		t.Errorf("上游应当只被调一次，实际 %d 次", len(fake.calls))
	}
	log := readLog(t, logPath)
	if len(log) != 2 || log[1].OK || !strings.Contains(log[1].Refusal, "不重复派工") {
		t.Errorf("重复派工的拒绝没记对：%+v", log)
	}
}

// 上游起不来时不能留下绑定记录。
func TestFailedLaunchLeavesNoBinding(t *testing.T) {
	fake := &fakeLauncher{err: errors.New("上游炸了")}
	d, _ := newDispatcher(t, fake)

	if _, err := d.Dispatch(context.Background(), goodRequest()); err == nil {
		t.Fatal("上游失败时必须报错")
	}
	if _, ok := d.Store.Get("n099_demo", 1); ok {
		t.Error("启动失败却留下了绑定记录")
	}
}

func TestRouteTableRejectsAmbiguousEntries(t *testing.T) {
	if _, err := NewRouteTable([]Model{
		{ID: "m", Tool: "t", Class: Frontier},
		{ID: "m", Tool: "t", Class: Balanced},
	}); err == nil {
		t.Error("同一个模型两种档位说法必须报错")
	}
	if _, err := NewRouteTable([]Model{{ID: "m", Tool: "t", Class: RouteClass("怪档")}}); err == nil {
		t.Error("不认识的档位必须报错")
	}
	if _, err := NewRouteTable([]Model{{ID: "m", Class: Frontier}}); err == nil {
		t.Error("没写工具的模型必须报错")
	}
}

func TestEmptyRouteTableRefusesEverything(t *testing.T) {
	var empty *RouteTable
	if _, err := empty.Lookup("任何模型"); err == nil {
		t.Error("没有档位表时必须拒绝，而不是放行")
	}
}

func TestCandidatesPreferTheCheapestThatQualifies(t *testing.T) {
	got := table(t).CandidatesFor(Balanced)
	if len(got) == 0 {
		t.Fatal("应当有满足 balanced 的模型")
	}
	if got[0].Class != Balanced {
		t.Errorf("第一个候选应当是刚好够用的那档，实际是 %s", got[0].Class)
	}
	for _, m := range got {
		if m.Class == Economy {
			t.Errorf("economy 的 %s 不该出现在 balanced 的候选里", m.ID)
		}
	}
}

// 每次派单都留一条路由审计记录；写不进去就不派，上游一次都不调。
func TestEveryDispatchLeavesARoutingRecord(t *testing.T) {
	l := &fakeLauncher{res: agentdeck.LaunchResult{SessionID: "s-1", Success: true}}
	d, _ := newDispatcher(t, l)
	if _, err := d.Dispatch(context.Background(), goodRequest()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(d.RoutingLog)
	if err != nil {
		t.Fatal(err)
	}
	var rec routing.Decision
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.NodeID != "n099_demo" || rec.Chosen != "claude-opus-5" || len(rec.Candidates) == 0 || rec.QuotaBasis == "" {
		t.Errorf("记录缺字段：%+v", rec)
	}

	l2 := &fakeLauncher{res: agentdeck.LaunchResult{SessionID: "s-2", Success: true}}
	d2, _ := newDispatcher(t, l2)
	d2.RoutingLog = filepath.Join(t.TempDir(), "no-such-dir", "sub", "\x00bad")
	req := goodRequest()
	if _, err := d2.Dispatch(context.Background(), req); err == nil {
		t.Fatal("审计记录写不进去时必须拒绝派单")
	}
	if len(l2.calls) != 0 {
		t.Errorf("拒绝时不该调上游，实际调了 %d 次", len(l2.calls))
	}
}

// routing 包的档位高低与本包一致。
func TestRoutingRanksMatchDispatch(t *testing.T) {
	classes := []RouteClass{Economy, Balanced, Frontier, Human}
	for _, a := range classes {
		for _, b := range classes {
			d := routing.Decide(routing.Request{RequiredClass: string(b)},
				[]routing.Candidate{{ID: "m", Tool: "t", Class: string(a)}}, nil, time.Now())
			if d.Candidates[0].MeetsRouteClass != a.Meets(b) {
				t.Errorf("%s 能否满足 %s：routing 说 %v，dispatch 说 %v", a, b, d.Candidates[0].MeetsRouteClass, a.Meets(b))
			}
		}
	}
}
