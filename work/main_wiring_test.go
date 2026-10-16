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
	// 写入者锁落在临时目录，不碰本机真实的 ~/.local/state（n050）
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
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

// n044 attempt 2 评审 blocker 1：同一个配置目录下，写绑定的进程至多一个。第二个进程打开写入权记账会失败（排他文件锁），
// 用它装配出的派工器派工被拒在起会话之前，绑定存储一条都不写——所以不会有两个进程拿旧快照互相覆盖绑定。
func TestASecondWorkDeckOnTheSameConfigCannotWriteBindings(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	t.Setenv("WORKDECK_CONFIG", cfg)
	// 写入者锁落在临时目录，不碰本机真实的 ~/.local/state（n050）
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	called := filepath.Join(dir, "upstream-called")
	bin := filepath.Join(dir, "agent-deck")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ntouch \""+called+"\"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(`{"upstream_binary":"`+bin+`","profile":"_n044wire",
		"models":[{"id":"m1","tool":"claude","route_class":"frontier"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := openLeases()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(first.Close)
	second, err := openLeases()
	if err == nil {
		second.Close()
		t.Fatal("同一配置目录的第二个进程不该拿到写入权记账")
	}
	bs, bsErr := openBindings()
	d, err := buildDispatcher(dir, second, bs, bsErr, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Dispatch(t.Context(), dispatch.Request{NodeID: "n1", Attempt: 1, RequiredClass: dispatch.Frontier, ModelID: "m1",
		ProjectPath: dir, Message: "继续", DispatchedBy: "work-deck", ChosenBy: "owner", ChoiceReason: "测试"})
	if err == nil {
		t.Fatal("没有写入权记账的进程不该派得出工")
	}
	if len(bs.List()) != 0 {
		t.Errorf("没有写入权记账的进程不该写绑定：%+v", bs.List())
	}
	if _, err := os.Stat(called); !os.IsNotExist(err) {
		t.Error("没有写入权记账的进程不该调用上游")
	}
}
