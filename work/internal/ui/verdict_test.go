package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tingschen287/terminal-deck/work/internal/binding"
	"github.com/Tingschen287/terminal-deck/work/internal/dispatch"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

func verdictModel(t *testing.T, pane string) (Model, *fakeStarter, *fakeDispatcher) {
	t.Helper()
	n := submittedNode("n1", 2, "exec")
	n.ReviewerRole = "independent_workbench_reviewer"
	fd := frontierFD()
	st := &fakeStarter{}
	b := fakeBindings{dispatch.ReviewerKey("n1") + "#2": {NodeID: dispatch.ReviewerKey("n1"), Attempt: 2,
		SessionID: "rev-sess", Model: "gpt-6-sol", RouteClass: "frontier"}}
	m := modelWithDispatcherAndBindings(t, []projectctl.Node{n}, fd, b).WithStarter(st)
	m.attacher = &fakeAttacher{text: pane}
	m.snap.ProjectRoot = t.TempDir()
	m.qw.script = "/x/projectctl.py"
	return m, st, fd
}

func pressV(m Model) Model {
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	return next.(Model)
}

// n040 终审断点 6：按 v 在界面上看到评审结论，并拿到一条填好参数、可直接复制的 projectctl review 命令。
// Work Deck 自己不记账：按 v 不调开工、不调派工，也没有任何 review 写入口。
func TestVerdictIsShownWithACopyableReviewCommand(t *testing.T) {
	m, st, fd := verdictModel(t, "…\nVERDICT: accept\n（改主意）\nVERDICT: rework\n$ ")
	m = pressV(m)
	for _, want := range []string{
		"评审结论：rework", "rev-sess", "还不存在",
		"$ python3 /x/projectctl.py review n1 --decision rework --reviewer workdeck-review-rev-sess --role independent_workbench_reviewer --route-class frontier --model gpt-6-sol --report 4.eval/reviews/n1-attempt2-workdeck-review.md",
		"不替你记账",
	} {
		if !strings.Contains(m.detail, want) {
			t.Errorf("详情缺 %q：\n%s", want, m.detail)
		}
	}
	if len(st.got) != 0 || len(fd.got) != 0 || len(fd.reviews) != 0 {
		t.Error("按 v 不该开工或派工")
	}
	// 命令行不折行，复制下来是完整的一行。
	out := RenderScreen(m.snap, 80, Screen{Detail: m.detail, DetailOf: m.detailOf})
	if !strings.Contains(out, "--report 4.eval/reviews/n1-attempt2-workdeck-review.md --reason") {
		t.Errorf("命令被折断了：\n%s", out)
	}

	// 报告写好了就说已存在。
	os.MkdirAll(filepath.Join(m.snap.ProjectRoot, "4.eval", "reviews"), 0o755)
	os.WriteFile(filepath.Join(m.snap.ProjectRoot, reviewReportPath("n1", 2)), []byte("r"), 0o644)
	if m = pressV(m); !strings.Contains(m.detail, "已存在") {
		t.Errorf("报告已写好时应当说已存在：\n%s", m.detail)
	}
}

// 没有 VERDICT 行、没派评审、不是 submitted：都说清楚，不给命令。
func TestVerdictRefusalsExplainThemselves(t *testing.T) {
	m, _, _ := verdictModel(t, "还在看代码")
	if m = pressV(m); m.detail != "" || !strings.Contains(m.notice, "还没有 VERDICT 行") {
		t.Errorf("没结论时不给命令：%q %q", m.notice, m.detail)
	}
	m, _, _ = verdictModel(t, "VERDICT: accept")
	m.bindings = fakeBindings{}
	if m = pressV(m); m.detail != "" || !strings.Contains(m.notice, "先按 d 派评审") {
		t.Errorf("没派评审时要说：%q", m.notice)
	}
	m, _, _ = verdictModel(t, "VERDICT: accept")
	m.snap.Nodes[0].State = projectctl.Known(projectctl.StateRunning)
	if m = pressV(m); m.detail != "" || !strings.Contains(m.notice, "不是 submitted") {
		t.Errorf("非 submitted 要说：%q", m.notice)
	}
}

// 评审 brief 告诉评审者报告写到哪，和 v 引用的路径一致。
func TestReviewBriefNamesTheReportPath(t *testing.T) {
	if !strings.Contains(reviewBrief(submittedNode("n1", 2, "exec"), 2), reviewReportPath("n1", 2)) {
		t.Error("评审 brief 没说报告写到哪")
	}
}

// v 给的命令要真的能被控制器接受：在夹具副本上对 submitted 的 f03 原样运行（真实 projectctl）。
func TestIntegrationVerdictCommandIsAcceptedByTheRealController(t *testing.T) {
	script, err := projectctl.DefaultScript()
	if err != nil {
		t.Skipf("跳过：找不到 projectctl（%v）", err)
	}
	src, _ := filepath.Abs("../../testdata/workdeck-fixture")
	dst := filepath.Join(t.TempDir(), "workdeck-fixture")
	if out, err := exec.Command("cp", "-r", src, dst).CombinedOutput(); err != nil {
		t.Fatalf("复制夹具失败：%v %s", err, out)
	}
	node := projectctl.Node{ID: "f03_write_report", ReviewerRole: "fixture_reviewer"}
	b := binding.Binding{SessionID: "rev-sess", Model: "fixture-model", RouteClass: "balanced"}
	report := filepath.Join(dst, reviewReportPath(node.ID, 1))
	os.MkdirAll(filepath.Dir(report), 0o755)
	os.WriteFile(report, []byte("# 夹具评审\n\nVERDICT: accept\n"), 0o644)
	cmd := exec.Command("bash", "-c", reviewCommand(script, node, 1, b, "accept"))
	cmd.Dir = dst
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "accepted") {
		t.Fatalf("控制器没接受这条命令：%v\n%s", err, out)
	}
}
