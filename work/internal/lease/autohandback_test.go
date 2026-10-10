package lease

import (
	"path/filepath"
	"testing"
	"time"
)

// 自动交还：owner 持有时立即回到 workbench，事件留档；不是 owner 持有就拒绝。
func TestAutoHandback(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "leases.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.AutoHandback("x", Handback{}); err == nil {
		t.Fatal("没有接管记录时不能自动交还")
	}
	if err := s.OwnerTakeover("x", "deck"); err != nil {
		t.Fatal(err)
	}
	if err := s.AutoHandback("x", Handback{DurationSeconds: 42, Notes: []string{"画面有变化"}}); err != nil {
		t.Fatal(err)
	}
	l, _ := s.Get("x")
	if l.Holder != HolderWorkbench || l.PendingHandback != nil {
		t.Fatalf("应立即回到 workbench：%+v", l)
	}
	ev, _ := s.Events()
	last := ev[len(ev)-1]
	if last.Kind != EventAutoHandback || last.From != HolderOwner || last.To != HolderWorkbench {
		t.Fatalf("应留 auto_handback 事件：%+v", last)
	}
}

// 完整摘要随事件落盘：重新打开后仍能读到进入、离开时间与画面末行（n050 评审 R2）。
func TestAutoHandbackPersistsTheFullSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leases.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.OwnerTakeover("x", "deck")
	at := time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC)
	hb := Handback{AttachedAt: at, ReturnedAt: at.Add(42 * time.Second), DurationSeconds: 42, Via: "deck", LastLines: []string{"a", "b"}, Notes: []string{"离开方式：隐藏"}}
	if err := s.AutoHandback("x", hb); err != nil {
		t.Fatal(err)
	}
	hb.LastLines[0] = "调用方之后改了也不影响记账"
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	ev, _ := s2.Events()
	got := ev[len(ev)-1].Handback
	if got == nil || !got.AttachedAt.Equal(at) || !got.ReturnedAt.Equal(at.Add(42*time.Second)) || len(got.LastLines) != 2 || got.LastLines[0] != "a" {
		t.Fatalf("摘要应完整落盘：%+v", got)
	}
}

// OwnerHeld 只列 owner 持有、没有待确认摘要的会话。
func TestOwnerHeld(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "leases.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Grant("w", "派出")
	s.OwnerTakeover("b", "deck")
	s.OwnerTakeover("a", "deck")
	s.ExternalTakeover("e", 1)
	got := s.OwnerHeld()
	if len(got) != 2 || got[0].SessionID != "a" || got[1].SessionID != "b" {
		t.Fatalf("OwnerHeld = %+v", got)
	}
}
