package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tingschen287/terminal-deck/work/internal/dispatch"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

// 主界面上 Esc 不退出（n047）。n045 attempt 2 的界面进程就是被一次顺手的 Esc 关掉的。
func TestTopLevelEscapeDoesNotQuit(t *testing.T) {
	m := modelWithDispatcher(t, []projectctl.Node{nodeWith("n1", projectctl.StateReady, "frontier")}, &fakeDispatcher{})
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatal("主界面按 Esc 退出了程序")
		}
	}
	if after := next.(Model); !strings.Contains(after.notice, "按 q") {
		t.Errorf("按了 Esc 没告诉人怎么退出：%q", after.notice)
	}
}

// 预览与详情里的 Esc 照旧只关掉它们，不退出。
func TestEscapeStillClosesPreviewAndDetail(t *testing.T) {
	m := modelWithDispatcher(t, []projectctl.Node{nodeWith("n1", projectctl.StateReady, "frontier")}, &fakeDispatcher{})
	m.preview, m.previewFrom = "画面", "sess"
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil || next.(Model).preview != "" {
		t.Error("预览里的 Esc 应当只关掉预览")
	}
	m = next.(Model)
	m.detail, m.detailOf = "全文", "n1"
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil || next.(Model).detail != "" {
		t.Error("详情里的 Esc 应当只收起详情")
	}
}

// 执行 brief 写明项目根、读合同与提交的完整命令（n047）。
func TestExecutorBriefNamesRootControllerAndActor(t *testing.T) {
	b := executorBrief(nodeWith("n1", projectctl.StateReady, "frontier"), 2, "/tmp/proj", "/opt/ctl/projectctl.py", "workdeck-m")
	for _, want := range []string{
		"项目根：/tmp/proj",
		"cd /tmp/proj && python3 /opt/ctl/projectctl.py brief n1",
		"cd /tmp/proj && python3 /opt/ctl/projectctl.py submit n1 --actor workdeck-m --summary",
		"read_scope",
		"先停下来问",
		"不要运行 review",
		"第 2 次 attempt",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("执行 brief 缺 %q：\n%s", want, b)
		}
	}
}

// 路径里有空格时命令仍然能直接粘贴运行；不知道的值留成占位，不编。
func TestExecutorBriefQuotesPathsAndMarksUnknowns(t *testing.T) {
	b := executorBrief(nodeWith("n1", projectctl.StateReady, "frontier"), 1, "/tmp/my proj", "/opt/c tl/p.py", "")
	for _, want := range []string{"cd '/tmp/my proj' && python3 '/opt/c tl/p.py' submit", "--actor <开工时的 actor>"} {
		if !strings.Contains(b, want) {
			t.Errorf("执行 brief 缺 %q：\n%s", want, b)
		}
	}
	b = executorBrief(nodeWith("n1", projectctl.StateReady, "frontier"), 1, "", "", "a")
	for _, want := range []string{"cd <项目根> && python3 <控制器脚本路径> brief n1"} {
		if !strings.Contains(b, want) {
			t.Errorf("缺值时没留占位 %q：\n%s", want, b)
		}
	}
}

// 新开工时 brief 里的 actor 就是这次 start 记进账本的 actor：照抄就能 submit。
func TestDispatchedBriefCarriesTheStartActor(t *testing.T) {
	fd := &fakeDispatcher{cands: []dispatch.Model{{ID: "m", Tool: "claude", Class: dispatch.Frontier}}}
	fs := &fakeStarter{}
	m := modelWithDispatcher(t, []projectctl.Node{nodeWith("n1", projectctl.StateReady, "frontier")}, fd)
	m = m.WithStarter(fs).WithScript("/opt/ctl/projectctl.py")
	m, cmd := enterWithReason(t, pressD(m), "唯一够档的")
	if cmd == nil {
		t.Fatal("应当派出")
	}
	cmd()
	if len(fd.got) != 1 || len(fs.got) != 1 {
		t.Fatalf("应当开工并派工各一次：派 %d，开工 %d", len(fd.got), len(fs.got))
	}
	brief := fd.got[0].MessageFor(1)
	want := "--actor " + fs.got[0].Actor + " "
	if fs.got[0].Actor == "" || !strings.Contains(brief, want) {
		t.Errorf("brief 的 actor 与开工 actor %q 不一致：\n%s", fs.got[0].Actor, brief)
	}
	if !strings.Contains(brief, "python3 /opt/ctl/projectctl.py submit n1") || !strings.Contains(brief, "cd /tmp/proj") {
		t.Errorf("brief 没写出控制器完整命令：\n%s", brief)
	}
}

// 已经开工的节点补派时不再开工，brief 用账本记着的执行者。
func TestRedispatchBriefCarriesTheLedgerActor(t *testing.T) {
	fd := &fakeDispatcher{cands: []dispatch.Model{{ID: "m", Tool: "claude", Class: dispatch.Frontier}}}
	n := runningNode("n1", 3)
	n.Actor = projectctl.Known("workdeck-earlier")
	m := modelWithDispatcher(t, []projectctl.Node{n}, fd).WithScript("/opt/ctl/projectctl.py")
	m, cmd := enterWithReason(t, pressD(m), "补派")
	if cmd == nil {
		t.Fatalf("running 节点应当能补派：%q", m.notice)
	}
	cmd()
	if len(fd.got) != 1 {
		t.Fatalf("应当派一次：%d", len(fd.got))
	}
	if brief := fd.got[0].MessageFor(3); !strings.Contains(brief, "--actor workdeck-earlier ") {
		t.Errorf("补派的 brief 没用账本里的执行者：\n%s", brief)
	}
}

// 评审 brief 禁止评审者自己记账（n047），且这句话不会被 v 当成结论。
func TestReviewBriefForbidsLedgerWrites(t *testing.T) {
	b := reviewBrief(submittedNode("n1", 2, "exec"), 2)
	for _, want := range []string{"不要运行 projectctl 的 review、submit", "owner 看过报告后自己记账"} {
		if !strings.Contains(b, want) {
			t.Errorf("评审 brief 缺 %q：\n%s", want, b)
		}
	}
	if v := lastVerdict(b); v != "" {
		t.Errorf("评审 brief 被认成结论 %q", v)
	}
}

// brief 里写出的命令要真的能被控制器接受：在夹具副本上对 running 的 f02 原样运行（真实 projectctl）。
// brief 命令须成功；submit 命令用账本里的执行者时，控制器不因 actor 拒绝——换一个 actor 则必须被拒（对照）。
func TestIntegrationBriefCommandsWorkAgainstTheRealController(t *testing.T) {
	script, err := projectctl.DefaultScript()
	if err != nil {
		t.Skipf("跳过：找不到 projectctl（%v）", err)
	}
	src, _ := filepath.Abs("../../testdata/workdeck-fixture")
	// 控制器要求目录名等于 project_id，空格放在上一级：命令里的路径照样要正确引用。
	parent := filepath.Join(t.TempDir(), "with space")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(parent, "workdeck-fixture")
	if out, err := exec.Command("cp", "-r", src, dst).CombinedOutput(); err != nil {
		t.Fatalf("复制夹具失败：%v %s", err, out)
	}
	node := nodeWith("f02_build_core", projectctl.StateRunning, "balanced")
	line := func(brief, verb string) string {
		for _, l := range strings.Split(brief, "\n") {
			if strings.Contains(l, " "+verb+" f02_build_core") {
				return strings.TrimSpace(l)
			}
		}
		t.Fatalf("brief 里没有 %s 命令：\n%s", verb, brief)
		return ""
	}
	run := func(cmdline string) string {
		out, _ := exec.Command("bash", "-c", cmdline).CombinedOutput()
		return string(out)
	}
	const refused = "only the current executor may submit"

	good := executorBrief(node, 2, dst, script, "fixture-executor-a1")
	if out := run(line(good, "brief")); !strings.Contains(out, "f02_build_core") || strings.Contains(out, "ERROR") {
		t.Errorf("brief 命令没跑通：\n%s", out)
	}
	// 对照先跑：正确的那条一提交，节点就不再 running。
	bad := executorBrief(node, 2, dst, script, "someone-else")
	if out := run(line(bad, "submit")); !strings.Contains(out, refused) {
		t.Errorf("对照失效：换了 actor 控制器也没拒：\n%s", out)
	}
	if out := run(line(good, "submit")); !strings.Contains(out, "'state': 'submitted'") {
		t.Errorf("照抄 brief 的 submit 命令没能提交：\n%s", out)
	}
}
