package panels

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/deckd"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/wirecheck"
)

func fix3Store(t *testing.T) (*deckstore.Store, deckstore.Dirs) {
	t.Helper()
	root := t.TempDir()
	d := deckstore.Dirs{Config: filepath.Join(root, "cfg"), State: filepath.Join(root, "state")}
	s, err := deckstore.Open(d)
	if err != nil {
		t.Fatal(err)
	}
	set := s.Settings()
	set.Projects = append(set.Projects, deckstore.ProjectDef{ID: "p", Name: "p", Path: "/tmp/p", Agents: []string{"claude"}, Envelope: deckstore.DefaultEnvelope("p")})
	if err := s.ReplaceSettings(set); err != nil {
		t.Fatal(err)
	}
	return s, d
}

// 返工 3 任务 1：给 owner 看的每个出口都把中性标记按当前语言换成记号。
// 中文逐字等于 63f6247 的「[已打码]」，英文是「[redacted]」，画面里不留私用区字符、不留假 token。
func TestN060PanelExitsTranslateTheNeutralMark(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	s, d := fix3Store(t)
	if _, err := s.CreateDecision(deckstore.NewDecision{Project: "p", Kind: deckstore.KindCost, Summary: "ship sk-FAKETOKEN99", Options: []string{"go sk-FAKETOKEN99"}, Recommend: "go sk-FAKETOKEN99", CreatedBy: "ses-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Notify("p", "report", "plain sk-FAKETOKEN99 text", "deckd"); err != nil {
		t.Fatal(err)
	}
	wirecheck.External("upstream literal [已打码] end")
	if _, err := s.NotifyCoded("p", "report", i18n.E("err.deckd.serverrunspawn.47", "upstream literal [已打码] end"), "deckd"); err != nil {
		t.Fatal(err)
	}
	s2, err := deckstore.Open(d)
	if err != nil {
		t.Fatal(err)
	}
	decs, ns := s2.Decisions("p"), s2.Notifications(0, "")
	sessions := []deckstore.Session{
		{Project: "p", Role: "sub", Title: deckstore.Clean("task sk-FAKETOKEN99", 80), Agent: "claude", Status: deckstore.StatusRunning},
		{Project: "p", Role: "main", Title: "主 agent · p", TitleCode: "err.deckd.serverpostmain.4", TitleArgs: i18n.Args{"p"}, Agent: "claude", Status: deckstore.StatusRunning},
	}
	for _, c := range []struct {
		lang, mark string
		main       string
	}{{i18n.ZH, "[已打码]", "主 agent · p"}, {i18n.EN, "[redacted]", "main agent: p"}} {
		i18n.SetLang(c.lang)
		shown := map[string]string{
			"决策面板": ansi.Strip(RenderDecisions(decs, 0, "")),
			"通知":   ansi.Strip(RenderNotifications(ns, 10)),
			"活动面板": ansi.Strip(RenderActivity(sessions, time.Now())),
		}
		for name, out := range shown {
			if strings.ContainsAny(out, i18n.RedactMark) || strings.Contains(out, "sk-FAKE") {
				t.Fatalf("%s %s：不应留下标记字符或假 token：%s", c.lang, name, out)
			}
		}
		other := "[redacted]"
		if c.lang == i18n.EN {
			other = "[已打码]"
		}
		dec := shown["决策面板"]
		for _, want := range []string{"ship " + c.mark, "go " + c.mark} {
			if !strings.Contains(dec, want) {
				t.Fatalf("%s 决策面板应有 %q：\n%s", c.lang, want, dec)
			}
		}
		if strings.Contains(dec, other) {
			t.Fatalf("%s 决策面板不该出现另一种语言的记号 %q：\n%s", c.lang, other, dec)
		}
		note := shown["通知"]
		if !strings.Contains(note, "plain "+c.mark+" text") {
			t.Fatalf("%s 无码通知正文应是 %q：\n%s", c.lang, c.mark, note)
		}
		// 外部原文里字面的 [已打码] 中英文都原样显示，不被改写
		if !strings.Contains(note, "literal [已打码] end") {
			t.Fatalf("%s 外部原文里字面的记号要原样：\n%s", c.lang, note)
		}
		act := shown["活动面板"]
		if !strings.Contains(act, "task "+c.mark) || !strings.Contains(act, c.main) {
			t.Fatalf("%s 活动面板的会话标题：\n%s", c.lang, act)
		}
	}
}

// 带 ANSI、超长的标题和通知不回退：清洗规则还是那几条。
func TestN060PanelCleaningDidNotRegress(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	long := strings.Repeat("长", 900)
	out := ansi.Strip(RenderActivity([]deckstore.Session{{Project: "p", Role: "sub", Title: "\x1b[31mRED\x1b[0m sk-FAKETOKEN99 " + long, Agent: "claude", Status: deckstore.StatusRunning}}, time.Now()))
	if strings.Contains(out, "sk-FAKE") || strings.Contains(out, "\x1b") || strings.Contains(out, long) || !strings.Contains(out, "RED") || !strings.Contains(out, "[已打码]") {
		t.Fatalf("标题没清干净：%q", out)
	}
}

// 进度面板：预算单位和取数问题都不是本地中文。中文逐字不变，英文按码翻译。
func TestN060ProgressPanelUnitsAndProblems(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	used, limit := 3.0, 10.0
	p := deckd.Progress{
		Project: "p", Graph: true, Counts: map[string]int{"running": 1},
		Budgets: []deckd.BudgetView{
			{Label: "worker rounds", Used: &used, Limit: &limit, Unit: "轮", UnitKey: "round"},
			{Label: "frontier calls", Limit: &limit, Unit: "次", UnitKey: "call"},
			{Label: "elapsed", Limit: &limit, Unit: "小时", UnitKey: "hour"},
			{Label: "tokens", Used: &used, Limit: &limit},
		},
		Problems:      []string{"读不到 /x/work-graph.yaml：open /x/work-graph.yaml: no such file or directory", "外部原文的一条"},
		ProblemsCoded: []i18n.Nest{{Code: "err.projectctl.graph.read", Args: []any{"/x/work-graph.yaml", "open /x/work-graph.yaml: no such file or directory"}, Error: "读不到 /x/work-graph.yaml：open /x/work-graph.yaml: no such file or directory"}, {}},
	}
	zh := ansi.Strip(RenderProgress(p))
	for _, want := range []string{"worker rounds 3 / 10轮", "frontier calls 未知 / 10次", "elapsed 未知 / 10小时", "tokens 3 / 10", "! 读不到 /x/work-graph.yaml：open", "! 外部原文的一条"} {
		if !strings.Contains(zh, want) {
			t.Fatalf("中文进度面板少了 %q：\n%s", want, zh)
		}
	}
	i18n.SetLang(i18n.EN)
	en := ansi.Strip(RenderProgress(p))
	for _, want := range []string{"worker rounds 3 / 10 rounds", "frontier calls unknown / 10 calls", "elapsed unknown / 10 h", "! cannot read /x/work-graph.yaml: open"} {
		if !strings.Contains(en, want) {
			t.Fatalf("英文进度面板少了 %q：\n%s", want, en)
		}
	}
	if strings.Contains(strings.ReplaceAll(en, "外部原文的一条", ""), "读") || strings.ContainsAny(en, "轮次") {
		t.Fatalf("英文进度面板不该留下本地中文：\n%s", en)
	}
}
