package lease

import (
	"path/filepath"
	"testing"
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
