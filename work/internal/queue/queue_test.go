package queue

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/binding"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

var t0 = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func node(id, kind, risk string, st projectctl.State, attempt int) projectctl.Node {
	n := projectctl.Node{ID: id, Title: id + " 标题", Kind: kind, Risk: risk, State: projectctl.Known(st)}
	if attempt > 0 {
		n.Attempt = projectctl.Known(attempt)
	} else {
		n.Attempt = projectctl.Unknown[int]("没开过工")
	}
	return n
}

func fixtureSnapshot() projectctl.Snapshot {
	blocked := node("f04_external_probe", "task", "high", projectctl.StateBlocked, 1)
	blocked.RequiredAction = "owner 决定是否为该接口另开只读账号"
	return projectctl.Snapshot{Nodes: []projectctl.Node{
		node("f01_collect_inputs", "task", "low", projectctl.StateAccepted, 1),
		node("f02_build_core", "task", "medium", projectctl.StateRunning, 2),
		blocked,
		node("f06_owner_gate", "human_gate", "high", projectctl.StateReady, 0),
		{ID: "f07_ship_result", Kind: "task", State: projectctl.Unknown[projectctl.State]("取不到")},
	}}
}

func waiting(child string, hash string, ts time.Time) agentdeck.InboxRecord {
	return agentdeck.InboxRecord{ChildSessionID: child, ChildTitle: "worker " + child, Profile: "_wd",
		FromStatus: "running", ToStatus: "waiting", Timestamp: ts, LastOutputHash: hash,
		TargetSessionID: "parent-1", TargetKind: "parent", TurnFingerprint: child + "@abcd"}
}

type fakeBindings map[string]binding.Binding

func (f fakeBindings) Get(n string, a int) (binding.Binding, bool) {
	b, ok := f[n+"#"+strconv.Itoa(a)]
	return b, ok
}

// Graph 的门与运行时会话的待决事项进同一个队列。
func TestGraphAndSessionItemsShareOneQueue(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "queue.json"))
	graph := FromGraph(fixtureSnapshot(), fakeBindings{"f04_external_probe#1": {SessionID: "sess-f04"}}, "/x/projectctl.py", t0)
	if _, err := s.Merge(SourceGraph, graph, t0); err != nil {
		t.Fatal(err)
	}
	sess := FromInbox([]agentdeck.InboxRecord{waiting("c1", "h1", t0)}, t0)
	if _, err := s.Merge(SourceSession, sess, t0); err != nil {
		t.Fatal(err)
	}
	var cats []string
	for _, it := range s.Items() {
		cats = append(cats, string(it.Category))
	}
	sort.Strings(cats)
	want := []string{"graph_blocked", "graph_human_gate", "session_waiting"}
	if !reflect.DeepEqual(cats, want) {
		t.Fatalf("队列里的类别是 %v，期望 %v", cats, want)
	}
	// 被阻塞节点绑定的会话要算进 blocking_sessions。
	for _, it := range s.Items() {
		if it.Source.NodeID == "f04_external_probe" && !reflect.DeepEqual(it.BlockingSessions, []string{"sess-f04"}) {
			t.Errorf("blocked 节点的 blocking_sessions 是 %v", it.BlockingSessions)
		}
	}
}

// 状态取不到的节点不进队列，也不猜。accepted、running 的普通节点不是待决事项。
func TestGraphOnlyPicksGatesAndBlocked(t *testing.T) {
	items := FromGraph(fixtureSnapshot(), nil, "p", t0)
	if len(items) != 2 {
		t.Fatalf("应当只挑出 human gate 与 blocked 两条，拿到 %d 条", len(items))
	}
	for _, it := range items {
		if it.Recommendation.Confidence != ConfidenceNone || it.Recommendation.Option != "" {
			t.Errorf("%s：Graph 门不该有推荐：%+v", it.ID, it.Recommendation)
		}
	}
}

// 同一条目重复上报被去重：同批重复、跨轮重复、daemon 重启后换了时间戳重发。
func TestDuplicateReportsDedupe(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "queue.json"))
	batch := []agentdeck.InboxRecord{waiting("c1", "h1", t0), waiting("c1", "h1", t0)}
	if n, _ := s.Merge(SourceSession, FromInbox(batch, t0), t0); n != 1 {
		t.Fatalf("同批两条相同记录应当只新增 1 条，新增了 %d", n)
	}
	later := t0.Add(10 * time.Minute)
	// daemon 重启：同一轮用新时间戳重发。
	if n, _ := s.Merge(SourceSession, FromInbox([]agentdeck.InboxRecord{waiting("c1", "h1", later)}, later), later); n != 0 {
		t.Fatalf("换时间戳重发的同一轮不该新增，新增了 %d", n)
	}
	if got := len(s.Items()); got != 1 {
		t.Fatalf("队列里应当只有 1 条，实际 %d", got)
	}
	it := s.Items()[0]
	if !it.FirstSeen.Equal(t0) || !it.LastSeen.Equal(later) {
		t.Errorf("重复上报应当只刷新 last_seen：first=%v last=%v", it.FirstSeen, it.LastSeen)
	}
	// 输出不同的下一轮等待是另一条。
	if n, _ := s.Merge(SourceSession, FromInbox([]agentdeck.InboxRecord{waiting("c1", "h2", later)}, later), later); n != 1 {
		t.Errorf("新一轮等待应当新增 1 条，新增了 %d", n)
	}
	// Graph 条目重复上报同样去重。
	g := FromGraph(fixtureSnapshot(), nil, "p", t0)
	s.Merge(SourceGraph, g, t0)
	if n, _ := s.Merge(SourceGraph, g, later); n != 0 {
		t.Errorf("Graph 条目重复上报不该新增，新增了 %d", n)
	}
}

// 队列持久化，重启后恢复；已回答的条目重启后还是已回答，再次上报也不会重新打开。
func TestQueueSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	s, _ := Open(path)
	recs := []agentdeck.InboxRecord{waiting("c1", "h1", t0)}
	s.Merge(SourceSession, FromInbox(recs, t0), t0)
	id := s.Items()[0].ID
	d := &fakeDeliverer{}
	if _, err := Respond(context.Background(), s, d, id, "continue", t0); err != nil {
		t.Fatal(err)
	}

	// 模拟崩溃：丢掉内存里的 Store，从磁盘重新打开。
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	it, ok := s2.Get(id)
	if !ok || it.Status != StatusAnswered || it.Answer == nil || !it.Answer.Delivered {
		t.Fatalf("重启后回答丢了：%+v", it)
	}
	s2.Merge(SourceSession, FromInbox(recs, t0.Add(time.Hour)), t0.Add(time.Hour))
	if it, _ := s2.Get(id); it.Status != StatusAnswered {
		t.Errorf("已回答的条目被重复上报重新打开了：%s", it.Status)
	}
	if len(s2.Items()) != 1 {
		t.Errorf("重启后重复上报不该多出条目，现在有 %d 条", len(s2.Items()))
	}
}

// 崩溃时留下的临时文件不影响重开；落盘的主文件总是完整的。
func TestLeftoverTempFileDoesNotBreakReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.json")
	s, _ := Open(path)
	s.Merge(SourceSession, FromInbox([]agentdeck.InboxRecord{waiting("c1", "h1", t0)}, t0), t0)
	// 写到一半断电留下的半个临时文件。
	os.WriteFile(filepath.Join(dir, ".queue-123.tmp"), []byte(`{"version":1,"items":[{"id":`), 0o600)
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.Items()) != 1 {
		t.Errorf("重开后应当还是 1 条，实际 %d", len(s2.Items()))
	}
}

// 队列文件坏了不能当成空队列打开：那会悄悄丢掉已回答的记录。
func TestCorruptQueueFileIsNotAnEmptyQueue(t *testing.T) {
	for name, content := range map[string]string{
		"截断的 JSON":     `{"version":1,"items":[`,
		"缺 items":      `{"version":1}`,
		"items 为 null": `{"version":1,"items":null}`,
		"版本不对":         `{"version":2,"items":[]}`,
		"条目缺 id":       `{"version":1,"items":[{"category":"session_waiting","status":"open","source":{"kind":"session","present":true},"options":[{"key":"ack","label":"x"}]}]}`,
		"条目状态乱写":       `{"version":1,"items":[{"id":"x","category":"session_waiting","status":"??","source":{"kind":"session","present":true},"options":[{"key":"ack","label":"x"}]}]}`,
		"条目没有选项":       `{"version":1,"items":[{"id":"x","category":"session_waiting","status":"open","source":{"kind":"session","present":true},"options":[]}]}`,
	} {
		path := filepath.Join(t.TempDir(), "queue.json")
		os.WriteFile(path, []byte(content), 0o600)
		if _, err := Open(path); err == nil {
			t.Errorf("%s：应当报错，不能当成空队列", name)
		}
	}
	// 合法的空队列照常打开。
	path := filepath.Join(t.TempDir(), "queue.json")
	os.WriteFile(path, []byte(`{"version":1,"items":[]}`), 0o600)
	if _, err := Open(path); err != nil {
		t.Errorf("合法的空队列应当能打开：%v", err)
	}
}

// Graph 节点换了状态，open 的条目自动转为 resolved；回到原状态时重新打开。
func TestGraphItemResolvesWhenNodeMoves(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "queue.json"))
	snap := fixtureSnapshot()
	s.Merge(SourceGraph, FromGraph(snap, nil, "p", t0), t0)
	snap.Nodes[2].State = projectctl.Known(projectctl.StateRunning) // f04 被放行
	s.Merge(SourceGraph, FromGraph(snap, nil, "p", t0), t0)
	it, _ := s.Get("graph:f04_external_probe#1:blocked")
	if it.Status != StatusResolved {
		t.Errorf("节点放行后条目应当 resolved，实际 %s", it.Status)
	}
}

// 记录被 conductor drain 走后，条目保留但标出来。
func TestSessionItemMarkedAbsentWhenDrained(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "queue.json"))
	s.Merge(SourceSession, FromInbox([]agentdeck.InboxRecord{waiting("c1", "h1", t0)}, t0), t0)
	s.Merge(SourceSession, nil, t0.Add(time.Minute))
	it := s.Items()[0]
	if it.Status != StatusOpen || it.Source.Present {
		t.Errorf("drain 后应当仍是 open 且 present=false：%+v", it)
	}
}

type fakeDeliverer struct {
	calls []agentdeck.InboxRecord
	msgs  []string
	err   error
}

func (f *fakeDeliverer) DeliverAnswer(_ context.Context, rec agentdeck.InboxRecord, msg string) error {
	f.calls = append(f.calls, rec)
	f.msgs = append(f.msgs, msg)
	return f.err
}

// 回答会话条目：把选项的文字投递回发起会话（child_session_id，不是父会话）。
func TestAnswerDeliversToOriginatingSession(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "queue.json"))
	s.Merge(SourceSession, FromInbox([]agentdeck.InboxRecord{waiting("c1", "h1", t0)}, t0), t0)
	id := s.Items()[0].ID
	d := &fakeDeliverer{}
	it, err := Respond(context.Background(), s, d, id, "continue", t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.calls) != 1 || d.calls[0].ChildSessionID != "c1" || d.calls[0].Profile != "_wd" || d.msgs[0] != "继续" {
		t.Fatalf("投递不对：%+v %v", d.calls, d.msgs)
	}
	if it.Status != StatusAnswered || !it.Answer.Delivered {
		t.Errorf("投递成功后应当 answered 且 delivered：%+v", it)
	}
}

// 投递失败：条目留在 open，失败原因记下来，owner 可以重试。
func TestFailedDeliveryStaysOpen(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "queue.json"))
	s.Merge(SourceSession, FromInbox([]agentdeck.InboxRecord{waiting("c1", "h1", t0)}, t0), t0)
	id := s.Items()[0].ID
	it, err := Respond(context.Background(), s, &fakeDeliverer{err: errors.New("会话不在了")}, id, "continue", t0)
	if err != nil {
		t.Fatal(err)
	}
	if it.Status != StatusOpen || it.Answer.Delivered || !strings.Contains(it.Answer.DeliveryError, "会话不在了") {
		t.Errorf("投递失败应当留在 open 并记下原因：%+v", it)
	}
}

// 「已处理」只记状态，不给会话发任何东西。
func TestAckDoesNotMessageTheSession(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "queue.json"))
	s.Merge(SourceSession, FromInbox([]agentdeck.InboxRecord{waiting("c1", "h1", t0)}, t0), t0)
	d := &fakeDeliverer{}
	if _, err := Respond(context.Background(), s, d, s.Items()[0].ID, "ack", t0); err != nil {
		t.Fatal(err)
	}
	if len(d.calls) != 0 {
		t.Errorf("ack 不该投递：%v", d.msgs)
	}
}

// Graph 条目：Work Deck 不写账本，只把命令交给 owner；不投递任何东西。
func TestGraphAnswerOnlyHandsOverCommand(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "queue.json"))
	s.Merge(SourceGraph, FromGraph(fixtureSnapshot(), nil, "/x/projectctl.py", t0), t0)
	d := &fakeDeliverer{}
	it, err := Respond(context.Background(), s, d, "graph:f04_external_probe#1:blocked", "resume", t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.calls) != 0 {
		t.Error("Graph 条目不该投递到任何会话")
	}
	if !strings.Contains(it.Answer.Note, "python3 /x/projectctl.py resume f04_external_probe") {
		t.Errorf("没把命令交给 owner：%q", it.Answer.Note)
	}
	// 门还在：条目留在 open，仍计入待拍板；Graph 状态没变时再合并也还是 open。
	if it.Status != StatusOpen {
		t.Errorf("Graph 条目选了选项后应当仍是 open，实际 %s", it.Status)
	}
	before := s.OpenCount()
	s.Merge(SourceGraph, FromGraph(fixtureSnapshot(), nil, "/x/projectctl.py", t0), t0.Add(time.Minute))
	if got, _ := s.Get("graph:f04_external_probe#1:blocked"); got.Status != StatusOpen || s.OpenCount() != before {
		t.Errorf("Graph 状态没变，条目不该离开待拍板：%s", got.Status)
	}
}

// 已回答的会话条目不会被再投一遍。
func TestAnsweredSessionItemIsNotDeliveredTwice(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "queue.json"))
	s.Merge(SourceSession, FromInbox([]agentdeck.InboxRecord{waiting("c1", "h1", t0)}, t0), t0)
	id := s.Items()[0].ID
	d := &fakeDeliverer{}
	Respond(context.Background(), s, d, id, "continue", t0)
	if _, err := Respond(context.Background(), s, d, id, "continue", t0); err == nil {
		t.Error("第二次回答应当被拒绝")
	}
	if len(d.calls) != 1 {
		t.Errorf("只该投递一次，实际 %d 次", len(d.calls))
	}
}

// 投递中途进程中断：重启后结果未知，不自动重投。
func TestInterruptedDeliveryIsNotRetried(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	s, _ := Open(path)
	s.Merge(SourceSession, FromInbox([]agentdeck.InboxRecord{waiting("c1", "h1", t0)}, t0), t0)
	id := s.Items()[0].ID
	if err := s.MarkInFlight(id, "continue", t0); err != nil {
		t.Fatal(err)
	}
	// 模拟投递途中崩溃：结果没记下，从磁盘重开。
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDeliverer{}
	if _, err := Respond(context.Background(), s2, d, id, "continue", t0); err == nil || !strings.Contains(err.Error(), "结果未知") {
		t.Errorf("中断后的条目应当拒绝自动重投：%v", err)
	}
	if len(d.calls) != 0 {
		t.Error("不该重投")
	}
}

type fakeUpstream struct {
	st     agentdeck.WriterStatus
	stErr  error
	recs   []agentdeck.InboxRecord
	recErr error
	calls  []string
	// procDown 为真表示进程表里找不到 daemon；procErr 是查进程表失败。
	procDown bool
	procErr  error
}

func (f *fakeUpstream) InboxWriterStatus(context.Context) (agentdeck.WriterStatus, error) {
	f.calls = append(f.calls, "writer-status")
	return f.st, f.stErr
}
func (f *fakeUpstream) InboxExport(context.Context) ([]agentdeck.InboxRecord, error) {
	f.calls = append(f.calls, "export")
	return f.recs, f.recErr
}
func (f *fakeUpstream) NotifyDaemonRunning(context.Context) (bool, error) {
	f.calls = append(f.calls, "process")
	return !f.procDown, f.procErr
}
func (f *fakeUpstream) ProfileName() string { return "_wd" }

// 先自检再读；只有 daemon 在跑且读取成功时，才没有告警。
func TestPollChecksWriterFirstAndAlertsLoudly(t *testing.T) {
	up := &fakeUpstream{st: agentdeck.WriterStatus{Running: true}}
	in, _ := Poll(context.Background(), up, t0)
	if !reflect.DeepEqual(up.calls, []string{"writer-status", "process", "export"}) {
		t.Errorf("调用顺序应当先自检再读：%v", up.calls)
	}
	if in.Alert() != "" {
		t.Errorf("daemon 在跑且读取成功时不该告警：%q", in.Alert())
	}

	cases := map[string]*fakeUpstream{
		"daemon 没在跑": {st: agentdeck.WriterStatus{Running: false, Detail: "no notify-daemon has ever stamped"}},
		"自检失败":       {stErr: errors.New("exit 1")},
		"读取失败":       {st: agentdeck.WriterStatus{Running: true}, recErr: errors.New("exit 1")},
		"心跳新鲜但进程已退出": {st: agentdeck.WriterStatus{Running: true}, procDown: true},
		// 上游仍答 running（90 秒窗口内），但心跳已 30 秒没更新：daemon 多半刚退出。
		"心跳过了 15 秒门槛": {st: agentdeck.WriterStatus{Running: true, LastHeartbeat: t0.Add(-30 * time.Second)}},
		"查不了进程表":      {st: agentdeck.WriterStatus{Running: true}, procErr: errors.New("permission denied")},
	}
	for name, up := range cases {
		in, _ := Poll(context.Background(), up, t0)
		if in.Alert() == "" {
			t.Errorf("%s：必须告警，不能呈现为空队列", name)
		}
	}
	// daemon 不在时，旧记录照样读回来，但告警不因为有记录就消失。
	up = &fakeUpstream{st: agentdeck.WriterStatus{Running: false}, recs: []agentdeck.InboxRecord{waiting("c1", "h", t0)}}
	in, recs := Poll(context.Background(), up, t0)
	if len(recs) != 1 || in.Alert() == "" {
		t.Errorf("daemon 不在时应当既返回旧记录又告警：recs=%d alert=%q", len(recs), in.Alert())
	}
	if in, _ := Poll(context.Background(), nil, t0); in.Alert() == "" {
		t.Error("没配置 upstream 时必须说明运行时来源取不到")
	}
}

// 只收本 profile 的记录：别的 profile 或没写 profile 的回答投递不过去，不进队列，但要数出来。
func TestPollKeepsOnlyOwnProfileRecords(t *testing.T) {
	mine := waiting("c1", "h1", t0) // profile _wd
	theirs := waiting("c2", "h2", t0)
	theirs.Profile = "work"
	blank := waiting("c3", "h3", t0)
	blank.Profile = ""
	up := &fakeUpstream{st: agentdeck.WriterStatus{Running: true}, recs: []agentdeck.InboxRecord{mine, theirs, blank}}
	in, recs := Poll(context.Background(), up, t0)
	if len(recs) != 1 || recs[0].ChildSessionID != "c1" {
		t.Fatalf("只该收本 profile 的记录：%v", recs)
	}
	if in.Foreign != 2 || !strings.Contains(in.ForeignNote(), "2 条") {
		t.Errorf("没收进来的记录要数出来：foreign=%d note=%q", in.Foreign, in.ForeignNote())
	}
}

// 只有 waiting、error、额度、finished 四类进队列。
func TestOnlyDecisionRecordsBecomeItems(t *testing.T) {
	recs := []agentdeck.InboxRecord{
		waiting("c1", "h1", t0),
		{ChildSessionID: "c2", FromStatus: "running", ToStatus: "error", Substate: "auth-401", Timestamp: t0},
		{ChildSessionID: "c3", FromStatus: "running", ToStatus: "idle", Substate: "usage-limit", Timestamp: t0},
		{ChildSessionID: "c4", Kind: "finished", DoneStatus: "ok", DoneSummary: "done", Timestamp: t0},
		{ChildSessionID: "c5", FromStatus: "running", ToStatus: "idle", Timestamp: t0},
	}
	got := map[string]Category{}
	for _, it := range FromInbox(recs, t0) {
		got[it.Source.SessionID] = it.Category
	}
	want := map[string]Category{"c1": CatSessionWaiting, "c2": CatSessionError, "c3": CatSessionUsageLimit, "c4": CatSessionFinished}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("分类是 %v，期望 %v", got, want)
	}
}

// 落盘文件按 schema.json 校验；枚举与代码常量一一对应。
func TestItemsMatchTheSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	s, _ := Open(path)
	s.Merge(SourceGraph, FromGraph(fixtureSnapshot(), nil, "/x/projectctl.py", t0), t0)
	recs := []agentdeck.InboxRecord{
		waiting("c1", "h1", t0),
		{ChildSessionID: "c2", FromStatus: "running", ToStatus: "error", Timestamp: t0},
		{ChildSessionID: "c4", Kind: "finished", DoneStatus: "ok", DoneSummary: "done", Timestamp: t0},
	}
	s.Merge(SourceSession, FromInbox(recs, t0), t0)
	Respond(context.Background(), s, &fakeDeliverer{}, "graph:f04_external_probe#1:blocked", "resume", t0)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	json.Unmarshal(data, &doc)
	c := loadSchema(t)
	if errs := c.validate(doc); len(errs) != 0 {
		t.Fatalf("落盘文件不符合 schema：\n%s", strings.Join(errs, "\n"))
	}

	defs := c.root["$defs"].(map[string]any)
	enumOf := func(def, prop string) []string {
		p := defs[def].(map[string]any)["properties"].(map[string]any)[prop].(map[string]any)
		return asStrings(p["enum"])
	}
	var cats []string
	for _, c := range AllCategories {
		cats = append(cats, string(c))
	}
	for name, pair := range map[string][2][]string{
		"category":   {enumOf("item", "category"), cats},
		"risk":       {enumOf("item", "risk"), AllRisks},
		"status":     {enumOf("item", "status"), AllStatuses},
		"confidence": {enumOf("recommendation", "confidence"), AllConfidences},
		"source":     {enumOf("source", "kind"), {SourceGraph, SourceSession}},
	} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Errorf("%s 枚举对不上：schema %v，代码 %v", name, pair[0], pair[1])
		}
	}
}

// 投递中断后，owner 预览确认，再下结论：没收到就能重新回答并投递一次；收到了就标为已回答。
func TestInterruptedDeliveryCanBeSettledAfterCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	s, _ := Open(path)
	s.Merge(SourceSession, FromInbox([]agentdeck.InboxRecord{waiting("c1", "h1", t0), waiting("c2", "h2", t0)}, t0), t0)
	items := s.Items()
	lost, got := items[0].ID, items[1].ID
	s.MarkInFlight(lost, "continue", t0)
	s.MarkInFlight(got, "continue", t0)
	s2, _ := Open(path) // 崩溃后重开

	it, err := s2.ResolveInFlight(lost, false, t0)
	if err != nil || it.Status != StatusOpen || it.Answer != nil {
		t.Fatalf("确认没收到后应当回到待拍板、清掉标记：%+v %v", it, err)
	}
	d := &fakeDeliverer{}
	if it, err := Respond(context.Background(), s2, d, lost, "continue", t0); err != nil || !it.Answer.Delivered {
		t.Fatalf("清掉标记后应当能重新投递：%+v %v", it, err)
	}
	if len(d.calls) != 1 {
		t.Errorf("重新投递应当恰好一次，实际 %d", len(d.calls))
	}

	it, err = s2.ResolveInFlight(got, true, t0)
	if err != nil || it.Status != StatusAnswered || !it.Answer.Delivered || it.Answer.InFlight {
		t.Fatalf("确认收到后应当已回答：%+v %v", it, err)
	}
	if _, err := s2.ResolveInFlight(got, true, t0); err == nil {
		t.Error("没有中断投递的条目不该再结案")
	}
}

// 落盘失败时内存退回原样：不能留下一个实际没发的在途标记，等下一次落盘带上去。
func TestFailedFlushLeavesNoPhantomInFlight(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.json")
	s, _ := Open(path)
	s.Merge(SourceSession, FromInbox([]agentdeck.InboxRecord{waiting("c1", "h1", t0)}, t0), t0)
	id := s.Items()[0].ID
	os.Chmod(dir, 0o500) // 目录不可写：临时文件建不出来
	defer os.Chmod(dir, 0o700)
	if err := s.MarkInFlight(id, "continue", t0); err == nil {
		t.Skip("当前环境下目录只读没能挡住写入（可能以 root 运行），跳过")
	}
	if it, _ := s.Get(id); it.Answer != nil {
		t.Errorf("落盘失败后内存里不该留下在途标记：%+v", it.Answer)
	}
}

// 快照没取全时不能结案旧门：整份没取到就不合并；某个门状态取不到就保留它。
func TestMergeGraphDoesNotResolveGatesItCouldNotSee(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "queue.json"))
	snap := fixtureSnapshot()
	MergeGraph(s, snap, nil, "p", t0)
	gate := "graph:f06_owner_gate#0:ready"
	if it, ok := s.Get(gate); !ok || it.Status != StatusOpen {
		t.Fatalf("门应当在队列里：%+v", it)
	}

	// 整份没取到：零节点、带问题。
	if _, skipped, _ := MergeGraph(s, projectctl.Snapshot{Problems: []string{"brief 失败"}}, nil, "p", t0); !skipped {
		t.Error("零节点带问题的快照应当跳过")
	}
	if it, _ := s.Get(gate); it.Status != StatusOpen {
		t.Errorf("没取到的快照不该结案门：%s", it.Status)
	}

	// 这个门的状态取不到。
	partial := fixtureSnapshot()
	for i := range partial.Nodes {
		if partial.Nodes[i].ID == "f06_owner_gate" {
			partial.Nodes[i].State = projectctl.Unknown[projectctl.State]("brief 超时")
		}
	}
	partial.Problems = []string{"f06 brief 超时"}
	MergeGraph(s, partial, nil, "p", t0)
	if it, _ := s.Get(gate); it.Status != StatusOpen {
		t.Errorf("状态取不到的门不该结案：%s", it.Status)
	}

	// 合法的空 Graph：结案。
	MergeGraph(s, projectctl.Snapshot{}, nil, "p", t0)
	if it, _ := s.Get(gate); it.Status != StatusResolved {
		t.Errorf("合法的空 Graph 应当结案旧门：%s", it.Status)
	}
}

// Merge 落盘失败时整轮退回，不留下没持久化的条目。
func TestFailedMergeFlushRollsBack(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(filepath.Join(dir, "queue.json"))
	s.Merge(SourceSession, FromInbox([]agentdeck.InboxRecord{waiting("c1", "h1", t0)}, t0), t0)
	os.Chmod(dir, 0o500)
	defer os.Chmod(dir, 0o700)
	_, err := s.Merge(SourceSession, FromInbox([]agentdeck.InboxRecord{waiting("c1", "h1", t0), waiting("c2", "h2", t0)}, t0), t0)
	if err == nil {
		t.Skip("当前环境下目录只读没能挡住写入（可能以 root 运行），跳过")
	}
	if n := len(s.Items()); n != 1 {
		t.Errorf("落盘失败后应当退回 1 条，实际 %d", n)
	}
}

// 送达未确认（n042）：算已发出，免得 owner 再答一遍让对方收到两次；但照实写未确认、请他看一眼。
func TestUnconfirmedAnswerCountsAsSentButSaysSo(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "queue.json"))
	s.Merge(SourceSession, FromInbox([]agentdeck.InboxRecord{waiting("c1", "h1", t0)}, t0), t0)
	id := s.Items()[0].ID
	d := &fakeDeliverer{err: &agentdeck.ErrDeliveryUnconfirmed{SessionID: "c1", Detail: "delivery=unverified"}}
	it, err := Respond(context.Background(), s, d, id, "continue", t0)
	if err != nil {
		t.Fatal(err)
	}
	if it.Status != StatusAnswered || !it.Answer.Delivered || it.Answer.DeliveryError != "" || !strings.Contains(it.Answer.Note, "没确认提交") {
		t.Errorf("送达未确认应当算已发出并注明：%+v", it.Answer)
	}
	if _, err := Respond(context.Background(), s, d, id, "continue", t0); err == nil {
		t.Error("已算发出的条目不该再投一遍")
	}
}
