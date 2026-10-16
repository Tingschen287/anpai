package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/arbiter"
	"github.com/Tingschen287/terminal-deck/work/internal/lease"
)

// grantUpstream 是一个假的 agent-deck：launch 把参数记进 $FAKE_SENT.launch，session send 把正文记进 $FAKE_SENT。
func grantUpstream(t *testing.T) (bin, sent string) {
	t.Helper()
	dir := t.TempDir()
	sent = filepath.Join(dir, "sent.txt")
	bin = filepath.Join(dir, "agent-deck")
	script := `#!/bin/sh
case "$1" in
  launch) echo "launch $*" >> "$FAKE_SENT.launch"; echo '{"success":true,"session_id":"sess-1","id":"sess-1","tool":"claude"}' ;;
  session) if [ "$2" = "send" ]; then echo "to=$3 $(cat)" >> "$FAKE_SENT"; echo '{"success":true,"delivery":"submitted","submitted":true}';
           elif [ "$2" = "stop" ]; then echo "stop $3" >> "$FAKE_SENT.stop"; echo '{"success":true}'; fi ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_SENT", sent)
	return bin, sent
}

type noClients struct{}

func (noClients) AttachedClients(context.Context, string) (int, error) { return 0, nil }

func lines(path string) []string {
	data, _ := os.ReadFile(path)
	if s := strings.TrimSpace(string(data)); s != "" {
		return strings.Split(s, "\n")
	}
	return nil
}

// 评审 evt_000472 blocker 2：记账此前完好、**本次** launch 后登记失败，任务文本一个字都不能出去。
// 用真实 arbiter 和假上游看上游到底收到了什么，不只看返回错误。
func TestTaskTextNeverLeavesWhenThisGrantFails(t *testing.T) {
	bin, sent := grantUpstream(t)
	c, err := agentdeck.NewClient(bin, "_grant", "", "")
	if err != nil {
		t.Fatal(err)
	}
	ldir := t.TempDir()
	leases, err := lease.Open(filepath.Join(ldir, "leases.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(leases.Close)
	d, _ := newDispatcher(t, arbiter.Deck{Client: c, Leases: leases, Clients: noClients{}})

	// 打开之后才让目录只读：记账此刻没停用（launch 前的预检放行），落盘在这次 Grant 才失败。
	os.Chmod(ldir, 0o500)
	defer os.Chmod(ldir, 0o700)
	if leases.Broken() != nil {
		t.Fatal("前提：记账此刻不该已停用")
	}
	req := goodRequest()
	req.Message = "开工：执行 n099"
	_, err = d.Dispatch(context.Background(), req)
	if leases.Broken() == nil {
		t.Skip("当前环境下目录只读没能挡住写入（可能以 root 运行），跳过")
	}
	if err == nil || !strings.Contains(err.Error(), "写入权登记失败") {
		t.Fatalf("登记失败必须拒绝派工并说明：%v", err)
	}
	if got := lines(sent + ".launch"); len(got) != 1 {
		t.Fatalf("前提：上游 launch 应当恰好被调用一次：%v", got)
	} else if strings.Contains(got[0], " -m ") || strings.Contains(got[0], "开工") {
		t.Errorf("任务文本随 launch 出去了：%s", got[0])
	}
	if got := lines(sent); len(got) != 0 {
		t.Errorf("登记失败后任务文本不该送进会话：%v", got)
	}
	if _, ok := d.Store.Get(req.NodeID, req.Attempt); ok {
		t.Error("没送出任务的会话不该落绑定")
	}
}

// 对照：登记成功时，任务文本经受仲裁的投递口送到，而且是在 launch 之后、另一次上游调用。
func TestTaskTextGoesThroughTheArbiterAfterTheGrant(t *testing.T) {
	bin, sent := grantUpstream(t)
	c, _ := agentdeck.NewClient(bin, "_grant", "", "")
	leases, err := lease.Open(filepath.Join(t.TempDir(), "leases.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(leases.Close)
	d, _ := newDispatcher(t, arbiter.Deck{Client: c, Leases: leases, Clients: noClients{}})
	req := goodRequest()
	req.Message = "开工：执行 n099"
	if _, err := d.Dispatch(context.Background(), req); err != nil {
		t.Fatalf("正常派工不该失败：%v", err)
	}
	if l := lines(sent + ".launch"); len(l) != 1 || strings.Contains(l[0], "开工") {
		t.Errorf("launch 不该带任务文本：%v", l)
	}
	if got := lines(sent); len(got) != 1 || got[0] != "to=sess-1 开工：执行 n099" {
		t.Errorf("任务文本应当经 session send 送到 sess-1：%v", got)
	}
	if l, ok := leases.Get("sess-1"); !ok || l.Holder != lease.HolderWorkbench {
		t.Errorf("送之前应当已登记 Work Deck 写入权：%+v", l)
	}
}
