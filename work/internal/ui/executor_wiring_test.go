package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/arbiter"
	"github.com/Tingschen287/terminal-deck/work/internal/lease"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

type noClients struct{}

func (noClients) AttachedClients(context.Context, string) (int, error) { return 0, nil }

// n039 attempt 3 评审的 blocker：界面派执行者时不带任务文本，派工器跳过投递仍显示「已派」。
// 这条从界面按键一路走到上游：真实派工器、真实 arbiter 与写入权记账、假的 agent-deck 二进制记下每次调用。
func TestUIExecutorDispatchDeliversTheBriefToUpstream(t *testing.T) {
	dir := t.TempDir()
	sent := filepath.Join(dir, "sent.txt")
	bin := filepath.Join(dir, "agent-deck")
	script := `#!/bin/sh
case "$1" in
  launch) echo "launch $*" >> "$FAKE_SENT.launch"; echo '{"success":true,"session_id":"exec-sess","id":"exec-sess"}' ;;
  session) if [ "$2" = "send" ]; then printf 'to=%s\n' "$3" >> "$FAKE_SENT"; cat >> "$FAKE_SENT"; fi ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_SENT", sent)
	c, err := agentdeck.NewClient(bin, "_uiexec", "", "")
	if err != nil {
		t.Fatal(err)
	}
	leases, err := lease.Open(filepath.Join(t.TempDir(), "leases.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(leases.Close)
	d := realDispatcher(t, arbiter.Deck{Client: c, Leases: leases, Clients: noClients{}})

	m := modelWithDispatcherAndBindings(t, []projectctl.Node{nodeWith("n1", projectctl.StateReady, "frontier")}, d, d.Store)
	m = pressD(m)
	if m.mode != ModePickModel || m.pickingReviewer {
		t.Fatalf("前提：ready 节点按 d 应当进入挑执行模型：%q", m.notice)
	}
	m, cmd := enterWithReason(t, m, "唯一够档的")
	if cmd == nil {
		t.Fatal("回车应当真的派工")
	}
	done, ok := cmd().(dispatchDoneMsg)
	if !ok || !done.ok || !strings.Contains(done.text, "brief 已送达") {
		t.Fatalf("派工应当成功并说明 brief 已送达：%+v", done)
	}

	launches, _ := os.ReadFile(sent + ".launch")
	if n := strings.Count(string(launches), "\n"); n != 1 || strings.Contains(string(launches), "执行者") {
		t.Errorf("应当恰好一次不带任务文本的 launch：%s", launches)
	}
	got, _ := os.ReadFile(sent)
	body := string(got)
	if !strings.HasPrefix(body, "to=exec-sess\n") {
		t.Fatalf("执行 brief 没送到上游的 exec-sess：%q", body)
	}
	for _, want := range []string{"n1 的执行者", "第 1 次 attempt", "acceptance_criteria", "submit"} {
		if !strings.Contains(body, want) {
			t.Errorf("上游收到的 brief 缺 %q：\n%s", want, body)
		}
	}
	if b, ok := d.Store.Get("n1", 1); !ok || b.SessionID != "exec-sess" {
		t.Errorf("送达后才落绑定：%+v", b)
	}
	if l, ok := leases.Get("exec-sess"); !ok || l.Holder != lease.HolderWorkbench {
		t.Errorf("投递前应当已登记 Work Deck 写入权：%+v", l)
	}
}
