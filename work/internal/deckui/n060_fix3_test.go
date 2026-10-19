package deckui

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 返工 3 任务 1：界面的出口都把中性打码标记按当前语言翻译。
// 中文逐字等于 63f6247 的「[已打码]」；英文是「[redacted]」；字面的 [已打码]（外部原文）中英文都原样。
func TestN060UIExitsTranslateTheNeutralMark(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	mark := i18n.RedactMark
	for _, c := range []struct{ lang, want string }{{i18n.ZH, "[已打码]"}, {i18n.EN, "[redacted]"}} {
		i18n.SetLang(c.lang)
		// 界面错误提示（deckui/run.go 的 errText）：无码错误是外部原文，现打码；HTTP 错误带着 deckd 线路上的标记
		if got := errText(errors.New("upstream sk-FAKETOKEN99")); strings.Contains(got, "sk-FAKE") || !strings.HasSuffix(got, "upstream "+c.want) {
			t.Fatalf("%s errText 应以 %q 收尾：%q", c.lang, c.want, got)
		}
		httpErr := &deckclient.Error{Status: 502, Message: "upstream " + mark}
		got := errText(httpErr)
		if strings.ContainsAny(got, mark) || !strings.HasSuffix(got, "upstream "+c.want) {
			t.Fatalf("%s HTTP 错误应显示 %q：%q", c.lang, c.want, got)
		}
		if c.lang == i18n.EN && (strings.Contains(got, "：") || !strings.HasPrefix(got, "deckd 502: ")) {
			t.Fatalf("英文里无码的 HTTP 错误也要翻前缀：%q", got)
		}
		// 侧栏、标签条的会话标题
		if got := sanitize("task " + mark); got != "task "+c.want {
			t.Fatalf("%s sanitize：%q", c.lang, got)
		}
		d := Data{
			Projects: []deckstore.ProjectDef{{ID: "p", Name: "p"}},
			Sessions: []SessionInfo{
				{Session: deckstore.Session{Key: "k1", Project: "p", Role: "sub", Title: "task " + mark, Tool: "claude", AgentDeckID: "a1", TmuxSession: "t1", Status: deckstore.StatusRunning}},
				{Session: deckstore.Session{Key: "k2", Project: "p", Role: "main", Title: "主 agent · p", TitleCode: "err.deckd.serverpostmain.4", TitleArgs: i18n.Args{"p"}, Tool: "claude", AgentDeckID: "a2", TmuxSession: "t2", Status: deckstore.StatusRunning}},
			},
		}
		d.View.Open = []string{"a1", "a2"}
		side := ansi.Strip(strings.Join((&Sidebar{Data: d, Mode: "none"}).Render(60, 30).Lines, "\n"))
		tabs := ansi.Strip(strings.Join((&Tabs{Data: d}).Render(80).Lines, "\n"))
		mainTitle := "主 agent · p"
		if c.lang == i18n.EN {
			mainTitle = "main agent: p"
		}
		for name, out := range map[string]string{"侧栏": side, "标签条": tabs} {
			if strings.ContainsAny(out, mark) {
				t.Fatalf("%s %s 不应有标记字符：%q", c.lang, name, out)
			}
		}
		if !strings.Contains(side, "task "+c.want) || !strings.Contains(side, mainTitle) {
			t.Fatalf("%s 侧栏：\n%s", c.lang, side)
		}
		if c.lang == i18n.EN && strings.ContainsAny(side, "主") {
			t.Fatalf("英文侧栏不应留下中文标题：\n%s", side)
		}
	}
}

// uiCleanCalls 找出文件里直接调 deckstore.Clean / deckstore.Tidy 的地方，返回「函数」标签。
func uiCleanCalls(f *ast.File) []string {
	var out []string
	walkStack(f, func(n ast.Node, stack []ast.Node) {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "Clean" && sel.Sel.Name != "Tidy") {
			return
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == "deckstore" {
			out = append(out, ownerFuncLabel(stack))
		}
	})
	return out
}

// uiCleanAllow 是界面包里还直接调 deckstore.Clean 的地方：键是「文件 · 函数」，值是理由。
var uiCleanAllow = map[string]string{
	"up.go · center": "把 tmux 的 stderr 末行清洗后接进 client 退出的错误；这是第一次清洗（外部原文），显示出口是 layout/center.go 的 Present",
}

// 界面（deckui、panels、layout、cmd/deck）显示文字时一律走 deckstore.Display，让中性打码标记按当前语言翻译；
// 不能直接调 Clean / Tidy 把标记留在画面上。新增出口忘了翻译，这个测试会失败。
func TestN060UIPackagesShowTextThroughDisplay(t *testing.T) {
	root := moduleRoot()
	var files []string
	for _, d := range []string{"internal/deckui", "internal/panels", "internal/layout", "cmd/deck"} {
		ms, err := filepath.Glob(filepath.Join(root, d, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, ms...)
	}
	fset := token.NewFileSet()
	hits := map[string]bool{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, fn := range uiCleanCalls(f) {
			site := filepath.Base(path) + " · " + fn
			hits[site] = true
			if uiCleanAllow[site] == "" {
				t.Errorf("%s 直接调了 deckstore.Clean / Tidy：显示文字要走 deckstore.Display", site)
			}
		}
	}
	for site, why := range uiCleanAllow {
		if why == "" || !hits[site] {
			t.Errorf("%s 的例外没有理由或已过期", site)
		}
	}
	// 反例：种一个直接调 Clean 的函数，必须被抓到
	src := "package x\nimport \"deckstore\"\nfunc planted(s string) string { return deckstore.Clean(s, 80) }\n"
	pf, err := parser.ParseFile(token.NewFileSet(), "planted.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := uiCleanCalls(pf); len(got) != 1 || got[0] != "planted" {
		t.Fatalf("反例没被抓到：%v", got)
	}
}
