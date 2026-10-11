package viewstate

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

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
