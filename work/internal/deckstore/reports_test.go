package deckstore

import (
	"testing"
)

func mainAndSub(t *testing.T, s *Store) (Session, Session) {
	t.Helper()
	m, err := s.Reserve(Session{Project: "deck", Role: "main", Agent: "claude", Tool: "claude", Model: "m", Tier: Frontier, Title: "主", CreatedBy: "owner"}, "")
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
