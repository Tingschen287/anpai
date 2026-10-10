package viewstate

import (
	"errors"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/lease"
)

// n049 评审 1 B2：交还摘要按被交还会话自己的 tmux 名抓，不按中间 client 正显示的（重建后是 deck-idle）。
func TestHandbackCapturesTheReturnedSessionByName(t *testing.T) {
	w := newWorld(t)
	w.m.Open("subA")
	w.m.Takeover("subA")
	w.c.shown = Idle // client 重建：中间已回到占位会话
	if err := w.m.CenterReset(testClient); err != nil {
		t.Fatal(err)
	}
	if len(w.c.captured) != 1 || w.c.captured[0] != "agentdeck_a" {
		t.Fatalf("应按 subA 的 tmux 名抓画面：%v", w.c.captured)
	}
}

// n049 评审 1 B1：旧 client 的 reset、resync 到得晚了（中间已换成新 client），不执行，什么都不改。
func TestStaleClientResetAndResyncAreRefused(t *testing.T) {
	w := newWorld(t)
	w.m.Open("main")
	w.m.Open("subA")
	w.m.Takeover("subA")
	w.c.client = testClient + 1 // 中间已经是新 client
	before := w.m.State()
	shown := w.c.shown
	if err := w.m.CenterReset(testClient); !errors.Is(err, ErrStaleClient) {
		t.Fatalf("旧 client 的 reset 应拒绝：%v", err)
	}
	if err := w.m.Resync(testClient); !errors.Is(err, ErrStaleClient) {
		t.Fatalf("旧 client 的 resync 应拒绝：%v", err)
	}
	if err := w.m.CenterReset(0); !errors.Is(err, ErrStaleClient) {
		t.Fatalf("不说是哪个 client 的 reset 应拒绝：%v", err)
	}
	st := w.m.State()
	if st.Focus != before.Focus || len(st.Open) != len(before.Open) || st.ClientGen != before.ClientGen || w.c.shown != shown {
		t.Fatalf("被拒绝的通知不应改任何东西：%+v → %+v", before, st)
	}
	if l, _ := w.l.Get("subA"); l.Holder != lease.HolderOwner {
		t.Fatalf("被拒绝的 reset 不应交还：%+v", l)
	}
	// 新 client 自己的 reset 照常执行
	if err := w.m.CenterReset(testClient + 1); err != nil {
		t.Fatal(err)
	}
	if st := w.m.State(); st.Focus != "main" || st.ClientGen != before.ClientGen+1 {
		t.Fatalf("新 client 的 reset 应隐藏焦点：%+v", st)
	}
	w.noViolations()
}
