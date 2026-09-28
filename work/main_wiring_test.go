package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/binding"
	"github.com/Tingschen287/terminal-deck/work/internal/dispatch"
	"github.com/Tingschen287/terminal-deck/work/internal/lease"
)

// n040 终审断点 1：派工器写下的绑定，进入与预览在同一进程里要立刻看得见，不用重启。
// 用 main 自己的装配函数搭一遍：派工器写一条绑定，从进入组件拿到的 Bindings 读。
func TestDispatchedBindingIsVisibleToAttachWithoutRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	t.Setenv("WORKDECK_CONFIG", cfg)
	bin := filepath.Join(dir, "agent-deck")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(`{"upstream_binary":"`+bin+`","profile":"_n042wire",
		"models":[{"id":"m1","tool":"claude","route_class":"frontier"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	leases, err := lease.Open(filepath.Join(dir, "leases.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(leases.Close)

	bs, bsErr := openBindings()
	d, err := buildDispatcher(dir, leases, bs, bsErr, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, attachBindings, err := buildAttacher(bs, bsErr)
	if err != nil {
		t.Fatal(err)
	}
	// 进入组件先读一次（界面启动时就会读），再由派工器写入：分开 Open 的旧副本此时看不见新绑定。
	if _, ok := attachBindings.Get("n1", 1); ok {
		t.Fatal("前提：还没派工时不该有绑定")
	}
	store := d.(*dispatch.Dispatcher).Store
	if err := store.Put(binding.Binding{NodeID: "n1", Attempt: 1, SessionID: "s-new", Tool: "claude",
		Model: "m1", RouteClass: "frontier", ProjectPath: dir, DispatchedAt: time.Now(), DispatchedBy: "work-deck"}); err != nil {
		t.Fatal(err)
	}
	if b, ok := attachBindings.Get("n1", 1); !ok || b.SessionID != "s-new" {
		t.Errorf("派工器刚写的绑定，进入组件看不见（要重启才行）：%+v %v", b, ok)
	}
}
