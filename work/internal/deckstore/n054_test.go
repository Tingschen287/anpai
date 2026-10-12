package deckstore

import (
	"testing"
	"time"
)

// n054（n051 评审 4 B1）：等送的推断提醒被账本提醒换掉后，判断下一条推断提醒是否过时看账本提醒到的时刻，不看旧推断提醒入队的时刻。
// 对照：账本提醒到了之后才开始的安静照常提醒；到达时刻落盘、重开后还在。
func TestReplacedReminderUsesArrivalTime(t *testing.T) {
	base := time.Now().Add(-10 * time.Minute)
	setup := func(t *testing.T) (*Store, Dirs, Session, Report) {
		d := testDirs(t)
		s := withProject(t, d, DefaultEnvelope("deck"))
		_, sb := mainAndSub(t, s)
		s.now = func() time.Time { return base }
		h, added, err := s.EnqueueReport(Report{Sub: sb.Key, CompletionID: HintID(1, base.Add(-time.Minute)), Source: ReportInferred, Text: "旧推断"})
		if err != nil || !added {
			t.Fatalf("旧推断提醒入队：%v %v", added, err)
		}
		s.now = func() time.Time { return base.Add(2 * time.Minute) }
		g, added, err := s.EnqueueReport(Report{Sub: sb.Key, Source: ReportGraph, Ref: "n1#1", Text: "账本"})
		if err != nil || !added || g.ID != h.ID || g.Source != ReportGraph {
			t.Fatalf("账本提醒应换掉还没送的推断提醒：%+v %v %v", g, added, err)
		}
		if !g.CreatedAt.Equal(base) || !g.ArrivedAt.Equal(base.Add(2*time.Minute)) {
			t.Fatalf("替换后入队时刻不变、到达时刻改为账本提醒的：created=%v arrived=%v", g.CreatedAt, g.ArrivedAt)
		}
		s.MarkReport(g.ID, ReportSending)
		s.MarkReport(g.ID, ReportDelivered)
		return s, d, sb, g
	}
	t.Run("安静开始在账本提醒之前", func(t *testing.T) {
		s, _, sb, _ := setup(t)
		s.now = func() time.Time { return base.Add(4 * time.Minute) }
		if _, added, _ := s.EnqueueReport(Report{Sub: sb.Key, CompletionID: HintID(1, base.Add(time.Minute)), Source: ReportInferred, Text: "推断"}); added {
			t.Fatal("这段安静里到过账本提醒（替换进旧行），不该再推断提醒")
		}
	})
	t.Run("对照_安静开始在账本提醒之后", func(t *testing.T) {
		s, _, sb, _ := setup(t)
		s.now = func() time.Time { return base.Add(5 * time.Minute) }
		if _, added, _ := s.EnqueueReport(Report{Sub: sb.Key, CompletionID: HintID(1, base.Add(3*time.Minute)), Source: ReportInferred, Text: "推断"}); !added {
			t.Fatal("账本提醒之后才开始的安静照常提醒")
		}
	})
	t.Run("到达时刻落盘", func(t *testing.T) {
		_, d, _, g := setup(t)
		s2, err := Open(d)
		if err != nil {
			t.Fatal(err)
		}
		r, ok := s2.Report(g.ID)
		if !ok || !r.ArrivedAt.Equal(base.Add(2*time.Minute)) {
			t.Fatalf("重开后到达时刻：%+v", r)
		}
	})
}

// 提醒从 sending 退回 pending、并进之后到的主动报告时，到达时刻取那条主动报告的。
func TestAbsorbCarriesArrivalTime(t *testing.T) {
	s := withProject(t, testDirs(t), DefaultEnvelope("deck"))
	m, sb := mainAndSub(t, s)
	base := time.Now().Add(-10 * time.Minute)
	s.now = func() time.Time { return base }
	h, _, _ := s.EnqueueReport(Report{Sub: sb.Key, CompletionID: HintID(1, base.Add(-time.Minute)), Source: ReportInferred, Text: "推断"})
	if err := s.MarkReport(h.ID, ReportSending); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return base.Add(2 * time.Minute) }
	if _, added, err := s.EnqueueReport(Report{Sub: sb.Key, Source: ReportActive, Text: "做完了"}); !added || err != nil {
		t.Fatalf("完成入队：%v %v", added, err)
	}
	s.now = func() time.Time { return base.Add(3 * time.Minute) }
	if err := s.MarkReport(h.ID, ReportPending); err != nil {
		t.Fatal(err)
	}
	got := s.PendingReports(m.Key)
	if len(got) != 1 || got[0].ID != h.ID || got[0].Source != ReportActive || !got[0].ArrivedAt.Equal(base.Add(2*time.Minute)) {
		t.Fatalf("并进的主动报告带上它自己的到达时刻：%+v", got)
	}
}

// 旧版本写的汇报没有 arrived_at：按 created_at 判断过时，不当成「从没到过」。
func TestLegacyReportWithoutArrivalFallsBackToCreated(t *testing.T) {
	s := withProject(t, testDirs(t), DefaultEnvelope("deck"))
	_, sb := mainAndSub(t, s)
	base := time.Now().Add(-10 * time.Minute)
	s.now = func() time.Time { return base.Add(2 * time.Minute) }
	g, _, _ := s.EnqueueReport(Report{Sub: sb.Key, Source: ReportGraph, Ref: "n1#1", Text: "账本"})
	s.MarkReport(g.ID, ReportSending)
	s.MarkReport(g.ID, ReportDelivered)
	s.mu.Lock()
	for _, x := range s.reports {
		if x.ID == g.ID {
			x.ArrivedAt = time.Time{}
		}
	}
	s.mu.Unlock()
	s.now = func() time.Time { return base.Add(5 * time.Minute) }
	if _, added, _ := s.EnqueueReport(Report{Sub: sb.Key, CompletionID: HintID(1, base.Add(time.Minute)), Source: ReportInferred, Text: "推断"}); added {
		t.Fatal("旧记录按 created_at：账本提醒在安静开始之后到，不再推断提醒")
	}
	if _, added, _ := s.EnqueueReport(Report{Sub: sb.Key, CompletionID: HintID(1, base.Add(3*time.Minute)), Source: ReportInferred, Text: "推断"}); !added {
		t.Fatal("对照：账本提醒之后才开始的安静照常提醒")
	}
}
