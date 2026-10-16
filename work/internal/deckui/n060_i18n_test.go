package deckui

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/dashquota"
	"github.com/Tingschen287/terminal-deck/work/internal/deckd"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/layout"
	"github.com/Tingschen287/terminal-deck/work/internal/panels"
	"github.com/Tingschen287/terminal-deck/work/internal/viewstate"
)

// coveredDirs 是本段收进文字表的包。
// 不扫描：internal/i18n（文字表本身）、*_test.go、注释、
// internal/ui（旧的 Work Deck 界面）、deckd / deckstore / agentdeck 的报错（第 2 段）、
// cmd/deck 的命令行输出（不在本节点；空闲页已改走 IdleLines）。
func coveredDirs() []string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("caller")
	}
	root := filepath.Dir(filepath.Dir(file))
	var out []string
	for _, name := range []string{"deckui", "panels", "layout", "logo"} {
		out = append(out, filepath.Join(root, name))
	}
	return out
}

// strayAllow 是允许留下的中文字面量。键是字面量内容，值是理由。现在没有。
var strayAllow = map[string]string{}

// dynamicKeys 是代码里拼出来的键，字符串字面量扫不到。
// tier.frontier / tier.balanced / tier.economy：settings.go 的 tierName 用 "tier." + 档位名。
// tier.unset 是 T("tier.unset")，扫得到。
var dynamicKeys = []string{"tier.frontier", "tier.balanced", "tier.economy"}

func walkCovered(t *testing.T, fn func(path string, f *ast.File, fset *token.FileSet)) {
	t.Helper()
	fset := token.NewFileSet()
	for _, dir := range coveredDirs() {
		ms, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range ms {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			fn(path, f, fset)
		}
	}
}

func isTextCall(n *ast.CallExpr) (name, key string, argc int, literal bool) {
	switch fun := n.Fun.(type) {
	case *ast.Ident:
		if fun.Name == "T" {
			name = "T"
		}
	case *ast.SelectorExpr:
		id, ok := fun.X.(*ast.Ident)
		if ok && id.Name == "i18n" && (fun.Sel.Name == "T" || fun.Sel.Name == "Errorf") {
			name = "i18n." + fun.Sel.Name
		}
	}
	if name == "" || len(n.Args) == 0 || n.Ellipsis.IsValid() {
		return "", "", 0, false
	}
	lit, ok := n.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return name, "", len(n.Args) - 1, false
	}
	k, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", "", 0, false
	}
	return name, k, len(n.Args) - 1, true
}

func checkNoStrayChinese(t *testing.T) {
	t.Helper()
	used := map[string]bool{}
	for _, k := range dynamicKeys {
		used[k] = true
	}
	walkCovered(t, func(path string, f *ast.File, fset *token.FileSet) {
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if cjk(v) {
				if why := strayAllow[v]; why == "" {
					t.Errorf("%s 里有中文字面量 %q：放进 internal/i18n", fset.Position(lit.Pos()), v)
				}
			}
			if strings.Contains(v, ".") {
				if _, ok := i18n.TextZH(v); ok {
					used[v] = true
				}
			}
			return true
		})
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			_, key, _, literal := isTextCall(call)
			if literal {
				used[key] = true
			}
			return true
		})
	})
	for k := range used {
		if _, ok := i18n.TextZH(k); !ok {
			t.Errorf("文字表里没有 %q", k)
		}
		if _, ok := i18n.TextEN(k); !ok {
			t.Errorf("英文没有 %q", k)
		}
	}
	for _, k := range i18n.Keys() {
		if !used[k] {
			t.Errorf("文字表里的 %q 没人用", k)
		}
	}
}

var verbRe = regexp.MustCompile(`%[-+# 0]*[0-9]*(\.[0-9]+)?[a-zA-Z]`)

func checkFormatArgs(t *testing.T) {
	t.Helper()
	verbs := func(s string) []string {
		return verbRe.FindAllString(strings.ReplaceAll(s, "%%", ""), -1)
	}
	walkCovered(t, func(_ string, f *ast.File, fset *token.FileSet) {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, key, argc, literal := isTextCall(call)
			if !literal {
				return true
			}
			zh, ok := i18n.TextZH(key)
			en, enOK := i18n.TextEN(key)
			if !ok || !enOK {
				t.Errorf("%s：%s(%q) 不在文字表里", fset.Position(call.Pos()), name, key)
				return true
			}
			if vz, ve := verbs(zh), verbs(en); strings.Join(vz, ",") != strings.Join(ve, ",") {
				t.Errorf("%s：%q 中英占位符不一致 %v / %v", fset.Position(call.Pos()), key, vz, ve)
			}
			if got, want := argc, len(verbs(zh)); got != want {
				t.Errorf("%s：%s(%q) 给了 %d 个参数，文字里有 %d 个占位符", fset.Position(call.Pos()), name, key, got, want)
			}
			return true
		})
	})
}

func cjkRune(s string) (rune, string) {
	rs := []rune(s)
	for i, r := range rs {
		if unicode.Is(unicode.Han, r) || (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF) {
			a, b := i-12, i+12
			if a < 0 {
				a = 0
			}
			if b > len(rs) {
				b = len(rs)
			}
			return r, string(rs[a:b])
		}
	}
	return 0, ""
}

func addView(t *testing.T, views *[]string, name, s string) {
	t.Helper()
	if r, snip := cjkRune(s); r != 0 {
		t.Errorf("%s 含 %q，附近 %q", name, string(r), snip)
	}
	*views = append(*views, name)
}

// TestEnglishUIHasNoHan 在英文下渲染本段的主要画面。
// 英文产品名一律 Anpai，不出现「安排」。
// 「安排」只允许出现在中文界面、而且 BrandFor 的列数 ≥ 10 的时候（n058）。本测试是英文，所以一处都不允许。
// 会话标题、项目名、模型 id、决定摘要、通知正文、从外面传进来的报错保持原样，夹具里这些都用英文，避免和界面文字混在一起。
func TestEnglishUIHasNoHan(t *testing.T) {
	i18n.SetLang(i18n.EN)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })

	var got []string
	keep := func(name, s string) { addView(t, &got, name, ansi.Strip(s)) }

	sb := &Sidebar{Data: englishData(), Mode: "none"}
	f := sb.Render(80, 80)
	keep("sidebar", strings.Join(f.Lines, "\n"))

	wide, narrow := IdleLines(80), IdleLines(8)
	if wide[0] != "Anpai" || narrow[0] != "Anpai" {
		t.Fatalf("英文品牌一律 Anpai：宽 %q 窄 %q", wide[0], narrow[0])
	}
	keep("idle-wide", strings.Join(wide, "\n"))
	keep("idle-narrow", strings.Join(narrow, "\n"))

	base := englishData()
	keep("tabs", (&Tabs{Data: base}).Render(80).Lines[0])
	keep("tabs-taken", (&Tabs{Data: base}).Hint())
	pend := base
	pend.View.Focus = "s2"
	keep("tabs-pending", (&Tabs{Data: pend}).Hint())
	ro := base
	ro.View.Open = []string{"s5"}
	ro.View.Focus = "s5"
	ro.View.TakenOver = nil
	ro.View.HandbackPending = nil
	keep("tabs-readonly", (&Tabs{Data: ro}).Hint())
	empty := base
	empty.View.Open = nil
	empty.View.Focus = ""
	keep("tabs-empty", (&Tabs{Data: empty}).Hint())

	m, api := n057Fixture()
	m.width, m.height = 100, 40
	for _, p := range []page{pageProject, pageAgents, pageTrust} {
		if p == pageTrust {
			m.trust = []deckstore.TrustGrant{{Project: "demo", Tool: "codex"}}
		}
		m.switchPage(p)
		m.cur = 0
		keep("settings-"+string(rune('1'+int(p))), m.View())
	}
	m.openAgent("claude")
	keep("sub-agent", m.View())
	m.tools = nil
	m.openAdd()
	keep("sub-add-loading", m.View())
	m.tools = api.tools
	keep("sub-add", m.View())
	m.sub = nil
	m.openTier(deckstore.Frontier)
	keep("sub-tier", m.View())
	m.sub = nil
	m.form = &projectForm{dir: "/work/demo", subdirs: []string{"src"}, id: "demo"}
	keep("form-browse", m.View())
	m.form.chosen = "/work/demo"
	keep("form-confirm", m.View())

	now := time.Date(2026, 10, 14, 15, 0, 0, 0, time.UTC)
	qs := []dashquota.ToolQuota{
		{Tool: "codex", Status: dashquota.Exhausted, Windows: []dashquota.Window{
			{Key: "5h", Pct: 92, Reset: now.Add(time.Hour)},
			{Key: "week", Pct: 8, Reset: now.Add(48 * time.Hour)}}},
		{Tool: "opencode", Status: dashquota.Warn, Windows: []dashquota.Window{{Key: "day", Pct: 50, Reset: now.Add(-time.Hour)}}},
	}
	lines, _ := panels.QuotaLines(qs, 60, "none")
	keep("quota", strings.Join(lines, "\n"))
	lines, _ = panels.QuotaLines(nil, 60, "none")
	keep("quota-empty", strings.Join(lines, "\n"))
	keep("quota-err", strings.Join(QuotaFrame(nil, errors.New("disk"), 40, 10, "none", func(string, int) string { return "" }).Lines, "\n"))

	var decs []deckstore.Decision
	for i, kind := range []string{deckstore.KindCost, deckstore.KindConfig, deckstore.KindPush, deckstore.KindDisagree, deckstore.KindDeviation, deckstore.KindCodexTrust, deckstore.KindGraphGate, deckstore.KindSessionWait} {
		d := deckstore.Decision{ID: kind, Project: "demo", Kind: kind, Summary: "ship it", State: deckstore.StateOpen, CreatedAt: now.Add(time.Duration(i) * time.Second)}
		if i == 0 {
			d.Options = []string{deckstore.OptApprove, deckstore.OptReject, deckstore.OptStop}
			d.Recommend = deckstore.OptApprove
		}
		decs = append(decs, d)
	}
	keep("decisions", panels.RenderDecisions(decs, 0, "noted"))

	one := 1.0
	keep("progress", panels.RenderProgress(deckd.Progress{
		Project: "demo", Graph: true, Unknown: 2,
		Counts:   map[string]int{"accepted": 1, "running": 1, "submitted": 1, "ready": 1, "rework": 1, "blocked": 1, "failed": 1, "planned": 1, "superseded": 1},
		Budgets:  []deckd.BudgetView{{Label: "frontier", Used: &one, Limit: &one, Unit: "h"}, {Label: "elapsed", Limit: &one, Unit: "h"}},
		Problems: []string{"late"},
	}))
	keep("progress-nograph", panels.RenderProgress(deckd.Progress{Project: "demo"}))
	keep("notify-empty", panels.RenderNotifications(nil, 10))
	keep("notify", panels.RenderNotifications([]deckstore.Notification{{At: now, Project: "demo", Text: "hello", Kind: "info"}}, 10))
	var sessions []deckstore.Session
	for _, st := range []string{deckstore.StatusLaunching, deckstore.StatusRunning, deckstore.StatusUncertain, deckstore.StatusDone, deckstore.StatusFailed, deckstore.StatusStopped} {
		role := "sub"
		if st == deckstore.StatusRunning {
			role = "main"
		}
		sessions = append(sessions, deckstore.Session{Project: "demo", Role: role, Title: "Work", Agent: "claude", Status: st})
	}
	keep("activity", panels.RenderActivity(sessions, now))
	keep("activity-empty", panels.RenderActivity(nil, now))

	keep("layout-base", layout.BaseConf(layout.Spec{}))
	keep("layout-keys", layout.BindingsConf(layout.Spec{Deck: "/opt/deck"}, layout.Built{Center: "%1", Tabs: "%7"}))
	keep("layout-err", i18n.Errorf("layout.err.restore", "Tab bar", errors.New("busy")).Error())

	if len(got) < 20 {
		t.Fatalf("画面太少：%d", len(got))
	}
}

func englishData() Data {
	t0 := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	s := func(key, id, project, role, title, tool, status, act string) SessionInfo {
		return SessionInfo{Session: deckstore.Session{Key: key, AgentDeckID: id, TmuxSession: "agentdeck_" + id, Project: project,
			Role: role, Title: title, Tool: tool, Status: status, CreatedAt: t0}, Activity: act}
	}
	return Data{
		Projects: []deckstore.ProjectDef{{ID: "empty", Name: "Empty"}, {ID: "demo", Name: "Demo"}},
		Sessions: []SessionInfo{
			s("k1", "m1", "demo", "main", "Main", "claude", deckstore.StatusRunning, "waiting"),
			s("k2", "s1", "demo", "sub", "Write", "codex", deckstore.StatusRunning, "running"),
			s("k3", "s2", "demo", "sub", "Pend", "grok", deckstore.StatusRunning, "idle"),
			s("k4", "", "demo", "sub", "Launch", "kimi", deckstore.StatusLaunching, ""),
			s("k5", "s5", "demo", "sub", "Fail", "opencode", deckstore.StatusFailed, "error"),
			s("k6", "s6", "demo", "sub", "Stop", "antigravity", deckstore.StatusStopped, ""),
			s("k7", "s7", "demo", "sub", "Unsure", "claude", deckstore.StatusUncertain, ""),
			s("k8", "s8", "demo", "sub", "Done", "codex", deckstore.StatusDone, ""),
		},
		View: viewstate.State{Open: []string{"m1", "s1", "s2"}, Focus: "s1", TakenOver: []string{"s1", "s2"}, HandbackPending: []string{"s2"}},
	}
}
