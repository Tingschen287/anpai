package binding

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sample() Binding {
	return Binding{
		NodeID: "n099_demo", Attempt: 1, SessionID: "sess-1",
		Tool: "claude", Model: "claude-opus-5", RouteClass: "frontier",
		ProjectPath: "/tmp/p", Profile: "_n036fixture",
		DispatchedAt: time.Now().UTC().Truncate(time.Second),
		DispatchedBy: "tester",
	}
}

// 这是本节点的硬要求：Work Deck 重启后绑定还在。
func TestBindingSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "bindings.json")

	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	want := sample()
	if err := first.Put(want); err != nil {
		t.Fatal(err)
	}

	// 换一个全新的 Store，等同于进程重启。
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := second.Get(want.NodeID, want.Attempt)
	if !ok {
		t.Fatal("重启后绑定丢了")
	}
	if got.SessionID != want.SessionID || got.Model != want.Model || got.RouteClass != want.RouteClass {
		t.Errorf("重启后绑定内容变了：%+v", got)
	}
	if !got.DispatchedAt.Equal(want.DispatchedAt) {
		t.Errorf("派工时间没存住：%v vs %v", got.DispatchedAt, want.DispatchedAt)
	}
}

// 说不清楚的绑定不许落盘。
func TestIncompleteBindingIsRefused(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "b.json"))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(Binding) Binding{
		"没有节点号":     func(b Binding) Binding { b.NodeID = ""; return b },
		"attempt 非正": func(b Binding) Binding { b.Attempt = 0; return b },
		"没有会话号":     func(b Binding) Binding { b.SessionID = ""; return b },
		"没有模型":      func(b Binding) Binding { b.Model = ""; return b },
		"没有 route":  func(b Binding) Binding { b.RouteClass = ""; return b },
	}
	for name, mutate := range cases {
		if err := s.Put(mutate(sample())); err == nil {
			t.Errorf("%s 的绑定应当被拒绝", name)
		}
	}
}

// 存储坏了要喊出来，不能当成空的——那会让人以为派工记录丢失是正常的。
func TestCorruptStoreIsReportedNotSwallowed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.json")
	if err := os.WriteFile(path, []byte("{这不是 JSON"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("存储损坏时必须报错，不能静默当成空存储")
	}
}

func TestMissingStoreIsEmptyNotAnError(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "never-written.json"))
	if err != nil {
		t.Fatalf("文件不存在不该报错：%v", err)
	}
	if len(s.List()) != 0 {
		t.Error("新存储应当是空的")
	}
}

// 落盘要原子：不留下半个文件，也不留下临时文件。
func TestFlushLeavesNoTempFileBehind(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "b.json"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		b := sample()
		b.Attempt = i
		b.SessionID = "sess-" + string(rune('0'+i))
		if err := s.Put(b); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "b.json" {
			t.Errorf("目录里多出了文件 %q，原子落盘不该留下残骸", e.Name())
		}
	}
}

func TestBySessionFindsTheBinding(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "b.json"))
	b := sample()
	if err := s.Put(b); err != nil {
		t.Fatal(err)
	}
	got, ok := s.BySession("sess-1")
	if !ok || got.NodeID != b.NodeID {
		t.Errorf("按会话号反查失败：%+v %v", got, ok)
	}
	if _, ok := s.BySession("不存在"); ok {
		t.Error("不存在的会话号不该查到东西")
	}
}
