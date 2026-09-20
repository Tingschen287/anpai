package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

func init() {
	// 测试里比的是字，不是颜色。关掉配色，断言才稳。
	lipgloss.SetColorProfile(termenv.Ascii)
}

// sample 造一份内容齐全的快照：七个状态都有，预算有已知也有未知。
func sample() projectctl.Snapshot {
	node := func(id string, st projectctl.State) projectctl.Node {
		return projectctl.Node{
			ID: id, Title: id, RouteClass: "balanced", OwnerRole: "fixture_executor",
			State:    projectctl.Known(st),
			Attempt:  projectctl.Unknown[int]("没有 attempt"),
			Actor:    projectctl.Unknown[string]("没有执行者"),
			Elapsed:  projectctl.Unknown[time.Duration]("没有耗时"),
		}
	}
	running := node("n_running", projectctl.StateRunning)
	running.Attempt = projectctl.Known(2)
	running.Actor = projectctl.Known("executor-a1")
	running.Elapsed = projectctl.Known(95 * time.Minute)

	blocked := node("n_blocked", projectctl.StateBlocked)
	blocked.Reason = "外部接口没有凭据"
	blocked.RequiredAction = "owner 决定是否另开只读账号"

	return projectctl.Snapshot{
		ProjectID:     "workdeck-fixture",
		GraphRevision: projectctl.Known(3),
		Health:        projectctl.Known("blocked"),
		Nodes: []projectctl.Node{
			node("n_accepted", projectctl.StateAccepted),
			running,
			node("n_submitted", projectctl.StateSubmitted),
			blocked,
			node("n_failed", projectctl.StateFailed),
			node("n_ready", projectctl.StateReady),
			node("n_planned", projectctl.StatePlanned),
		},
		Budgets: []projectctl.BudgetLine{
			{Label: "worker rounds", Used: projectctl.Known(5.0), Limit: projectctl.Known(20.0), Unit: "轮"},
			{Label: "frontier calls", Used: projectctl.Known(0.0), Limit: projectctl.Known(8.0), Unit: "次"},
			{Label: "elapsed", Used: projectctl.Unknown[float64]("控制器不导出这一维度"), Limit: projectctl.Known(40.0), Unit: "小时"},
		},
		Chain: projectctl.ChainStatus{ChainOK: true, Events: 14},
		Fresh: projectctl.Freshness{Note: "状态投影与账本一致"},
	}
}

// 验收要的四类信息必须在同一屏里同时出现。
func TestRenderShowsAllFourKindsOfInformation(t *testing.T) {
	out := Render(sample(), 100)

	// 一、每个节点连同它的状态。
	for _, id := range []string{"n_accepted", "n_running", "n_submitted", "n_blocked", "n_failed", "n_ready", "n_planned"} {
		if !strings.Contains(out, id) {
			t.Errorf("一屏里没有节点 %s", id)
		}
	}
	for _, st := range []string{"accepted", "running", "submitted", "blocked", "failed", "ready", "planned"} {
		if !strings.Contains(out, st) {
			t.Errorf("一屏里没有状态 %s", st)
		}
	}

	// 二、运行中节点的 attempt 号与已耗时。
	if !strings.Contains(out, "attempt 2") {
		t.Error("没有显示运行中节点的 attempt 号")
	}
	if !strings.Contains(out, "1h35m") {
		t.Errorf("没有显示运行中节点的已耗时，实际输出：\n%s", out)
	}

	// 三、三个预算维度的已用与上限。
	for _, want := range []string{"worker rounds", "5 / 20", "frontier calls", "0 / 8", "elapsed", "40"} {
		if !strings.Contains(out, want) {
			t.Errorf("预算里缺 %q", want)
		}
	}

	// 四、等 owner 处理的条目，连同要他做什么。
	if !strings.Contains(out, "等 owner 处理") {
		t.Error("没有等 owner 的分区")
	}
	if !strings.Contains(out, "owner 决定是否另开只读账号") {
		t.Error("没有显示 blocked 节点要 owner 做什么")
	}
}

// 取不到的值必须写成未知，不许填 0，也不许留白。
func TestUnknownValuesRenderAsUnknownNotZero(t *testing.T) {
	s := sample()
	out := Render(s, 100)

	if !strings.Contains(out, "elapsed") {
		t.Fatal("预算里没有 elapsed 这一行")
	}
	// elapsed 那一行必须写着未知，并且不能是 0。
	var line string
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "elapsed") && strings.Contains(ln, "/") {
			line = ln
			break
		}
	}
	if line == "" {
		t.Fatal("找不到 elapsed 的预算行")
	}
	if !strings.Contains(line, "未知") {
		t.Errorf("elapsed 已用量没写成未知：%q", line)
	}
	if strings.Contains(line, "0 / 40") {
		t.Errorf("elapsed 已用量被填成了 0，这是编出来的数：%q", line)
	}
	if !strings.Contains(out, "控制器不导出这一维度") {
		t.Error("没有说明 elapsed 为什么是未知")
	}
}

func TestStateUnknownRendersReason(t *testing.T) {
	s := sample()
	s.Nodes[0].State = projectctl.Unknown[projectctl.State]("控制器没答复")
	out := Render(s, 100)
	if !strings.Contains(out, "未知") {
		t.Error("状态未知的节点没有标成未知")
	}
	if !strings.Contains(out, "控制器没答复") {
		t.Error("状态未知的节点没有给出原因")
	}
	if !strings.Contains(out, "状态未知 1") {
		t.Error("统计里没有把状态未知的节点单列")
	}
}

func TestStaleProjectionIsCalledOutOnScreen(t *testing.T) {
	s := sample()
	s.Fresh = projectctl.Freshness{Stale: true, Note: "投影停在昨天"}
	out := Render(s, 100)
	if !strings.Contains(out, "投影已过期") {
		t.Error("投影过期时没有在屏幕上说出来")
	}
	if !strings.Contains(out, "投影停在昨天") {
		t.Error("投影过期时没有给出说明")
	}
}

// 界面要在自己的说明行里讲清楚能做什么、不能做什么。
//
// 早先这条测试查的是整屏里有没有「只读」两个字，结果被夹具数据喂饱了：
// blocked 节点的 owner 动作里恰好写着「另开只读账号」，于是界面说明改掉之后
// 它照样通过。现在只认底部那一行确切的说明，不认满屏碰运气的字符串。
func TestFooterStatesWhatTheScreenCanAndCannotDo(t *testing.T) {
	out := Render(sample(), 100)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	footer := lines[len(lines)-1]

	// 能做的：选节点、派工、刷新、退出。
	for _, want := range []string{"d 派工", "r 刷新", "q 退出"} {
		if !strings.Contains(footer, want) {
			t.Errorf("说明行里没讲 %q：%q", want, footer)
		}
	}
	// 不能做的，必须明说，不能靠人猜。
	for _, want := range []string{"不接管终端", "不写 Graph 账本"} {
		if !strings.Contains(footer, want) {
			t.Errorf("说明行里没讲清楚边界 %q：%q", want, footer)
		}
	}
}

// 界面不该声称自己做了本轮做不到的事。
func TestScreenNeverClaimsToAttachOrWriteLedger(t *testing.T) {
	out := Render(sample(), 100)
	for _, forbidden := range []string{"已接管", "已发送", "写入账本", "已验收"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("界面上出现了本轮做不到的动作字样：%s", forbidden)
		}
	}
}

// 一份什么都没取到的快照也要画得出来，不能崩。
func TestRenderSurvivesAnEmptySnapshot(t *testing.T) {
	var empty projectctl.Snapshot
	empty.Problems = []string{"读不到 work-graph.yaml"}
	out := Render(empty, 80)
	if !strings.Contains(out, "未知项目") {
		t.Error("空快照没有标出项目未知")
	}
	if !strings.Contains(out, "读不到 work-graph.yaml") {
		t.Error("空快照没有把问题显示出来")
	}
}

func TestDurationTextNeverInventsZero(t *testing.T) {
	if got := durationText(projectctl.Unknown[time.Duration]("没取到")); got != "未知" {
		t.Errorf("耗时未知时显示成 %q，期望「未知」", got)
	}
	if got := durationText(projectctl.Known(95 * time.Minute)); got != "1h35m" {
		t.Errorf("95 分钟显示成 %q，期望 1h35m", got)
	}
	if got := durationText(projectctl.Known(45 * time.Second)); got != "45s" {
		t.Errorf("45 秒显示成 %q，期望 45s", got)
	}
}
