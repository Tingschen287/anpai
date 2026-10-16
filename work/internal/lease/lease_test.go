package lease

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func openAt(t *testing.T, path string, now *time.Time) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return *now }
	t.Cleanup(s.Close)
	return s
}

// 没有记录的会话，持有者未知：不自动投递。Work Deck 自己派出的新会话登记后才放行。
func TestAutomaticNeedsAWorkbenchLease(t *testing.T) {
	now := t0
	s := openAt(t, filepath.Join(t.TempDir(), "leases.json"), &now)
	if _, err := s.Automatic("s1"); err == nil || !strings.Contains(err.Error(), "持有者未知") {
		t.Fatalf("没有记录时必须拒绝并说明：%v", err)
	}
	if err := s.Grant("s1", "测试"); err != nil {
		t.Fatal(err)
	}
	p, err := s.Automatic("s1")
	if err != nil {
		t.Fatalf("登记后应当放行：%v", err)
	}
	if err := p.Allows("s1"); err != nil {
		t.Errorf("刚签的许可应当有效：%v", err)
	}
}

// owner 接管期间一切自动投递停止——包括接管之前已经签出去的许可。
func TestOwnerTakeoverStopsAutomaticDelivery(t *testing.T) {
	now := t0
	s := openAt(t, filepath.Join(t.TempDir(), "leases.json"), &now)
	s.Grant("s1", "测试")
	early, _ := s.Automatic("s1")
	if err := s.OwnerTakeover("s1", "upstream"); err != nil {
		t.Fatal(err)
	}
	if err := early.Allows("s1"); err == nil {
		t.Error("接管前签的许可，接管后必须失效")
	}
	if _, err := s.Automatic("s1"); err == nil || !strings.Contains(err.Error(), "正由 owner 接管") {
		t.Errorf("接管期间不能签自动许可：%v", err)
	}
	if _, err := s.Manual("s1"); err == nil {
		t.Error("接管期间连手动回答也不该投进去（owner 正在里面操作）")
	}
	if err := s.Grant("s1", "想夺回"); err == nil {
		t.Error("Work Deck 不能借 Grant 从 owner 手里取回写入权")
	}
}

// 接管后交还：挂介入摘要，owner 确认前不恢复。
func TestHandbackRequiresConfirmation(t *testing.T) {
	now := t0
	s := openAt(t, filepath.Join(t.TempDir(), "leases.json"), &now)
	s.Grant("s1", "测试")
	s.OwnerTakeover("s1", "tmux")
	now = t0.Add(3 * time.Minute)
	changed := true
	if err := s.OwnerReturn("s1", Handback{AttachedAt: t0, ReturnedAt: now, DurationSeconds: 180, Via: "tmux", PaneChanged: &changed}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Automatic("s1"); err == nil || !strings.Contains(err.Error(), "确认交还") {
		t.Fatalf("回来但未确认时必须拒绝：%v", err)
	}
	l, _ := s.Get("s1")
	if l.PendingHandback == nil || l.PendingHandback.DurationSeconds != 180 {
		t.Fatalf("介入摘要没挂上：%+v", l)
	}
	if err := s.ConfirmHandback("s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Automatic("s1"); err != nil {
		t.Errorf("确认交还后应当恢复：%v", err)
	}
}

// 租约过期：自动投递停，要 owner 续约；手动回答不受影响。
func TestExpiredLeaseFailsClosed(t *testing.T) {
	now := t0
	s := openAt(t, filepath.Join(t.TempDir(), "leases.json"), &now)
	s.Grant("s1", "测试")
	now = t0.Add(DefaultTTL + time.Second)
	if _, err := s.Automatic("s1"); err == nil || !strings.Contains(err.Error(), "过期") {
		t.Fatalf("过期后必须拒绝并说明：%v", err)
	}
	if _, err := s.Manual("s1"); err != nil {
		t.Errorf("过期不影响 owner 的手动回答：%v", err)
	}
	if err := s.Renew("s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Automatic("s1"); err != nil {
		t.Errorf("续约后应当恢复：%v", err)
	}
}

// 持有者持久化：重启后 owner 接管的记录还在，照样拒绝。文件坏了报错，不当成空记账。
func TestLeasesSurviveRestartAndBadFilesAreErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leases.json")
	now := t0
	s := openAt(t, path, &now)
	s.Grant("s1", "测试")
	s.OwnerTakeover("s1", "upstream")
	s.Close() // 模拟进程退出：锁随之释放
	s2 := openAt(t, path, &now)
	if _, err := s2.Automatic("s1"); err == nil {
		t.Fatal("重启后 owner 接管的记录丢了")
	}
	for name, content := range map[string]string{
		"截断":       `{"version":1,"leases":[`,
		"缺 leases": `{"version":1}`,
		"版本不对":     `{"version":9,"leases":[]}`,
		"持有者乱写":    `{"version":1,"leases":[{"session_id":"s","holder":"someone"}]}`,
		"重复":       `{"version":1,"leases":[{"session_id":"s","holder":"owner"},{"session_id":"s","holder":"owner"}]}`,
	} {
		p := filepath.Join(t.TempDir(), "leases.json")
		os.WriteFile(p, []byte(content), 0o600)
		if s, err := Open(p); err == nil {
			s.Close()
			t.Errorf("%s：应当报错", name)
		}
	}
}

// 每次切换都有可观测的事件。
func TestEverySwitchIsAnEvent(t *testing.T) {
	now := t0
	s := openAt(t, filepath.Join(t.TempDir(), "leases.json"), &now)
	s.Grant("s1", "测试")
	s.OwnerTakeover("s1", "upstream")
	s.OwnerReturn("s1", Handback{ReturnedAt: now})
	s.ConfirmHandback("s1")
	evs, err := s.Events()
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
	}
	want := []string{EventGrant, EventTakeover, EventReturn, EventConfirm}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Errorf("事件是 %v，期望 %v", kinds, want)
	}
	if evs[1].From != HolderWorkbench || evs[1].To != HolderOwner {
		t.Errorf("接管事件的持有者变化不对：%+v", evs[1])
	}
}

// 自己构造的许可无效；给别的会话签的许可不能挪用。
func TestForgedOrBorrowedPermitsAreInvalid(t *testing.T) {
	if err := (Permit{}).Allows("s1"); err == nil {
		t.Error("零值许可必须无效")
	}
	now := t0
	s := openAt(t, filepath.Join(t.TempDir(), "leases.json"), &now)
	s.Grant("s1", "测试")
	s.Grant("s2", "测试")
	p, _ := s.Automatic("s1")
	if err := p.Allows("s2"); err == nil {
		t.Error("给 s1 的许可不能用于 s2")
	}
}

// 落盘失败：内存退回原样，记账停用，此后一律不签许可。
func TestFlushFailureStopsAllDelivery(t *testing.T) {
	dir := t.TempDir()
	now := t0
	s := openAt(t, filepath.Join(dir, "leases.json"), &now)
	s.Grant("s1", "测试")
	os.Chmod(dir, 0o500)
	defer os.Chmod(dir, 0o700)
	if err := s.OwnerTakeover("s1", "upstream"); err == nil {
		t.Skip("当前环境下目录只读没能挡住写入（可能以 root 运行），跳过")
	}
	if s.Broken() == nil {
		t.Fatal("落盘失败后应当停用")
	}
	if _, err := s.Automatic("s1"); err == nil || !strings.Contains(err.Error(), "停用") {
		t.Errorf("停用后连原本有效的租约也不该签许可：%v", err)
	}
	if _, err := s.Manual("s9"); err == nil {
		t.Error("停用后手动许可也不签")
	}
}

// 同一份记账只许一个进程持有：第二个 Open 失败，第一个关掉后才能再开。
func TestOnlyOneStoreMayHoldTheLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leases.json")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := Open(path); err == nil {
		b.Close()
		t.Fatal("第二个实例不该打开：两份记账互不知道对方的接管")
	} else if !strings.Contains(err.Error(), "另一个 Work Deck 进程持有") {
		t.Errorf("要说清为什么打不开：%v", err)
	}
	a.Close()
	c, err := Open(path)
	if err != nil {
		t.Fatalf("第一个关掉后应当能打开：%v", err)
	}
	c.Close()
}

// 投递进行中不许登记接管；投完才能进。
func TestTakeoverWaitsForADeliveryInProgress(t *testing.T) {
	now := t0
	s := openAt(t, filepath.Join(t.TempDir(), "leases.json"), &now)
	s.Grant("s1", "测试")
	p, _ := s.Automatic("s1")
	release, err := p.Begin("s1")
	if err != nil {
		t.Fatal(err)
	}
	var busy *ErrDeliveryInProgress
	if err := s.OwnerTakeover("s1", "upstream"); !errors.As(err, &busy) {
		t.Fatalf("投递进行中登记接管必须被拒：%v", err)
	}
	release()
	release() // 多调一次无害
	if err := s.OwnerTakeover("s1", "upstream"); err != nil {
		t.Fatalf("投完之后应当能接管：%v", err)
	}
	if _, err := p.Begin("s1"); err == nil {
		t.Error("接管后旧许可不能再开始投递")
	}
}

// 切换事件和持有者在同一个文件里：重开之后事件还在，且与持有者一致。
func TestEventsLiveInTheSameFileAsHolders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leases.json")
	now := t0
	s := openAt(t, path, &now)
	s.Grant("s1", "测试")
	s.OwnerTakeover("s1", "tmux")
	s.Close()
	s2 := openAt(t, path, &now)
	evs, _ := s2.Events()
	l, _ := s2.Get("s1")
	if len(evs) != 2 || evs[1].To != l.Holder {
		t.Errorf("重开后事件与持有者对不上：%+v %+v", evs, l)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			t.Errorf("事件不该另写文件：%s", e.Name())
		}
	}
}
