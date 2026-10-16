package viewstate

import (
	"errors"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/lease"
)

// n049 评审 2 B1：操作绑定到开始时的 client。做到一半 client 被替换（新 client 与旧的共用 tty），
// 之后的每一步都不作用到新 client 上，操作整体中止、状态不变。

// replaceOn 在第 n 次 SetReadOnly 之前把中间换成新 client（新 client 起点：只读、deck-idle）。
type replaceOn struct {
	*fakeCenter
	n, calls int
}

func (c *replaceOn) SetReadOnly(ro bool) error {
	c.calls++
	if c.calls == c.n {
		id, _ := c.fakeCenter.ClientID()
		c.client, c.shown, c.ro = id+1, Idle, true
	}
	return c.fakeCenter.SetReadOnly(ro)
}

func TestOpsAbortWhenTheirClientIsReplaced(t *testing.T) {
	cases := []struct {
		name string
		op   func(m *Machine) error
	}{
		{"open", func(m *Machine) error { return m.Open("main") }},
		{"focus", func(m *Machine) error { return m.Focus("subA") }},
		{"hide", func(m *Machine) error { return m.Hide("") }},
		{"resync", func(m *Machine) error { return m.Resync(testClient) }},
		{"reset", func(m *Machine) error { return m.CenterReset(testClient) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.m.Open("subA")
			w.m.Open("subB")
			before := w.m.State()
			w.m.center = &replaceOn{fakeCenter: w.c, n: 1}
			err := tc.op(w.m)
			if !errors.Is(err, ErrStaleClient) {
				t.Fatalf("client 被替换应中止：%v", err)
			}
			st := w.m.State()
			if st.Focus != before.Focus || len(st.Open) != len(before.Open) || st.ClientGen != before.ClientGen {
				t.Fatalf("中止的操作不应改状态：%+v → %+v", before, st)
			}
			if w.c.shown != Idle || !w.c.ro {
				t.Fatalf("新 client 应保持起点（只读、deck-idle）：shown=%s ro=%v log=%v", w.c.shown, w.c.ro, w.c.log)
			}
			if w.c.bound != 0 {
				t.Fatal("操作结束应解除绑定")
			}
		})
	}
}

// 主 agent 打开到一半（已切过去、放开输入之前）client 被替换：不把新 client 放开成可写。
func TestOpenDoesNotUnlockAReplacementClient(t *testing.T) {
	w := newWorld(t)
	w.m.center = &replaceOn{fakeCenter: w.c, n: 2} // 第 1 次 SetReadOnly 是关输入，第 2 次是放开输入
	if err := w.m.Open("main"); !errors.Is(err, ErrStaleClient) {
		t.Fatalf("放开输入前 client 被替换应报错：%v", err)
	}
	if !w.c.ro {
		t.Fatal("新 client 不应被放开成可写")
	}
}

// 接管等写入权时放过锁：醒来发现 client 换了，就放弃，不登记接管。
func TestTakeoverGivesUpWhenClientReplacedWhileWaiting(t *testing.T) {
	w := newWorld(t)
	if err := w.m.Open("subA"); err != nil {
		t.Fatal(err)
	}
	p, err := w.l.Automatic("subA")
	if err != nil {
		t.Fatal(err)
	}
	release, err := p.Begin("subA") // 一条投递正在进行：接管要等
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	w.m.SetClock(time.Now, func(time.Duration) { w.c.client = testClient + 1 }) // 等的时候中间换了 client
	if err := w.m.Takeover("subA"); !errors.Is(err, ErrTakeoverStale) {
		t.Fatalf("应放弃接管：%v", err)
	}
	if got, _ := w.l.Get("subA"); got.Holder == lease.HolderOwner {
		t.Fatalf("不应登记接管：%+v", got)
	}
	if !w.c.ro {
		t.Fatal("不应放开输入")
	}
}

// Fence 等正在进行的操作做完才返回。
type blockingSwitch struct {
	*fakeCenter
	entered, release chan struct{}
}

func (c *blockingSwitch) Switch(s string) error {
	close(c.entered)
	<-c.release
	return c.fakeCenter.Switch(s)
}

func TestFenceWaitsForInFlightOp(t *testing.T) {
	w := newWorld(t)
	c := &blockingSwitch{fakeCenter: w.c, entered: make(chan struct{}), release: make(chan struct{})}
	w.m.center = c
	go w.m.Open("subA")
	<-c.entered
	fenced := make(chan struct{})
	go func() { w.m.Fence(); close(fenced) }()
	select {
	case <-fenced:
		t.Fatal("操作还在进行，Fence 不应返回")
	case <-time.After(100 * time.Millisecond):
	}
	close(c.release)
	select {
	case <-fenced:
	case <-time.After(2 * time.Second):
		t.Fatal("操作做完后 Fence 应返回")
	}
}
