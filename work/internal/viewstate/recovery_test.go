package viewstate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/lease"
)

// 第 3 次实现（n050 评审 R2、R3、R5）：摘要取不到不交还、存储中途停止不放开输入、等待交还可恢复。

type captureFails struct{ *fakeCenter }

func (c captureFails) Capture(int) ([]string, error) { return nil, errors.New("capture failed") }

func pending(st State, s string) bool { return contains(st.HandbackPending, s) }

// 评审复现：取不到画面摘要就不交还；之后能取到了，重试交还成功。
func TestCaptureFailureKeepsOwnerLeaseUntilRetry(t *testing.T) {
	w := newWorld(t)
	w.m.Open("subA")
	w.m.Takeover("subA")
	w.m.center = captureFails{w.c}
	err := w.m.Hide("subA")
	if !isHandback(err) {
		t.Fatalf("取不到摘要应返回交还失败：%v", err)
	}
	if l, _ := w.l.Get("subA"); l.Holder != lease.HolderOwner {
		t.Fatalf("取不到摘要时不能交还：%+v", l)
	}
	if !w.c.ro || !pending(w.m.State(), "subA") {
		t.Fatalf("应输入关闭、等待交还：ro=%v %+v", w.c.ro, w.m.State())
	}
	// 它已不在中间、又没取到过摘要：重试也不能不带画面交还
	w.m.center = w.c
	if err := w.m.RetryHandback("subA"); !isHandback(err) {
		t.Fatalf("不在中间且没有摘要，重试应失败：%v", err)
	}
	// 显示在中间后重试：先关输入、取到摘要、交还
	if err := w.m.Open("subA"); err != nil {
		t.Fatal(err)
	}
	if err := w.m.RetryHandback("subA"); err != nil {
		t.Fatal(err)
	}
	if l, _ := w.l.Get("subA"); l.Holder != lease.HolderWorkbench {
		t.Fatalf("重试后应交还：%+v", l)
	}
	w.noViolations()
}

// 交还失败后它离开了中间：用第一次取到的摘要重试。
func TestRetryUsesTheFirstCapturedSummary(t *testing.T) {
	w := newWorld(t)
	w.m.Open("subA")
	w.m.Takeover("subA")
	w.failNotify = true
	if err := w.m.Open("subB"); !isHandback(err) {
		t.Fatalf("通知写不进应交还失败：%v", err)
	}
	w.failNotify = false
	if err := w.m.RetryHandback("subA"); err != nil {
		t.Fatal(err)
	}
	ev, _ := w.l.Events()
	hb := ev[len(ev)-1].Handback
	if hb == nil || len(hb.LastLines) != 1 || !strings.Contains(strings.Join(hb.Notes, "；"), "上一次交还尝试") {
		t.Fatalf("应带第一次取到的摘要：%+v", hb)
	}
}

// 评审复现：deckd 重启后，lease 里 owner 持有的会话恢复为等待交还，可以重试交还。
func TestRestartRestoresTakenSessionsAndRetryHandsBack(t *testing.T) {
	w := newWorld(t)
	w.m.Open("subA")
	w.m.Takeover("subA")
	w.m = New(w.c, w.l, w.m.lookup, func(string) error { return nil }, nil)
	if st := w.m.State(); !pending(st, "subA") {
		t.Fatalf("重启后应恢复为等待交还：%+v", st)
	}
	if err := w.m.RetryHandback("subA"); err != nil {
		t.Fatal(err)
	}
	l, _ := w.l.Get("subA")
	ev, _ := w.l.Events()
	if l.Holder != lease.HolderWorkbench || !strings.Contains(strings.Join(ev[len(ev)-1].Handback.Notes, "；"), "重启后恢复") {
		t.Fatalf("重启后重试应交还并注明：%+v %+v", l, ev[len(ev)-1])
	}
}

// 评审复现：交还时存储停了，不再切换、不放开主 agent 输入。
func TestStoppedDuringHandbackDoesNotOpenMainInput(t *testing.T) {
	w := newWorld(t)
	w.m.Open("subA")
	w.m.Takeover("subA")
	w.m.notify = func(string) error { w.stopErr = errors.New("持久化失败"); return w.stopErr }
	err := w.m.Open("main")
	if err == nil || isHandback(err) {
		t.Fatalf("存储停了应直接拒绝：%v", err)
	}
	st := w.m.State()
	if !w.c.ro || st.Focus != "subA" || contains(st.Open, "main") {
		t.Fatalf("不应切换、不应放开输入：ro=%v %+v", w.c.ro, st)
	}
	if l, _ := w.l.Get("subA"); l.Holder != lease.HolderOwner {
		t.Fatalf("交还没完成，lease 应仍在 owner：%+v", l)
	}
}

// 评审复现：写入权记账落盘失败后，视图一律拒绝写，Stopped 报出原因。
func TestLeaseStoreFailureStopsViewWrites(t *testing.T) {
	w := newWorld(t)
	path := filepath.Join(t.TempDir(), "leases.json")
	l, err := lease.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	l.Grant("subA", "deck")
	w.l, w.m.leases = l, l
	w.m.Open("subA")
	w.m.Takeover("subA")
	os.Remove(path)
	os.Mkdir(path, 0o700)
	if err := w.m.Open("subB"); err == nil || isHandback(err) {
		t.Fatalf("交还落盘失败把记账弄停了，不应再切换：%v", err)
	}
	if l.Broken() == nil || w.m.Stopped() == nil {
		t.Fatal("记账应已停止，并由 Stopped 报出")
	}
	if err := w.m.Open("main"); err == nil || !w.c.ro {
		t.Fatalf("记账停止后不能改变中间窗格：err=%v ro=%v", err, w.c.ro)
	}
	w.noViolations()
}

// 评审复现：交还失败后再接管，要真的放开输入。
func TestRetakeAfterFailedHandbackOpensInput(t *testing.T) {
	w := newWorld(t)
	w.m.Open("subA")
	w.m.Takeover("subA")
	w.failNotify = true
	w.m.Hide("subA")
	w.failNotify = false
	w.m.Open("subA")
	if err := w.m.Takeover("subA"); err != nil {
		t.Fatal(err)
	}
	if w.c.ro || pending(w.m.State(), "subA") {
		t.Fatalf("重新接管应放开输入：ro=%v %+v", w.c.ro, w.m.State())
	}
	if !strings.Contains(w.notes[len(w.notes)-1], "重新接管") {
		t.Fatalf("应有重新接管的通知：%q", w.notes[len(w.notes)-1])
	}
	w.noViolations()
}

// 接管通知写不进、交还也写不进：照实说保持接管，不说「已撤回」。
func TestTakeoverNotifyFailureReportsTheRealOutcome(t *testing.T) {
	w := newWorld(t)
	w.m.Open("subA")
	w.failNotify = true
	err := w.m.Takeover("subA")
	if err == nil || strings.Contains(err.Error(), "已撤回") {
		t.Fatalf("交还也失败时不能说已撤回：%v", err)
	}
	if l, _ := w.l.Get("subA"); l.Holder != lease.HolderOwner || !w.c.ro || !pending(w.m.State(), "subA") {
		t.Fatalf("应输入关闭、保持接管等待交还：%+v ro=%v %+v", l, w.c.ro, w.m.State())
	}
}

// 送汇报过程中存储停了：送完不放开主 agent 输入。
func TestDeliverDoesNotReopenInputWhenStoppedMidway(t *testing.T) {
	w := newWorld(t)
	w.m.Open("main")
	w.c.lastInput = w.now.Add(-time.Hour)
	err := w.m.DeliverToMain("main", func() error { w.stopErr = errors.New("写坏了"); return nil })
	if err == nil || !w.c.ro {
		t.Fatalf("存储中途停止应保持只读：err=%v ro=%v", err, w.c.ro)
	}
}

// 第 4 次实现（n050 评审 A3-2）：重新接管后，旧摘要与重启豁免都失效，下一次交还必须重新取到画面。

type scriptedCapture struct {
	*fakeCenter
	line string
	fail bool
}

func (c *scriptedCapture) Capture(int) ([]string, error) {
	if c.fail {
		return nil, errors.New("capture failed")
	}
	return []string{c.line}, nil
}

// 评审复现：交还失败时缓存了「重新接管前」的画面；重新接管后 owner 又输入了，下一次抓屏失败，不能拿旧画面交还。
func TestRetakeInvalidatesTheCachedSummary(t *testing.T) {
	w := newWorld(t)
	c := &scriptedCapture{fakeCenter: w.c, line: "重新接管前"}
	w.m.center = c
	w.m.Open("subA")
	w.m.Takeover("subA")
	w.failNotify = true
	w.m.Hide("subA") // 抓到「重新接管前」，通知失败，保持接管并缓存
	w.failNotify = false
	w.m.Open("subA")
	if err := w.m.Takeover("subA"); err != nil {
		t.Fatal(err)
	}
	c.line, c.fail = "owner 新输入之后", true
	if err := w.m.Open("main"); !isHandback(err) {
		t.Fatalf("抓屏失败应交还失败：%v", err)
	}
	if err := w.m.RetryHandback("subA"); err == nil {
		ev, _ := w.l.Events()
		t.Fatalf("重新接管后仍用旧摘要交还了：%+v", ev[len(ev)-1].Handback)
	}
	if l, _ := w.l.Get("subA"); l.Holder != lease.HolderOwner {
		t.Fatalf("应保持接管：%+v", l)
	}
	// 再显示到中间、抓屏恢复，用新画面交还
	c.fail = false
	w.m.Open("subA")
	if err := w.m.RetryHandback("subA"); err != nil {
		t.Fatal(err)
	}
	ev, _ := w.l.Events()
	if hb := ev[len(ev)-1].Handback; hb == nil || len(hb.LastLines) != 1 || hb.LastLines[0] != "owner 新输入之后" {
		t.Fatalf("应带新画面：%+v", hb)
	}
}

// 评审复现：重启恢复的会话被重新接管后，不再享有「无画面也可交还」的豁免。
func TestRetakeEndsTheRestoredWaiver(t *testing.T) {
	w := newWorld(t)
	w.m.Open("subA")
	w.m.Takeover("subA")
	w.m = New(w.c, w.l, w.m.lookup, func(string) error { return nil }, nil)
	w.m.Open("subA")
	if err := w.m.Takeover("subA"); err != nil {
		t.Fatal(err)
	}
	w.m.center = captureFails{w.c}
	if err := w.m.Open("main"); !isHandback(err) {
		t.Fatalf("抓屏失败应交还失败：%v", err)
	}
	if err := w.m.RetryHandback("subA"); err == nil {
		t.Fatal("重新接管后仍按重启豁免无画面交还了")
	}
	if l, _ := w.l.Get("subA"); l.Holder != lease.HolderOwner {
		t.Fatalf("应保持接管：%+v", l)
	}
}

// 空会话号表示「中间正显示的那个」：Hide 与 Takeover 按状态机自己的焦点，不用界面手里可能过时的焦点（n049 真机演示）。
func TestEmptySessionMeansCurrentFocus(t *testing.T) {
	w := newWorld(t)
	w.m.Open("subA")
	w.m.Open("main")
	if err := w.m.Hide(""); err != nil {
		t.Fatal(err)
	}
	if st := w.m.State(); st.Focus != "subA" || contains(st.Open, "main") {
		t.Fatalf("应隐藏当时的焦点 main：%+v", st)
	}
	if err := w.m.Takeover(""); err != nil {
		t.Fatal(err)
	}
	if l, _ := w.l.Get("subA"); l.Holder != lease.HolderOwner {
		t.Fatal("应接管当时的焦点 subA")
	}
	w.m.Hide("")
	if err := w.m.Hide(""); err != ErrNotOpen {
		t.Fatalf("没有焦点时应是 ErrNotOpen：%v", err)
	}
	if err := w.m.Takeover(""); err != ErrNotOpen {
		t.Fatalf("没有焦点时接管应是 ErrNotOpen：%v", err)
	}
}
