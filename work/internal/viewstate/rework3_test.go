package viewstate

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/lease"
)

// n049 评审 3 B1-B：中途发现中间 client 被替换时的「部分完成」语义。
//   - 还没写下任何不可撤销的东西（只动了内存与已不在的旧 client）：整体撤回，状态不变，返回 ErrStaleClient。
//   - 已写下不可撤销的部分（lease 交还已落盘）：按操作目标补齐焦点与打开集合，返回 *ErrPartial（包着 ErrStaleClient）。
//   - 接管：lease 已登记、放开输入时被替换——输入从没放开过，按交还收回，不留一个没生效的接管。

// replaceOnSwitch 在第 n 次 Switch 之前把中间换成新 client。
type replaceOnSwitch struct {
	*fakeCenter
	n, calls int
}

func (c *replaceOnSwitch) Switch(s string) error {
	c.calls++
	if c.calls == c.n {
		id, _ := c.fakeCenter.ClientID()
		c.client, c.shown, c.ro = id+1, Idle, true
	}
	return c.fakeCenter.Switch(s)
}

func TestOpenAbortedBeforeAnyCommitKeepsState(t *testing.T) {
	w := newWorld(t)
	if err := w.m.Open("subA"); err != nil {
		t.Fatal(err)
	}
	before := w.m.State()
	w.m.center = &replaceOn{fakeCenter: w.c, n: 2} // 已切到主 agent、放开输入之前被替换
	err := w.m.Open("main")
	if !errors.Is(err, ErrStaleClient) || isPartial(err) {
		t.Fatalf("没写下任何东西应整体中止：%v", err)
	}
	if after := w.m.State(); after.Focus != before.Focus || !reflect.DeepEqual(after.Open, before.Open) {
		t.Fatalf("整体中止不应改状态：%+v → %+v", before, after)
	}
}

func TestHideAfterHandbackCompletesConsistently(t *testing.T) {
	w := newWorld(t)
	for _, id := range []string{"main", "subA"} {
		if err := w.m.Open(id); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.m.Takeover("subA"); err != nil {
		t.Fatal(err)
	}
	w.m.center = &replaceOn{fakeCenter: w.c, n: 2} // 交还已落盘、切到主 agent 后放开输入时被替换
	err := w.m.Hide("subA")
	if !errors.Is(err, ErrStaleClient) || !isPartial(err) {
		t.Fatalf("交还已落盘应是部分完成：%v", err)
	}
	st := w.m.State()
	if st.Focus != "main" || !reflect.DeepEqual(st.Open, []string{"main"}) || len(st.TakenOver) != 0 {
		t.Fatalf("隐藏应按目标补齐（焦点主 agent、子 agent 移出、不再接管）：%+v", st)
	}
	if l, _ := w.l.Get("subA"); l.Holder != lease.HolderWorkbench {
		t.Fatalf("交还已落盘，不应再改回：%+v", l)
	}
	if !w.c.ro {
		t.Fatal("新 client 不应被放开")
	}
}

func TestSwitchFailureAfterHandbackCompletesConsistently(t *testing.T) {
	w := newWorld(t)
	for _, id := range []string{"main", "subA"} {
		if err := w.m.Open(id); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.m.Takeover("subA"); err != nil {
		t.Fatal(err)
	}
	w.m.center = &replaceOnSwitch{fakeCenter: w.c, n: 1} // 交还已落盘、切换时被替换
	err := w.m.Focus("main")
	if !errors.Is(err, ErrStaleClient) || !isPartial(err) {
		t.Fatalf("交还已落盘应是部分完成：%v", err)
	}
	if st := w.m.State(); st.Focus != "main" || len(st.TakenOver) != 0 {
		t.Fatalf("焦点应按目标提交：%+v", st)
	}
	if w.c.shown != Idle || !w.c.ro {
		t.Fatal("新 client 应保持起点")
	}
}

func TestTakeoverReplacedBeforeUnlockIsHandedBack(t *testing.T) {
	w := newWorld(t)
	if err := w.m.Open("subA"); err != nil {
		t.Fatal(err)
	}
	w.m.center = &replaceOn{fakeCenter: w.c, n: 1} // lease 已登记，放开输入时被替换
	err := w.m.Takeover("subA")
	if !errors.Is(err, ErrStaleClient) {
		t.Fatalf("应报 client 被替换：%v", err)
	}
	if l, _ := w.l.Get("subA"); l.Holder == lease.HolderOwner {
		t.Fatalf("没生效的接管应收回：%+v", l)
	}
	if st := w.m.State(); len(st.TakenOver) != 0 || st.Focus != "subA" {
		t.Fatalf("状态应回到接管之前：%+v", st)
	}
	ev, _ := w.l.Events()
	if hb := ev[len(ev)-1].Handback; hb == nil || len(hb.LastLines) == 0 {
		t.Fatalf("收回走正常交还、带画面摘要：%+v", ev[len(ev)-1])
	}
}

// 不变量：任何操作在任何一步被替换，焦点要么为空，要么在打开集合里；lease 与「接管中」一致。
func TestReplacementAtAnyStepKeepsStateConsistent(t *testing.T) {
	ops := map[string]func(m *Machine) error{
		"open main":  func(m *Machine) error { return m.Open("main") },
		"open subB":  func(m *Machine) error { return m.Open("subB") },
		"focus main": func(m *Machine) error { return m.Focus("main") },
		"hide focus": func(m *Machine) error { return m.Hide("") },
		"hide main":  func(m *Machine) error { return m.Hide("main") },
		"reset":      func(m *Machine) error { return m.CenterReset(testClient) },
		"resync":     func(m *Machine) error { return m.Resync(testClient) },
		"takeover":   func(m *Machine) error { return m.Takeover("") },
	}
	for name, op := range ops {
		for _, taken := range []bool{false, true} {
			for n := 1; n <= 3; n++ {
				for _, onSwitch := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/taken=%v/step=%d/switch=%v", name, taken, n, onSwitch), func(t *testing.T) {
						w := newWorld(t)
						for _, id := range []string{"main", "subA"} {
							if err := w.m.Open(id); err != nil {
								t.Fatal(err)
							}
						}
						if taken {
							if err := w.m.Takeover("subA"); err != nil {
								t.Fatal(err)
							}
						}
						if onSwitch {
							w.m.center = &replaceOnSwitch{fakeCenter: w.c, n: n}
						} else {
							w.m.center = &replaceOn{fakeCenter: w.c, n: n}
						}
						op(w.m)
						st := w.m.State()
						if st.Focus != "" && !contains(st.Open, st.Focus) {
							t.Fatalf("焦点不在打开集合里：%+v", st)
						}
						held := map[string]bool{}
						for _, l := range w.l.OwnerHeld() {
							held[l.SessionID] = true
						}
						for _, s := range st.TakenOver {
							if !held[s] {
								t.Fatalf("记为接管中、lease 却不在 owner 手上：%s %+v", s, st)
							}
						}
						for s := range held {
							if !contains(st.TakenOver, s) {
								t.Fatalf("lease 在 owner 手上、却没记为接管中：%s %+v", s, st)
							}
						}
						if w.c.bound != 0 {
							t.Fatal("操作结束应解除绑定")
						}
						w.noViolations()
					})
				}
			}
		}
	}
}

// 评审 3 建议：操作开始时中间没有 client，中途冒出来的 client 不认（不把操作做到它上面）。
type appearOn struct{ *fakeCenter }

func (c *appearOn) SetReadOnly(ro bool) error {
	if c.noClient {
		c.noClient, c.client, c.shown, c.ro = false, testClient+1, Idle, true // 新 client 恰好连上
	}
	return c.fakeCenter.SetReadOnly(ro)
}

func TestClientAppearingMidOpIsNotUsed(t *testing.T) {
	w := newWorld(t)
	w.c.noClient = true
	w.m.center = &appearOn{w.c}
	err := w.m.Open("main")
	if !errors.Is(err, ErrStaleClient) {
		t.Fatalf("开始时没有 client，中途出现的不应被用：%v", err)
	}
	if st := w.m.State(); st.Focus != "" || len(st.Open) != 0 {
		t.Fatalf("应整体中止：%+v", st)
	}
	if w.c.shown != Idle || !w.c.ro {
		t.Fatal("新 client 应保持起点")
	}
}

// n049 评审 4（n051 承接）：deckd 退出前 Close，等进行中的操作做完，之后的一律拒绝；放锁在这之后。
func TestCloseWaitsForInFlightOpAndRefusesLater(t *testing.T) {
	w := newWorld(t)
	c := &blockingSwitch{fakeCenter: w.c, entered: make(chan struct{}), release: make(chan struct{})}
	w.m.center = c
	opDone := make(chan error, 1)
	go func() { opDone <- w.m.Open("subA") }()
	<-c.entered
	closed := make(chan struct{})
	go func() { w.m.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("操作还在进行，Close 不应返回")
	case <-time.After(100 * time.Millisecond):
	}
	close(c.release)
	if err := <-opDone; err != nil {
		t.Fatalf("进行中的操作应正常做完：%v", err)
	}
	<-closed
	w.m.center = w.c
	for name, op := range map[string]func() error{
		"open":     func() error { return w.m.Open("main") },
		"hide":     func() error { return w.m.Hide("subA") },
		"takeover": func() error { return w.m.Takeover("subA") },
		"resync":   func() error { return w.m.Resync(testClient) },
	} {
		if err := op(); !errors.Is(err, ErrClosed) {
			t.Fatalf("%s：Close 之后应拒绝：%v", name, err)
		}
	}
}

// n049 评审 4（n051 承接）：命令落到新 client 上、又没能确认还原（ErrRestoreUnconfirmed）。

// unconfirmedOn 让第 n 次 SetReadOnly 报「被替换且还原未确认」。
type unconfirmedOn struct {
	*fakeCenter
	n, calls int
}

func (c *unconfirmedOn) SetReadOnly(ro bool) error {
	c.calls++
	if c.calls == c.n {
		return fmt.Errorf("%w：%w（测试注入）", ErrStaleClient, ErrRestoreUnconfirmed)
	}
	return c.fakeCenter.SetReadOnly(ro)
}

// 接管放开输入时：不交还，写入权留在 owner 手上、等待交还，并通知。
func TestTakeoverWithUnconfirmedRestoreKeepsLease(t *testing.T) {
	w := newWorld(t)
	if err := w.m.Open("subA"); err != nil {
		t.Fatal(err)
	}
	w.m.center = &unconfirmedOn{fakeCenter: w.c, n: 1}
	err := w.m.Takeover("subA")
	if !errors.Is(err, ErrRestoreUnconfirmed) || !errors.Is(err, ErrStaleClient) {
		t.Fatalf("应报还原未确认：%v", err)
	}
	if l, _ := w.l.Get("subA"); l.Holder != lease.HolderOwner {
		t.Fatalf("还原未确认时不能交还写入权：%+v", l)
	}
	st := w.m.State()
	if len(st.TakenOver) != 1 || len(st.HandbackPending) != 1 {
		t.Fatalf("应保持接管、等待交还（自动派送暂停）：%+v", st)
	}
	if len(w.notes) == 0 || !strings.Contains(w.notes[len(w.notes)-1], "没能确认") {
		t.Fatalf("应通知原因：%v", w.notes)
	}
}

// 关输入时（焦点是没被接管的子 agent）：把写入权收到 owner 手上、自动派送暂停，并通知；操作不继续。
func TestCloseInputWithUnconfirmedRestoreProtectsFocus(t *testing.T) {
	w := newWorld(t)
	if err := w.m.Open("subA"); err != nil {
		t.Fatal(err)
	}
	w.m.center = &unconfirmedOn{fakeCenter: w.c, n: 1}
	if err := w.m.Open("main"); !errors.Is(err, ErrRestoreUnconfirmed) {
		t.Fatalf("应报还原未确认：%v", err)
	}
	if l, _ := w.l.Get("subA"); l.Holder != lease.HolderOwner {
		t.Fatalf("新 client 可能在子 agent 上开着输入：写入权应收到 owner 手上：%+v", l)
	}
	if st := w.m.State(); st.Focus != "subA" || len(st.HandbackPending) != 1 {
		t.Fatalf("操作不应继续，子 agent 应等待交还：%+v", st)
	}
	if len(w.notes) == 0 || !strings.Contains(w.notes[len(w.notes)-1], "子 A") {
		t.Fatalf("应通知原因：%v", w.notes)
	}
}
