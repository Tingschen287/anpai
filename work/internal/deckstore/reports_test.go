package deckstore

import (
	"strings"
	"testing"
	"time"
)

func mainAndSub(t *testing.T, s *Store) (Session, Session) {
	t.Helper()
	m, err := s.Reserve(launch(Session{Project: "deck", Role: "main", Agent: "claude", Tool: "claude", Model: "m", Tier: Frontier, Title: "主", CreatedBy: "owner"}), "")
	if err != nil {
		t.Fatal(err)
	}
	x := sub(Economy)
	x.Parent = m.Key
	sb, err := s.Reserve(x, "")
	if err != nil {
		t.Fatal(err)
	}
	mm, _ := s.Session(m.Key)
	ss, _ := s.Session(sb.Key)
	return mm, ss
}

// 同一子 agent 的同一次完成只收一次；送达结果只能从 pending 改。
func TestReportsDedupeAndState(t *testing.T) {
	d := testDirs(t)
	s := withProject(t, d, DefaultEnvelope("deck"))
	m, sb := mainAndSub(t, s)
	r1, added, err := s.EnqueueReport(Report{Sub: sb.Key, CompletionID: "c1", Source: ReportActive, Text: "侧栏写完了 \x1b[31m"})
	if err != nil || !added || r1.Main != m.Key || r1.Text != "侧栏写完了 " {
		t.Fatalf("应收下并清洗：%+v %v %v", r1, added, err)
	}
	r2, added, _ := s.EnqueueReport(Report{Sub: sb.Key, CompletionID: "c1", Source: ReportInferred, Text: "再报一次"})
	if added || r2.ID != r1.ID {
		t.Fatalf("同一次完成不该重复：%+v", r2)
	}
	if got := s.PendingReports(m.Key); len(got) != 1 {
		t.Fatalf("应有 1 条待送：%+v", got)
	}
	if err := s.MarkReport(r1.ID, ReportUnconfirmed); err == nil {
		t.Fatal("没进入 sending 不能直接标结果")
	}
	if err := s.MarkReport(r1.ID, ReportSending); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkReport(r1.ID, ReportUnconfirmed); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkReport(r1.ID, ReportDelivered); err == nil {
		t.Fatal("送达未确认的不能再改（不自动重发）")
	}
	s2, _ := Open(d)
	if got := s2.PendingReports(m.Key); len(got) != 0 {
		t.Fatalf("重启后状态应保留：%+v", got)
	}
}

// 汇报只收已登记的子 agent，且它要有主 agent。
func TestReportsRequireRegisteredSubWithMain(t *testing.T) {
	s := withProject(t, testDirs(t), DefaultEnvelope("deck"))
	m, _ := mainAndSub(t, s)
	if _, _, err := s.EnqueueReport(Report{Sub: m.Key, CompletionID: "x", Source: ReportActive}); err == nil {
		t.Error("主 agent 不能给自己发汇报")
	}
	orphan, _ := s.Reserve(sub(Economy), "")
	if _, _, err := s.EnqueueReport(Report{Sub: orphan.Key, CompletionID: "x", Source: ReportActive}); err == nil {
		t.Error("没有主 agent 的子 agent 不能发汇报")
	}
}

// 面板注册表：默认值写出、开关落盘、未知面板报错。
func TestPanelsRegistry(t *testing.T) {
	d := testDirs(t)
	s := withProject(t, d, DefaultEnvelope("deck"))
	on := map[string]bool{}
	for _, p := range s.Panels() {
		on[p.Name] = p.On
	}
	if !on["decisions"] || !on["quota"] || !on["notifications"] || on["progress"] {
		t.Fatalf("默认挂着决策、额度、通知：%v", on)
	}
	if err := s.SetPanel("progress", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPanel("nope", true); err == nil {
		t.Error("未知面板应报错")
	}
	s2, _ := Open(d)
	for _, p := range s2.Panels() {
		if p.Name == "progress" && !p.On {
			t.Error("开关应落盘")
		}
	}
}

// 完成编号不合规就拒绝；合规的编号比较与保存用同一个值，不会因清洗而重复入队（n050 评审 B8）。
func TestCompletionIDIsValidatedNotCleaned(t *testing.T) {
	s := withProject(t, testDirs(t), DefaultEnvelope("deck"))
	_, sb := mainAndSub(t, s)
	for _, bad := range []string{strings.Repeat("a", 65), "a\x1b[31m", "a b"} {
		if _, _, err := s.EnqueueReport(Report{Sub: sb.Key, CompletionID: bad, Source: ReportActive}); err == nil {
			t.Errorf("编号 %q 应被拒", bad)
		}
	}
	// 推断提醒必须自带编号；真正的完成不给编号时由存储在锁里按当前代次给（n051 attempt 3）
	if _, _, err := s.EnqueueReport(Report{Sub: sb.Key, Source: ReportInferred}); err == nil {
		t.Error("提醒没有编号应被拒")
	}
	if r, _, err := s.EnqueueReport(Report{Sub: sb.Key, Source: ReportActive, Text: "做完了"}); err != nil || r.CompletionID != CompletionIDFor(1) {
		t.Errorf("真正的完成按当前代次编号：%+v %v", r, err)
	}
	id := strings.Repeat("a", 64)
	s.EnqueueReport(Report{Sub: sb.Key, CompletionID: id, Source: ReportActive})
	if _, added, _ := s.EnqueueReport(Report{Sub: sb.Key, CompletionID: id, Source: ReportActive}); added {
		t.Fatal("同一编号不该再入队")
	}
}

// 发送中（sending）的汇报在重启后标为送达未确认，不会再被当成待送（建议项：不重放）。
func TestSendingReportBecomesUnconfirmedAfterRestart(t *testing.T) {
	d := testDirs(t)
	s := withProject(t, d, DefaultEnvelope("deck"))
	m, sb := mainAndSub(t, s)
	r, _, _ := s.EnqueueReport(Report{Sub: sb.Key, CompletionID: "c1", Source: ReportActive})
	s.MarkReport(r.ID, ReportSending)
	s2, _ := Open(d)
	if len(s2.PendingReports(m.Key)) != 0 {
		t.Fatal("发送中的不该回到待送")
	}
	if err := s2.MarkReport(r.ID, ReportDelivered); err == nil {
		t.Fatal("重启后应已是 unconfirmed，不能再改")
	}
	// n051 评审 1 B5：转为未确认要通知 owner，不悄悄留着
	found := false
	for _, n := range s2.Notifications(0, sb.Project) {
		found = found || strings.Contains(n.Text, "重启时有一条汇报正在转告")
	}
	if !found {
		t.Fatal("重启转为未确认应通知 owner")
	}
}

// n051 attempt 3 自查：提醒送旧主 agent 途中旧主 agent 停了、新主 agent 接管；提醒退回时不把新主 agent 的完成并到旧主 agent 名下，
// 过时的提醒删掉，完成留给新主 agent。
func TestAbsorbOnlyWithinSameMain(t *testing.T) {
	s := withProject(t, testDirs(t), DefaultEnvelope("deck"))
	m1, sb := mainAndSub(t, s)
	h, _, _ := s.EnqueueReport(Report{Sub: sb.Key, CompletionID: HintID(1, time.Now()), Source: ReportInferred, Text: "推断"})
	if err := s.MarkReport(h.ID, ReportSending); err != nil {
		t.Fatal(err)
	}
	s.SetSessionStatus(m1.Key, StatusStopped, "退出")
	m2, err := s.Reserve(launch(Session{Project: "deck", Role: "main", Agent: "claude", Tool: "claude", Model: "m", Tier: Frontier, Title: "主2", CreatedBy: "owner"}), "")
	if err != nil {
		t.Fatal(err)
	}
	if subs, _, err := s.AdoptOrphans("deck", m2.Key); err != nil || subs != 1 {
		t.Fatalf("接管：%d %v", subs, err)
	}
	if _, added, err := s.EnqueueReport(Report{Sub: sb.Key, Source: ReportActive, Text: "做完了"}); !added || err != nil {
		t.Fatalf("完成入队：%v %v", added, err)
	}
	if err := s.MarkReport(h.ID, ReportPending); err != nil {
		t.Fatal(err)
	}
	got := s.PendingReports(m2.Key)
	if len(got) != 1 || got[0].Source != ReportActive {
		t.Fatalf("完成留在新主 agent：%+v", got)
	}
	if left := s.PendingReports(m1.Key); len(left) != 0 {
		t.Fatalf("旧主 agent 名下过时的提醒删掉：%+v", left)
	}
}

// 推断提醒不占完成：有自己的编号；同时只留一条等送的；真正的完成（含账本提交）到时把它换掉。账本提交另按 Ref 去重。
func TestReportsHintAndGraphRef(t *testing.T) {
	d := testDirs(t)
	s := withProject(t, d, DefaultEnvelope("deck"))
	m, sb := mainAndSub(t, s)
	h1, added, _ := s.EnqueueReport(Report{Sub: sb.Key, CompletionID: "hint:1:100", Source: ReportInferred, Text: "推断一"})
	if !added {
		t.Fatal("提醒应收下")
	}
	if _, added, _ := s.EnqueueReport(Report{Sub: sb.Key, CompletionID: "hint:1:200", Source: ReportInferred, Text: "推断二"}); added {
		t.Fatal("同时只留一条等送的提醒")
	}
	if s.HasCompletion(sb.Key, CompletionIDFor(1)) {
		t.Fatal("提醒不占完成")
	}
	g, added, _ := s.EnqueueReport(Report{Sub: sb.Key, CompletionID: CompletionIDFor(1), Source: ReportGraph, Ref: "n1#3", Text: "账本"})
	if !added || g.ID != h1.ID || g.Source != ReportGraph || g.Ref != "n1#3" {
		t.Fatalf("账本提交换掉还没送的提醒：%+v %v", g, added)
	}
	if got := s.PendingReports(m.Key); len(got) != 1 || !s.HasCompletion(sb.Key, CompletionIDFor(1)) {
		t.Fatalf("只一条：%+v", got)
	}
	if _, added, _ := s.EnqueueReport(Report{Sub: sb.Key, CompletionID: CompletionIDFor(2), Source: ReportGraph, Ref: "n1#3", Text: "账本"}); added {
		t.Fatal("同一轮提交（Ref 相同）换了代次也不重收")
	}
	// 这一代已有真正的完成：提醒在同一把锁里被拒（检查与入队之间不留空档）
	if _, added, _ := s.EnqueueReport(Report{Sub: sb.Key, CompletionID: HintID(1, time.Now()), Source: ReportInferred, Text: "迟到的推断"}); added {
		t.Fatal("这一代已有完成：不收提醒")
	}
	if _, added, _ := s.EnqueueReport(Report{Sub: sb.Key, CompletionID: HintID(2, time.Now()), Source: ReportInferred, Text: "新一代的推断"}); !added {
		t.Fatal("新一代还没完成：收提醒")
	}
}
