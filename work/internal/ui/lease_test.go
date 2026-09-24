package ui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/lease"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

func leaseModel(t *testing.T) (Model, *fakeAttacher, *lease.Store) {
	t.Helper()
	store, err := lease.Open(filepath.Join(t.TempDir(), "leases.json"))
	if err != nil {
		t.Fatal(err)
	}
	store.Grant("sess-7", "测试：Work Deck 派出的会话")
	fa := &fakeAttacher{text: "画面 A"}
	binds := fakeBindings{"n1#1": {NodeID: "n1", Attempt: 1, SessionID: "sess-7"}}
	m := modelWithAttacher(t, []projectctl.Node{runningNode("n1", 1)}, fa, binds).WithLeases(store)
	return m, fa, store
}

// 按 a 进入：交出终端之前就登记 owner 接管，此刻起自动投递停。
func TestAttachRecordsTheOwnerTakeoverFirst(t *testing.T) {
	m, _, store := leaseModel(t)
	_, cmd := press(m, 'a')
	if cmd == nil {
		t.Fatal("应当交出终端")
	}
	if l, _ := store.Get("sess-7"); l.Holder != lease.HolderOwner {
		t.Fatalf("交出终端前应当已登记 owner 接管：%+v", l)
	}
	if _, err := store.Automatic("sess-7"); err == nil {
		t.Error("接管期间自动投递必须被拒")
	}
}

// 回来后挂介入摘要、主界面常驻提示；H 看摘要，y 确认才恢复；别的键取消。
func TestHandbackNeedsTheOwnersConfirmation(t *testing.T) {
	m, fa, store := leaseModel(t)
	m, _ = press(m, 'a')
	fa.text = "画面 B：owner 在里面敲过命令"
	next, _ := m.Update(attachDoneMsg{sessionID: "sess-7"})
	m = next.(Model)
	l, _ := store.Get("sess-7")
	if l.PendingHandback == nil || l.PendingHandback.PaneChanged == nil || !*l.PendingHandback.PaneChanged {
		t.Fatalf("回来后应当挂上摘要且看出画面变了：%+v", l.PendingHandback)
	}
	if _, err := store.Automatic("sess-7"); err == nil {
		t.Fatal("确认交还前自动投递必须被拒")
	}
	if out := m.View(); !strings.Contains(out, "会话 sess-7 等你确认交还") {
		t.Errorf("主界面要常驻提示：\n%s", out)
	}
	m, _ = press(m, 'H')
	if !strings.Contains(m.detail, "介入摘要") || !strings.Contains(m.detail, "画面有变化") || !strings.Contains(m.detail, "owner 在里面敲过命令") {
		t.Errorf("H 应当展示介入摘要：%q", m.detail)
	}
	m, _ = press(m, 'n')
	if _, err := store.Automatic("sess-7"); err == nil {
		t.Error("取消后仍然不该恢复")
	}
	m, _ = press(m, 'H')
	m, _ = press(m, 'y')
	if _, err := store.Automatic("sess-7"); err != nil {
		t.Errorf("确认交还后应当恢复：%v", err)
	}
	if !strings.Contains(m.notice, "已确认交还") {
		t.Errorf("要告诉人已经恢复：%q", m.notice)
	}
}

// 记账打不开：主界面常驻告警，所有自动投递停着。
func TestLeaseStoreErrorIsAStandingAlert(t *testing.T) {
	m := New(nil).WithLeasesError(errFake("第 1 条不完整"))
	m.snap = projectctl.Snapshot{ProjectRoot: "/tmp/proj", Nodes: []projectctl.Node{runningNode("n1", 1)}}
	m.inflight, m.loading = false, false
	if out := m.View(); !strings.Contains(out, "写入权记账打不开") {
		t.Errorf("记账打不开时要告警：\n%s", out)
	}
}

type errFake string

func (e errFake) Error() string { return string(e) }
