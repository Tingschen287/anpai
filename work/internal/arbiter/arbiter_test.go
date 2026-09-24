package arbiter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/lease"
)

// fakeUpstream 写一个假的 agent-deck：launch 回一个固定会话号，session send 把正文追加进 FAKE_SENT。
// 用它看的是「投递到底有没有跑出去」，而不只是函数有没有返回错误。
func fakeUpstream(t *testing.T) (bin, sent string) {
	t.Helper()
	dir := t.TempDir()
	sent = filepath.Join(dir, "sent.txt")
	bin = filepath.Join(dir, "agent-deck")
	script := `#!/bin/sh
case "$1" in
  launch) echo "launch $*" >> "$FAKE_SENT.launch"; echo '{"success":true,"session_id":"sess-1","id":"sess-1"}' ;;
  session) if [ "$2" = "send" ]; then [ -n "$FAKE_SLOW" ] && sleep 1; echo "to=$3 $(cat)" >> "$FAKE_SENT"; fi ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_SENT", sent)
	return bin, sent
}

func sentLines(t *testing.T, path string) []string {
	data, _ := os.ReadFile(path)
	s := strings.TrimSpace(string(data))
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// 派工器这条路：launch 成功就登记写入权，brief 用自动许可投得进去；owner 一接管，
// 再投就被挡在 adapter 之前，上游一次都没被调用。
func TestAutomaticDeliveryStopsWhileOwnerHoldsTheSession(t *testing.T) {
	bin, sent := fakeUpstream(t)
	c, err := agentdeck.NewClient(bin, "_arb", "", "")
	if err != nil {
		t.Fatal(err)
	}
	leases, _ := lease.Open(filepath.Join(t.TempDir(), "leases.json"))
	d := Deck{Client: c, Leases: leases, Clients: fakeProbe{}}
	ctx := context.Background()
	res, err := d.Launch(ctx, agentdeck.LaunchRequest{ProjectPath: "/tmp", Tool: "shell", ModelNotApplicable: true})
	if err != nil {
		t.Fatal(err)
	}
	if l, ok := leases.Get(res.SessionID); !ok || l.Holder != lease.HolderWorkbench {
		t.Fatalf("新会话应当登记为 Work Deck 持有：%+v", l)
	}
	if err := d.SendMessage(ctx, res.SessionID, "brief-1"); err != nil {
		t.Fatal(err)
	}
	if err := leases.OwnerTakeover(res.SessionID, "upstream"); err != nil {
		t.Fatal(err)
	}
	if err := d.SendMessage(ctx, res.SessionID, "brief-2"); err == nil {
		t.Error("owner 接管期间自动投递必须被拒")
	}
	leases.OwnerReturn(res.SessionID, lease.Handback{})
	if err := d.SendMessage(ctx, res.SessionID, "brief-3"); err == nil {
		t.Error("未确认交还前自动投递必须被拒")
	}
	if got := sentLines(t, sent); len(got) != 1 || !strings.Contains(got[0], "brief-1") {
		t.Fatalf("上游只该收到接管前那一条：%v", got)
	}
	leases.ConfirmHandback(res.SessionID)
	if err := d.SendMessage(ctx, res.SessionID, "brief-4"); err != nil {
		t.Fatalf("确认交还后应当恢复：%v", err)
	}
	if got := sentLines(t, sent); len(got) != 2 {
		t.Errorf("确认后应当多出一条：%v", got)
	}
}

// 不是 Work Deck 派出的会话，没有写入权记录：自动投递拒绝，owner 的手动回答放行；
// owner 接管它之后，手动回答也拒绝。
func TestManualAnswerRules(t *testing.T) {
	bin, sent := fakeUpstream(t)
	c, _ := agentdeck.NewClient(bin, "_arb", "", "")
	leases, _ := lease.Open(filepath.Join(t.TempDir(), "leases.json"))
	ctx := context.Background()
	if err := (Deck{Client: c, Leases: leases, Clients: fakeProbe{}}).SendMessage(ctx, "child-1", "x"); err == nil {
		t.Error("没有记录的会话不该被自动投递")
	}
	del := Deliverer{Client: c, Leases: leases}
	rec := agentdeck.InboxRecord{ChildSessionID: "child-1", Profile: "_arb"}
	if err := del.DeliverAnswer(ctx, rec, "继续"); err != nil {
		t.Fatalf("owner 的手动回答应当放行：%v", err)
	}
	leases.OwnerTakeover("child-1", "tmux")
	if err := del.DeliverAnswer(ctx, rec, "继续"); err == nil {
		t.Error("owner 正接管时手动回答也不该投进去")
	}
	if got := sentLines(t, sent); len(got) != 1 {
		t.Errorf("上游应当只收到一条：%v", got)
	}
}

// 没有记账时什么都不投。
func TestNoLeaseStoreMeansNoDelivery(t *testing.T) {
	bin, sent := fakeUpstream(t)
	c, _ := agentdeck.NewClient(bin, "_arb", "", "")
	ctx := context.Background()
	if err := (Deck{Client: c}).SendMessage(ctx, "s", "x"); err == nil {
		t.Error("没有记账时不该投递")
	}
	if _, err := (Deck{Client: c}).Launch(ctx, agentdeck.LaunchRequest{ProjectPath: "/tmp", Tool: "shell", ModelNotApplicable: true}); err == nil {
		t.Error("没有记账时不该派工")
	}
	if err := (Deliverer{Client: c}).DeliverAnswer(ctx, agentdeck.InboxRecord{ChildSessionID: "s"}, "x"); err == nil {
		t.Error("没有记账时不该投递回答")
	}
	if got := sentLines(t, sent); len(got) != 0 {
		t.Errorf("上游不该收到任何东西：%v", got)
	}
}

// breakLedger 让记账落盘失败一次，从而停用。
func breakLedger(t *testing.T, s *lease.Store, dir string) {
	t.Helper()
	os.Chmod(dir, 0o500)
	defer os.Chmod(dir, 0o700)
	if err := s.OwnerTakeover("any", "upstream"); err == nil || s.Broken() == nil {
		t.Skip("当前环境下目录只读没能挡住写入（可能以 root 运行），跳过")
	}
}
