package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

func writeReport(t *testing.T, m Model, body string) string {
	t.Helper()
	path := filepath.Join(m.snap.ProjectRoot, reviewReportPath("n1", 2))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// n040 终审断点 6：按 v 在界面上看到评审结论，并拿到一条填好参数、可直接复制的 projectctl review 命令。
// n044：结论取自报告文件（不是会话画面），给出报告的 sha256，并写明这不是验收事实。
// Work Deck 自己不记账：按 v 不调开工、不调派工，也没有任何 review 写入口。
func TestVerdictIsShownWithACopyableReviewCommand(t *testing.T) {
	m, st, fd := verdictModel(t, "…\nVERDICT: rework\n$ ")
	sha := writeReport(t, m, "# 评审\n\nVERDICT: accept\n（改主意）\nVERDICT: rework\n")
	m = pressV(m)
	for _, want := range []string{
		"评审结论（取自报告文件）：rework", "rev-sess", "报告：4.eval/reviews/n1-attempt2-workdeck-review.md（sha256 " + sha, "这不是验收事实",
		"$ python3 /x/projectctl.py review n1 --decision rework --reviewer workdeck-review-rev-sess --role independent_workbench_reviewer --route-class frontier --model gpt-6-sol --report 4.eval/reviews/n1-attempt2-workdeck-review.md",
		"sha256 " + sha + "）结论 VERDICT: rework",
		"不替你记账",
	} {
		if !strings.Contains(m.detail, want) {
			t.Errorf("详情缺 %q：\n%s", want, m.detail)
		}
	}
	if !strings.Contains(m.notice, "这不是验收事实") {
		t.Errorf("提示行也要说这不是验收事实：%q", m.notice)
	}
	// 界面不知道 owner 核没核对，命令理由不能预先替他写「已核对」（n044 attempt 1 评审次要项）。
	if strings.Contains(m.detail, "已核对") {
		t.Errorf("记账命令理由不该预先声称已核对：\n%s", m.detail)
	}
	if len(st.got) != 0 || len(fd.got) != 0 || len(fd.reviews) != 0 {
		t.Error("按 v 不该开工或派工")
	}
	// 命令行不折行，复制下来是完整的一行。
	out := RenderScreen(m.snap, 80, Screen{Detail: m.detail, DetailOf: m.detailOf})
	if !strings.Contains(out, "--report 4.eval/reviews/n1-attempt2-workdeck-review.md --reason") {
		t.Errorf("命令被折断了：\n%s", out)
	}
}

// n043 复审新问题 3：只在评审会话画面上印一行 VERDICT（任何能往画面写字的进程都做得到），界面不再据此给命令。
func TestAVerdictOnlyOnThePaneGivesNoCommand(t *testing.T) {
	m, _, _ := verdictModel(t, "printf 'VERDICT: accept\\n'\nVERDICT: accept\n")
	if m = pressV(m); m.detail != "" || !strings.Contains(m.notice, "还读不到") {
		t.Errorf("没有报告文件时不该给命令：%q %q", m.notice, m.detail)
	}
}

// 画面和报告不一致：以报告为准，并明说不一致。读不到画面也照样按报告给命令。
func TestPaneVerdictIsOnlyAHint(t *testing.T) {
	m, _, _ := verdictModel(t, "VERDICT: accept\n")
	writeReport(t, m, "逐条核验……\nVERDICT: rework\n")
	m = pressV(m)
	if !strings.Contains(m.detail, "评审结论（取自报告文件）：rework") || !strings.Contains(m.detail, "画面写的是 accept，报告写的是 rework") ||
		!strings.Contains(m.detail, "--decision rework") {
		t.Errorf("应当以报告为准并警告不一致：\n%s", m.detail)
	}

	m, _, _ = verdictModel(t, "")
	m.attacher = &fakeAttacher{textErr: errors.New("会话没了")}
	writeReport(t, m, "VERDICT: accept\n")
	if m = pressV(m); !strings.Contains(m.detail, "--decision accept") || !strings.Contains(m.detail, "只按报告") {
		t.Errorf("读不到画面时按报告给命令并说明：\n%s", m.detail)
	}
}

// 没有结论行、没派评审、不是 submitted：都说清楚，不给命令。
func TestVerdictRefusalsExplainThemselves(t *testing.T) {
	m, _, _ := verdictModel(t, "还在看代码")
	writeReport(t, m, "# 评审（写了一半）\n")
	if m = pressV(m); m.detail != "" || !strings.Contains(m.notice, "没有独占一行的 VERDICT") {
		t.Errorf("报告没有结论时不给命令：%q %q", m.notice, m.detail)
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

// useRealPythonInCommands 让命令文字里的 python3 换成本机选出的 3.11 及以上的解释器：集成测试要把命令原样交给 shell 跑，
// 而 Mac 上 PATH 里的 python3 是 3.9，跑不了控制器（n067）。选不出就跳过。
func useRealPythonInCommands(t *testing.T) {
	t.Helper()
	py, err := projectctl.ResolvePython()
	if err != nil {
		t.Skipf("跳过：没有 Python 3.11 及以上（%v）", err)
	}
	old := projectctl.PythonInText
	projectctl.PythonInText = py
	t.Cleanup(func() { projectctl.PythonInText = old })
}

// v 给的命令要真的能被控制器接受：在夹具副本上对 submitted 的 f03 原样运行（真实 projectctl）。
func TestIntegrationVerdictCommandIsAcceptedByTheRealController(t *testing.T) {
	script, err := projectctl.DefaultScript()
	if err != nil {
		t.Skipf("跳过：找不到 projectctl（%v）", err)
	}
	useRealPythonInCommands(t)
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
	cmd := exec.Command("bash", "-c", reviewCommand(script, node, 1, b, "accept", "0123456789ab"))
	cmd.Dir = dst
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "accepted") {
		t.Fatalf("控制器没接受这条命令：%v\n%s", err, out)
	}
}

// n042 attempt 1 评审 blocker 2：评审 brief 本身会留在画面上（送达未确认时还可能停在输入框），
// 它不能被认成结论；只认独占一行的结论，夹在句子里的不算。
func TestOnlyAStandaloneVerdictLineCounts(t *testing.T) {
	brief := reviewBrief(submittedNode("n1", 2, "exec"), 2)
	if v := lastVerdict(brief); v != "" {
		t.Errorf("brief 本身不该被认成结论，却认出 %q", v)
	}
	for pane, want := range map[string]string{
		"VERDICT: accept":     "accept",
		"  • VERDICT: rework": "rework",
		"**VERDICT: accept**": "accept",
		"> VERDICT: rework.":  "rework",
		"`VERDICT: accept`":   "accept",
		"格式：`VERDICT: accept` 或 `VERDICT: rework`。": "",
		"结论写 VERDICT: accept 即可":                    "",
		"VERDICT: accept 但我还没看完":                    "",
		"说明：VERDICT: accept":                        "",
		"评审者说 VERDICT: rework":                      "",
		"VERDICT: acceptable":                       "",
		brief + "\n…评审中…\nVERDICT: accept\n":        "accept",
	} {
		if got := lastVerdict(pane); got != want {
			t.Errorf("画面 %q：认出 %q，期望 %q", pane, got, want)
		}
	}
	// 画面上只有 brief（评审还没开始）、也没有报告时按 v 不给命令；报告里只抄了 brief 同样不给（n044：结论取自报告）。
	m, _, _ := verdictModel(t, brief)
	if m = pressV(m); m.detail != "" || !strings.Contains(m.notice, "还读不到") {
		t.Errorf("只有 brief 时不该给记账命令：%q", m.notice)
	}
	writeReport(t, m, brief)
	if m = pressV(m); m.detail != "" || !strings.Contains(m.notice, "没有独占一行的 VERDICT") {
		t.Errorf("报告里只有 brief 时不该给记账命令：%q", m.notice)
	}
}
