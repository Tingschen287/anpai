package viewstate

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/lease"
)

// fakeCenter 记录每一步操作，并能模拟失败；它自己检查「切换时输入必须关着」这条不变量。
type fakeCenter struct {
	ro         bool
	shown      string
	log        []string
	failSetRO  bool
	failReadRO bool
	lastInput  time.Time
	violations []string
}

func (c *fakeCenter) SetReadOnly(ro bool) error {
	if ro && c.failSetRO {
		return errors.New("set ro failed")
	}
	c.ro = ro
	c.log = append(c.log, map[bool]string{true: "ro", false: "rw"}[ro])
	return nil
}
func (c *fakeCenter) ReadOnly() (bool, error) {
	if c.failReadRO {
		return false, errors.New("read failed")
	}
	return c.ro, nil
}
func (c *fakeCenter) Switch(s string) error {
	if !c.ro {
		c.violations = append(c.violations, "切换到 "+s+" 时输入是开着的")
	}
	c.shown = s
	c.log = append(c.log, "switch:"+s)
	return nil
}
func (c *fakeCenter) LastInput() (time.Time, error) { return c.lastInput, nil }

type world struct {
	t       *testing.T
	c       *fakeCenter
	l       *lease.Store
	m       *Machine
	notes   []string
	now     time.Time
	targets map[string]Target
}

func newWorld(t *testing.T) *world {
	w := &world{t: t, c: &fakeCenter{ro: true, shown: Idle}, now: time.Date(2026, 10, 10, 15, 0, 0, 0, time.Local)}
	l, err := lease.Open(filepath.Join(t.TempDir(), "leases.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	w.l = l
	w.targets = map[string]Target{
		"main":  {Session: "main", Tmux: "agentdeck_main", Role: "main", BriefConfirmed: true, Title: "主 agent"},
		"subA":  {Session: "subA", Tmux: "agentdeck_a", Role: "sub", Title: "子 A"},
		"subB":  {Session: "subB", Tmux: "agentdeck_b", Role: "sub", Title: "子 B"},
		"fresh": {Session: "fresh", Tmux: "agentdeck_fresh", Role: "main", BriefConfirmed: false, Title: "新主 agent"},
	}
	for _, s := range []string{"subA", "subB"} {
		l.Grant(s, "deck 派出")
	}
	w.m = New(w.c, l, func(s string) (Target, bool) { x, ok := w.targets[s]; return x, ok }, func(n string) { w.notes = append(w.notes, n) })
	w.m.SetClock(func() time.Time { return w.now }, func(d time.Duration) { w.now = w.now.Add(d) })
	return w
}

func (w *world) noViolations() {
	w.t.Helper()
	if len(w.c.violations) > 0 {
		w.t.Fatalf("违反「先关输入再切换」：%v（操作序列 %v）", w.c.violations, w.c.log)
	}
}

// 主 agent 可写 → 切到子 agent：先只读，再切，子 agent 保持只读。
func TestSwitchFromWritableMainToSubClosesInputFirst(t *testing.T) {
	w := newWorld(t)
	if err := w.m.Open("main"); err != nil {
		t.Fatal(err)
	}
	if w.c.ro {
		t.Fatal("brief 已确认的主 agent 应可写")
	}
	if err := w.m.Open("subA"); err != nil {
		t.Fatal(err)
	}
	w.noViolations()
	if !w.c.ro || w.c.shown != "agentdeck_a" {
		t.Fatalf("子 agent 应只读显示：ro=%v shown=%s", w.c.ro, w.c.shown)
	}
	got := strings.Join(w.c.log, ",")
	if !strings.Contains(got, "rw,ro,switch:agentdeck_a") {
		t.Fatalf("顺序应为先 ro 再 switch：%s", got)
	}
}

// brief 还没确认送达的主 agent：保持只读。
func TestUnconfirmedMainStaysReadOnly(t *testing.T) {
	w := newWorld(t)
	w.m.Open("fresh")
	if !w.c.ro {
		t.Fatal("首条 brief 未确认前主 agent 应只读")
	}
}

// 只读设置失败：不切换，焦点不变。
func TestSwitchRefusedWhenInputCannotBeClosed(t *testing.T) {
	w := newWorld(t)
	w.m.Open("main")
	w.c.failSetRO = true
	if err := w.m.Open("subA"); !errors.Is(err, ErrInputNotClosed) {
		t.Fatalf("关不上输入应拒绝切换：%v", err)
	}
	if w.c.shown != "agentdeck_main" || w.m.State().Focus != "main" {
		t.Fatalf("切换被拒时中间应不变：%s %s", w.c.shown, w.m.State().Focus)
	}
	w.c.failSetRO = false
	w.c.failReadRO = true
	if err := w.m.Open("subA"); !errors.Is(err, ErrInputNotClosed) {
		t.Fatalf("读回失败也应拒绝：%v", err)
	}
}

// 接管：拿到写入权后才放开输入；接管期间自动派送被 lease 拒。
func TestTakeoverOpensInputOnlyAfterLease(t *testing.T) {
	w := newWorld(t)
	w.m.Open("subA")
	if err := w.m.Takeover("subA"); err != nil {
		t.Fatal(err)
	}
	if w.c.ro {
		t.Fatal("接管后应可写")
	}
	l, _ := w.l.Get("subA")
	if l.Holder != lease.HolderOwner {
		t.Fatalf("写入权应在 owner：%+v", l)
	}
	p, _ := w.l.Automatic("subA")
	if _, err := p.Begin("subA"); err == nil {
		t.Fatal("接管中自动派送应被拒")
	}
}

// 接管要等在途派送完成；10 秒内没完成就失败并保持只读。
func TestTakeoverWaitsForInflightDeliveryThenGivesUp(t *testing.T) {
	w := newWorld(t)
	w.m.Open("subA")
	p, _ := w.l.Automatic("subA")
	release, err := p.Begin("subA")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := w.m.Takeover("subA"); !errors.Is(err, ErrTakeoverBusy) {
		t.Fatalf("派送一直在途应接管失败：%v", err)
	}
	if !w.c.ro {
		t.Fatal("接管失败应保持只读")
	}
}

// 等待期间切走：旧请求作废，不会把新会话放开，拿到的写入权立即退还。
func TestStaleTakeoverAfterSwitchAwayIsVoided(t *testing.T) {
	w := newWorld(t)
	w.m.Open("subA")
	p, _ := w.l.Automatic("subA")
	release, _ := p.Begin("subA")
	switched := false
	// 等待中（sleep 回调里）owner 切到 subB，派送随后完成
	w.m.SetClock(func() time.Time { return w.now }, func(d time.Duration) {
		w.now = w.now.Add(d)
		if !switched {
			switched = true
			if err := w.m.Open("subB"); err != nil {
				t.Errorf("等待期间切换失败：%v", err)
			}
			release()
		}
	})
	if err := w.m.Takeover("subA"); !errors.Is(err, ErrTakeoverStale) {
		t.Fatalf("切走后接管应作废：%v", err)
	}
	w.noViolations()
	if !w.c.ro || w.c.shown != "agentdeck_b" {
		t.Fatalf("subB 不应被放开：ro=%v shown=%s", w.c.ro, w.c.shown)
	}
	if l, _ := w.l.Get("subA"); l.Holder != lease.HolderWorkbench {
		t.Fatalf("作废的接管应立即交还：%+v", l)
	}
}

// 离开被接管的会话：先只读，再自动交还，记摘要。
func TestLeavingTakenOverSessionAutoHandsBackAfterClosingInput(t *testing.T) {
	w := newWorld(t)
	w.m.Open("subA")
	w.m.Takeover("subA")
	w.now = w.now.Add(42 * time.Second)
	if err := w.m.Open("subB"); err != nil {
		t.Fatal(err)
	}
	w.noViolations()
	if l, _ := w.l.Get("subA"); l.Holder != lease.HolderWorkbench {
		t.Fatalf("离开后应自动交还：%+v", l)
	}
	if last := w.notes[len(w.notes)-1]; !strings.Contains(last, "42 秒") || !strings.Contains(last, "已自动交还") {
		t.Fatalf("应发交还通知：%q", last)
	}
	ev, _ := w.l.Events()
	if ev[len(ev)-1].Kind != lease.EventAutoHandback {
		t.Fatalf("应记 auto_handback：%+v", ev[len(ev)-1])
	}
}

// 隐藏规则：隐藏焦点 → 上一个打开的会话；全隐藏 → Idle；隐藏非焦点不动中间。
func TestHideRule(t *testing.T) {
	w := newWorld(t)
	w.m.Open("main")
	w.m.Open("subA")
	w.m.Open("subB")
	w.m.Hide("subA") // 非焦点
	if w.c.shown != "agentdeck_b" {
		t.Fatalf("隐藏非焦点不应动中间：%s", w.c.shown)
	}
	w.m.Hide("subB")
	if w.c.shown != "agentdeck_main" || w.m.State().Focus != "main" {
		t.Fatalf("隐藏焦点应回到上一个打开的：%s", w.c.shown)
	}
	w.m.Hide("main")
	if w.c.shown != Idle || w.m.State().Focus != "" {
		t.Fatalf("全部隐藏应显示 Idle：%s", w.c.shown)
	}
	w.noViolations()
}

// 中间 client 重建（Ctrl-Q）：之前的接管作废并交还，焦点按隐藏处理。
func TestCenterResetHandsBackAndHidesFocus(t *testing.T) {
	w := newWorld(t)
	w.m.Open("subA")
	w.m.Takeover("subA")
	if err := w.m.CenterReset(); err != nil {
		t.Fatal(err)
	}
	if l, _ := w.l.Get("subA"); l.Holder != lease.HolderWorkbench {
		t.Fatalf("重建后应交还：%+v", l)
	}
	if st := w.m.State(); st.Focus != "" || len(st.Open) != 0 || st.ClientGen != 1 {
		t.Fatalf("焦点应按隐藏处理：%+v", st)
	}
}

// 只有子 agent 需要接管；只能接管当前显示的。
func TestTakeoverPreconditions(t *testing.T) {
	w := newWorld(t)
	w.m.Open("main")
	if err := w.m.Takeover("main"); !errors.Is(err, ErrNotSub) {
		t.Errorf("主 agent 不需要接管：%v", err)
	}
	if err := w.m.Takeover("subA"); !errors.Is(err, ErrNotFocused) {
		t.Errorf("不能接管没显示的：%v", err)
	}
}

// 汇报送达门：owner 30 秒内打过字就排队；否则送前关输入、送完放开；主 agent 不在中间就直接送。
func TestDeliverToMainGate(t *testing.T) {
	w := newWorld(t)
	w.m.Open("main")
	sent := 0
	deliver := func() error {
		if !w.c.ro && w.m.State().Focus == "main" {
			t.Error("送汇报时中间输入应是关着的")
		}
		sent++
		return nil
	}
	w.c.lastInput = w.now.Add(-5 * time.Second)
	if err := w.m.DeliverToMain("main", deliver); !errors.Is(err, ErrOwnerTyping) || sent != 0 {
		t.Fatalf("owner 刚打过字应排队：%v sent=%d", err, sent)
	}
	w.c.lastInput = w.now.Add(-time.Minute)
	if err := w.m.DeliverToMain("main", deliver); err != nil || sent != 1 {
		t.Fatalf("owner 空闲时应送达：%v sent=%d", err, sent)
	}
	if w.c.ro {
		t.Fatal("送完应放开主 agent 输入")
	}
	w.m.Open("subA")
	if err := w.m.DeliverToMain("main", deliver); err != nil || sent != 2 {
		t.Fatalf("主 agent 不在中间时应直接送：%v", err)
	}
	if err := w.m.DeliverToMain("subA", deliver); err == nil {
		t.Fatal("不能把汇报送进子 agent")
	}
}
