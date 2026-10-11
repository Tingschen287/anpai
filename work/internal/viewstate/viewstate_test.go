package viewstate

import (
	"errors"
	"fmt"
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
	captured   []string // Capture 收到的会话名
	client     int      // ClientID：中间现在的 client（0 表示 testClient）
	bound      int      // Bind 绑定的 client
	noClient   bool     // 中间没有 client（布局没起来、正在重连）
}

func (c *fakeCenter) Bind(client int) { c.bound = client }

// stale：绑定的 client 已被替换（同真实实现，SetReadOnly、ReadOnly、Switch、LastInput 都查）。
func (c *fakeCenter) stale() error {
	id, err := c.ClientID()
	if c.bound == NoClient && err != nil {
		return err // 开始时没有、现在也没有
	}
	if c.bound != 0 && (err != nil || id != c.bound) {
		return fmt.Errorf("%w（假 client %d → %d）", ErrStaleClient, c.bound, id)
	}
	return nil
}

// testClient 是假中间 client 的进程号。
const testClient = 7

func (c *fakeCenter) ClientID() (int, error) {
	if c.noClient {
		return 0, errors.New("中间没有 client")
	}
	if c.client == 0 {
		return testClient, nil
	}
	return c.client, nil
}

func (c *fakeCenter) SetReadOnly(ro bool) error {
	if err := c.stale(); err != nil {
		return err
	}
	if ro && c.failSetRO {
		return errors.New("set ro failed")
	}
	c.ro = ro
	c.log = append(c.log, map[bool]string{true: "ro", false: "rw"}[ro])
	return nil
}
func (c *fakeCenter) ReadOnly() (bool, error) {
	if err := c.stale(); err != nil {
		return false, err
	}
	if c.failReadRO {
		return false, errors.New("read failed")
	}
	return c.ro, nil
}
func (c *fakeCenter) Switch(s string) error {
	if err := c.stale(); err != nil {
		return err
	}
	if !c.ro {
		c.violations = append(c.violations, "切换到 "+s+" 时输入是开着的")
	}
	c.shown = s
	c.log = append(c.log, "switch:"+s)
	return nil
}
func (c *fakeCenter) LastInput() (time.Time, error) {
	if err := c.stale(); err != nil {
		return time.Time{}, err
	}
	return c.lastInput, nil
}
func (c *fakeCenter) Capture(s string, n int) ([]string, error) {
	c.captured = append(c.captured, s)
	return []string{"画面最后一行"}, nil
}

type world struct {
	failNotify bool
	stopErr    error
	t          *testing.T
	c          *fakeCenter
	l          *lease.Store
	m          *Machine
	notes      []string
	now        time.Time
	targets    map[string]Target
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
	w.m = New(w.c, l, func(s string) (Target, bool) {
		if x, ok := w.targets[s]; ok {
			return x, true
		}
		for _, x := range w.targets { // 也认注册表 key（key-<id>）
			if "key-"+x.Session == s {
				return x, true
			}
		}
		return Target{}, false
	}, func(n string) error {
		if w.failNotify {
			return errors.New("通知写不进")
		}
		w.notes = append(w.notes, n)
		return nil
	}, func() error { return w.stopErr })
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
	// lease 提交之前写的通知，不宣称「已交还」（n050 评审 R2）
	if last := w.notes[len(w.notes)-1]; !strings.Contains(last, "42 秒") || !strings.Contains(last, "正在自动交还") {
		t.Fatalf("应发交还通知：%q", last)
	}
	ev, _ := w.l.Events()
	last := ev[len(ev)-1]
	if last.Kind != lease.EventAutoHandback || !strings.Contains(last.Reason, "画面最后一行") {
		t.Fatalf("交还事件应带画面末行：%+v", last)
	}
	if hb := last.Handback; hb == nil || hb.DurationSeconds != 42 || hb.AttachedAt.IsZero() || !hb.ReturnedAt.Equal(w.now) || len(hb.LastLines) != 1 {
		t.Fatalf("完整摘要应随事件落盘：%+v", hb)
	}
	if n := w.notes[len(w.notes)-1]; !strings.Contains(n, "画面最后一行") {
		t.Fatalf("交还通知应带画面末行：%q", n)
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
	if err := w.m.CenterReset(testClient); err != nil {
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

// B3：用注册表 key 打开、用 agent-deck id 送汇报，必须是同一个焦点，走同一道门。
func TestSessionIdentifiersAreUnified(t *testing.T) {
	w := newWorld(t)
	if err := w.m.Open("key-main"); err != nil {
		t.Fatal(err)
	}
	if st := w.m.State(); st.Focus != "main" || st.Open[0] != "main" {
		t.Fatalf("焦点应统一成 agent-deck id：%+v", st)
	}
	w.c.lastInput = w.now.Add(-time.Second)
	if err := w.m.DeliverToMain("main", func() error { return nil }); !errors.Is(err, ErrOwnerTyping) {
		t.Fatalf("用另一种标识送汇报也应被送达门拦下：%v", err)
	}
	w.m.Open("key-subA")
	if err := w.m.Takeover("subA"); err != nil {
		t.Fatal(err)
	}
	if l, _ := w.l.Get("subA"); l.Holder != lease.HolderOwner {
		t.Fatalf("lease 应登记在 agent-deck id 上：%+v", l)
	}
}

// B4：等待中切走再切回，并由新请求接管成功；旧请求醒来后作废，不能动新接管的 lease。
func TestStaleTakeoverDoesNotReleaseANewerTakeover(t *testing.T) {
	w := newWorld(t)
	w.m.Open("subA")
	p, _ := w.l.Automatic("subA")
	release, _ := p.Begin("subA")
	done := false
	w.m.SetClock(func() time.Time { return w.now }, func(d time.Duration) {
		w.now = w.now.Add(d)
		if done {
			return
		}
		done = true
		release()
		w.m.Open("subB")
		w.m.Open("subA")
		if err := w.m.Takeover("subA"); err != nil {
			t.Errorf("新请求应接管成功：%v", err)
		}
	})
	if err := w.m.Takeover("subA"); !errors.Is(err, ErrTakeoverStale) {
		t.Fatalf("旧请求应作废：%v", err)
	}
	if l, _ := w.l.Get("subA"); l.Holder != lease.HolderOwner {
		t.Fatalf("新接管的 lease 不能被旧请求交还：%+v", l)
	}
	if w.c.ro {
		t.Fatal("新接管应保持可写")
	}
	if st := w.m.State(); len(st.TakenOver) != 1 {
		t.Fatalf("应只有新的那次接管：%+v", st)
	}
}

// B4：放开输入失败、又确认不了输入已关闭：保持接管与暂停，不交还。
type flakyCenter struct {
	*fakeCenter
	failOpen bool
	failRO   bool
}

func (c *flakyCenter) SetReadOnly(ro bool) error {
	if !ro && c.failOpen {
		return errors.New("rw failed")
	}
	if ro && c.failRO {
		return errors.New("ro failed")
	}
	return c.fakeCenter.SetReadOnly(ro)
}

func TestOpenFailureWithUnknownInputKeepsLease(t *testing.T) {
	w := newWorld(t)
	fc := &flakyCenter{fakeCenter: w.c}
	w.m.center = fc
	w.m.Open("subA")
	fc.failOpen, fc.failRO = true, true
	if err := w.m.Takeover("subA"); err == nil {
		t.Fatal("放开失败应报错")
	}
	if l, _ := w.l.Get("subA"); l.Holder != lease.HolderOwner {
		t.Fatalf("确认不了输入已关闭时不能交还：%+v", l)
	}
}

// B5：交还通知写不进时不释放 lease，保持接管；修好后可重试交还。
func TestHandbackKeepsLeaseWhenNotificationFailsAndCanRetry(t *testing.T) {
	w := newWorld(t)
	w.m.Open("subA")
	w.m.Takeover("subA")
	w.failNotify = true
	err := w.m.Open("subB")
	var he *ErrHandback
	if !errors.As(err, &he) {
		t.Fatalf("交还失败应报 ErrHandback：%v", err)
	}
	w.noViolations()
	if l, _ := w.l.Get("subA"); l.Holder != lease.HolderOwner {
		t.Fatalf("通知写不进时不能释放 lease：%+v", l)
	}
	if st := w.m.State(); st.Focus != "subB" || len(st.TakenOver) != 1 {
		t.Fatalf("应已切走但 subA 仍在接管中：%+v", st)
	}
	w.failNotify = false
	if err := w.m.RetryHandback("subA"); err != nil {
		t.Fatal(err)
	}
	if l, _ := w.l.Get("subA"); l.Holder != lease.HolderWorkbench {
		t.Fatalf("重试后应交还：%+v", l)
	}
}

// B5：存储停止后，一切会写 lease 或改变输入状态的操作都拒绝。
func TestStoppedStoreRefusesViewChanges(t *testing.T) {
	w := newWorld(t)
	w.m.Open("subA")
	w.stopErr = errors.New("磁盘坏了")
	for name, op := range map[string]func() error{
		"open":     func() error { return w.m.Open("subB") },
		"takeover": func() error { return w.m.Takeover("subA") },
		"hide":     func() error { return w.m.Hide("subA") },
		"deliver":  func() error { return w.m.DeliverToMain("main", func() error { return nil }) },
	} {
		if err := op(); err == nil || !strings.Contains(err.Error(), "存储已停止") {
			t.Errorf("%s 应被拒：%v", name, err)
		}
	}
	if l, _ := w.l.Get("subA"); l.Holder != lease.HolderWorkbench {
		t.Fatalf("lease 不该被改：%+v", l)
	}
}

// 交还摘要带画面最后几行。
func TestHandbackSummaryHasLastLines(t *testing.T) {
	w := newWorld(t)
	w.m.Open("subA")
	w.m.Takeover("subA")
	w.m.Open("subB")
	ev, _ := w.l.Events()
	if ev[len(ev)-1].Kind != lease.EventAutoHandback {
		t.Fatalf("应记 auto_handback：%+v", ev[len(ev)-1])
	}
}
