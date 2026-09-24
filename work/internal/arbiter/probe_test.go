package arbiter

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/lease"
)

type fakeProbe struct {
	n   int
	err error
}

func (f fakeProbe) AttachedClients(context.Context, string) (int, error) { return f.n, f.err }

func grantedDeck(t *testing.T, probe ClientProbe) (Deck, *lease.Store, string) {
	t.Helper()
	bin, sent := fakeUpstream(t)
	c, _ := agentdeck.NewClient(bin, "_arb", "", "")
	leases, _ := lease.Open(filepath.Join(t.TempDir(), "leases.json"))
	t.Cleanup(leases.Close)
	leases.Grant("sess-1", "测试")
	return Deck{Client: c, Leases: leases, Clients: probe}, leases, sent
}

// 不经 Work Deck 的接管：tmux 上有客户端连着，就按 owner 接管登记、不投递；之后要确认交还才恢复。
func TestExternalAttachIsTreatedAsTakeover(t *testing.T) {
	d, leases, sent := grantedDeck(t, fakeProbe{n: 1})
	err := d.SendMessage(context.Background(), "sess-1", "brief")
	if err == nil || !strings.Contains(err.Error(), "检测到 1 个 tmux 客户端") {
		t.Fatalf("有客户端连着必须拒绝并说明：%v", err)
	}
	if l, _ := leases.Get("sess-1"); l.Holder != lease.HolderOwner || l.PendingHandback == nil {
		t.Errorf("应当登记为 owner 接管并挂上等确认的摘要（否则 H 交还不了）：%+v", l)
	}
	if got := sentLines(t, sent); len(got) != 0 {
		t.Errorf("上游不该收到任何东西：%v", got)
	}
	// owner 确认交还后：客户端还在就再次登记接管；客户端走了才投。
	leases.ConfirmHandback("sess-1")
	if err := d.SendMessage(context.Background(), "sess-1", "brief"); err == nil {
		t.Error("确认后客户端仍连着，必须再次拒绝")
	}
	leases.ConfirmHandback("sess-1")
	d.Clients = fakeProbe{n: 0}
	if err := d.SendMessage(context.Background(), "sess-1", "brief"); err != nil {
		t.Errorf("客户端都走了、已确认交还，应当恢复：%v", err)
	}
	if got := sentLines(t, sent); len(got) != 1 {
		t.Errorf("上游应当只收到恢复后那一条：%v", got)
	}
}

// 探测失败、或没接探测器：都不投递。
func TestProbeFailureOrAbsenceMeansNoDelivery(t *testing.T) {
	for name, probe := range map[string]ClientProbe{"探测失败": fakeProbe{err: errors.New("tmux 不答")}, "没接探测器": nil} {
		d, _, sent := grantedDeck(t, probe)
		if err := d.SendMessage(context.Background(), "sess-1", "brief"); err == nil {
			t.Errorf("%s：必须拒绝", name)
		}
		if got := sentLines(t, sent); len(got) != 0 {
			t.Errorf("%s：上游不该收到任何东西：%v", name, got)
		}
	}
}

// 并发：一条投递正在进行时（上游慢），owner 登记接管被拒；投递结束后才能接管。
func TestTakeoverCannotSlipInWhileDelivering(t *testing.T) {
	t.Setenv("FAKE_SLOW", "1")
	d, leases, _ := grantedDeck(t, fakeProbe{})
	var wg sync.WaitGroup
	wg.Add(1)
	var sendErr error
	go func() { defer wg.Done(); sendErr = d.SendMessage(context.Background(), "sess-1", "brief") }()
	time.Sleep(300 * time.Millisecond) // 让投递进到上游那一步
	var busy *lease.ErrDeliveryInProgress
	if err := leases.OwnerTakeover("sess-1", "upstream"); !errors.As(err, &busy) {
		t.Errorf("投递进行中登记接管必须被拒：%v", err)
	}
	wg.Wait()
	if sendErr != nil {
		t.Fatalf("投递本身应当成功：%v", sendErr)
	}
	if err := leases.OwnerTakeover("sess-1", "upstream"); err != nil {
		t.Errorf("投递结束后应当能接管：%v", err)
	}
}

// 记账停用时不起会话：起了也只是一个登记不上、投不进去的空会话。
func TestLaunchRefusedWhileLedgerIsBroken(t *testing.T) {
	bin, sent := fakeUpstream(t)
	c, _ := agentdeck.NewClient(bin, "_arb", "", "")
	dir := t.TempDir()
	leases, _ := lease.Open(filepath.Join(dir, "leases.json"))
	t.Cleanup(leases.Close)
	breakLedger(t, leases, dir)
	d := Deck{Client: c, Leases: leases, Clients: fakeProbe{}}
	if _, err := d.Launch(context.Background(), agentdeck.LaunchRequest{ProjectPath: "/tmp", Tool: "shell", ModelNotApplicable: true}); err == nil {
		t.Fatal("记账停用时不该派工")
	}
	// 光看返回错误不够：去掉预检的话，launch 照样会跑、事后登记才失败，也返回错误。
	// 要看的是上游的 launch 有没有被调用。
	if got := sentLines(t, sent+".launch"); len(got) != 0 {
		t.Errorf("记账停用时上游 launch 不该被调用：%v", got)
	}
}
