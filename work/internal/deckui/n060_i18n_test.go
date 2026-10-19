package deckui

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
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

// dynamicTextKeys 是运行时才取到、再传给 T 的键。
// 名单来自生产代码里的表，不是另写一份：
// 面板的 map（panels.TextKeys）、布局按键行（layout.TextKeys）、设置页签（TextKeys）、档位 "tier." + deckstore.Tiers。
// tier.unset 是 T("tier.unset")，扫得到，不在这里。
func dynamicTextKeys() []string {
	out := make([]string, 0, len(pageText)+len(deckstore.Tiers))
	out = append(out, TextKeys()...)
	for _, tier := range deckstore.Tiers {
		out = append(out, "tier."+string(tier))
	}
	out = append(out, panels.TextKeys()...)
	out = append(out, layout.TextKeys()...)
	return out
}

// dynamicCallAllow 是放行清单。粒度是「文件 · 函数 · 参数表达式原文」。
// 原文用 go/printer 打印。改一个字符就对不上，测试失败，要人工复核后再改清单。
// 入口按 i18n 包里 T、Tf 两个函数对象认，不看导入名，也不看是选择器还是点导入后的裸名字。
// 这两个函数的标识符必须处在直接调用的函数位置。拿去当值用就报位置。
// 按对象找到的直接调用（含字面量键）少于此清单的条数，就当成入口坏了，不能静默通过。
// 清单不是放行依据。依据是 go/types 的符号绑定，坏绑定写进清单也过不了：
//   - 表[下标]、表[下标].字段：被下标的标识符必须绑定到包级变量（Parent 是包作用域），
//     并且就是本包 TextKeys 枚举到的那张表。按对象比对，不按名字。字段也按对象比对。
//   - "tier." + string(参数)：string 必须是 Universe 里的预声明类型。参数类型先 Unalias，再认 deckstore.Tier。
//     前缀另由 checkTierConcat 锁成 tier.。
//
// 函数调用、局部变量、参数、闭包参数、接收者、字段、未登记拼接、被遮蔽的名字，一律拒绝。
var dynamicCallAllow = []string{
	"layout.go · Keys · layoutKeyRows[i].text",
	"render.go · RenderActivity · activityStatusKey[x.Status]",
	"render.go · RenderProgress · progressCountKey[k]",
	"render.go · kindLabel · kindKey[k]",
	"render.go · label · optionKey[opt]",
	"settings.go · tabBar · pageText[i]",
	"settings.go · tierName · \"tier.\" + string(t)",
}

var keyShape = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z0-9]+)+$`)

type parsedFile struct {
	path string
	file *ast.File
}

// parseCovered 解析本段收进文字表的包。src 的键是绝对路径，值是整文件源码，用来在内存里替换。
// 没列进 src 的文件从磁盘读。不写盘。
func parseCovered(src map[string]string) (*token.FileSet, []parsedFile, error) {
	fset := token.NewFileSet()
	files, err := parseCoveredInto(fset, src)
	if err != nil {
		return nil, nil, err
	}
	return fset, files, nil
}

func parseCoveredInto(fset *token.FileSet, src map[string]string) ([]parsedFile, error) {
	var out []parsedFile
	for _, dir := range coveredDirs() {
		ms, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			return nil, err
		}
		for _, path := range ms {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			var body any
			if s, ok := src[path]; ok {
				body = s
			}
			f, err := parser.ParseFile(fset, path, body, 0)
			if err != nil {
				return nil, fmt.Errorf("解析 %s：%w", path, err)
			}
			out = append(out, parsedFile{path: path, file: f})
		}
	}
	return out, nil
}

func walkCovered(t *testing.T, fn func(path string, f *ast.File, fset *token.FileSet)) {
	t.Helper()
	fset, files, err := parseCovered(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, pf := range files {
		fn(pf.path, pf.file, fset)
	}
}

// isTextCall 只给字面量扫描用（中文、占位符、落单键、档位前缀）。它按名字认。
// 动态取键的入口不走这里，见 bindingProblems。
func isTextCall(n *ast.CallExpr) (name, key string, argc int, literal bool) {
	switch fun := n.Fun.(type) {
	case *ast.Ident:
		if fun.Name == "T" || fun.Name == "Tf" {
			name = fun.Name
		}
	case *ast.SelectorExpr:
		id, ok := fun.X.(*ast.Ident)
		if ok && id.Name == "i18n" && (fun.Sel.Name == "T" || fun.Sel.Name == "Tf" || fun.Sel.Name == "Format" || fun.Sel.Name == "Errorf") {
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
	for _, k := range dynamicTextKeys() {
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

// absentTextKeys 返回不在中文表或英文表里的键。正例和反例走同一条。
func absentTextKeys(keys []string) []string {
	var miss []string
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			continue
		}
		seen[k] = true
		_, zhOK := i18n.TextZH(k)
		_, enOK := i18n.TextEN(k)
		if !zhOK || !enOK {
			miss = append(miss, k)
		}
	}
	sort.Strings(miss)
	return miss
}

// TestDynamicKeysInBothTables 核对动态键都在中英两张表里。
// 不在 T 里对缺键 panic：缺键仍要原样返回键名，已有测试盖住这条。
// 拼错的键留在 map 里时，扫描器以前直接跳过。现在键从表里枚举出来，这里失败。
func TestDynamicKeysInBothTables(t *testing.T) {
	keys := dynamicTextKeys()
	if len(keys) == 0 {
		t.Fatal("动态键集合是空的")
	}
	if miss := absentTextKeys(keys); len(miss) > 0 {
		t.Fatalf("动态键不在中英文字表里：%s", strings.Join(miss, ", "))
	}
	catalog := map[string]bool{}
	for _, k := range keys {
		catalog[k] = true
	}
	checkOrphanKeyLiterals(t, catalog)
	checkDynamicCallSites(t)
	checkTierConcat(t, catalog)
	checkDynamicKeysPlain(t, keys)
}

// TestDynamicKeyTypoFails 注入一个不存在的键。不改生产代码里的键。
// 每一条都先确认原键在可枚举集合里，再换成错键，同一条检查必须失败。
func TestDynamicKeyTypoFails(t *testing.T) {
	live := dynamicTextKeys()
	have := map[string]bool{}
	for _, k := range live {
		have[k] = true
	}
	cases := []struct{ orig, bad string }{
		{"side.status.launching", "side.status.typo"},
		{"progress.submitted", "progress.typo"},
		{"decide.kind.gate", "decide.kind.typo"},
		{"layout.key.f2", "layout.key.typo"},
		{"tier.balanced", "tier.typo"},
		{"page.agents", "page.typo"},
	}
	for _, tc := range cases {
		t.Run(tc.orig, func(t *testing.T) {
			if !have[tc.orig] {
				t.Fatalf("可枚举集合里没有 %s", tc.orig)
			}
			keys := append([]string(nil), live...)
			for i, k := range keys {
				if k == tc.orig {
					keys[i] = tc.bad
				}
			}
			if miss := absentTextKeys(keys); len(miss) == 0 {
				t.Fatalf("把 %s 换成 %s 后，动态键检查仍通过", tc.orig, tc.bad)
			}
		})
	}
}

// TestDynamicCallExprMutationsFail 读真实源码，在内存里改已放行的调用或键表。
// 检查器必须报错。不写回磁盘。改过的源码同样走 go/types。
func TestDynamicCallExprMutationsFail(t *testing.T) {
	clean, err := dynamicKeyProblems(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(clean) != 0 {
		t.Fatalf("未改动的源码不应报错：\n%s", strings.Join(clean, "\n"))
	}
	bases := []string{"render.go", "settings.go"}
	orig := map[string]string{}
	pathOf := map[string]string{}
	for _, base := range bases {
		p, err := coveredPath(base)
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		orig[base] = string(b)
		pathOf[base] = p
	}
	t.Cleanup(func() {
		for _, base := range bases {
			got, err := os.ReadFile(pathOf[base])
			if err != nil {
				t.Errorf("重读 %s：%v", base, err)
				continue
			}
			if string(got) != orig[base] {
				t.Errorf("检查器把 %s 写回了磁盘", base)
			}
		}
	})
	cases := []struct {
		name string
		file string
		pass bool
		edit func(t *testing.T, src string) string
		want []string
	}{
		{
			name: "Sprintf",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, "i18n.T(activityStatusKey[x.Status])", `i18n.T(fmt.Sprintf("side.status.%s", "typo"))`)
			},
			want: []string{"函数调用 fmt.Sprintf", "render.go"},
		},
		{
			name: "LocalVar",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, "st := i18n.T(activityStatusKey[x.Status])", "k := \"side.status.\" + x.Status\n\t\tst := i18n.T(k)")
			},
			want: []string{"局部变量或参数 k", "render.go"},
		},
		{
			name: "UnregisteredConcat",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, "i18n.T(activityStatusKey[x.Status])", `i18n.T("side.status." + x.Status)`)
			},
			want: []string{"未登记的字符串拼接", "render.go"},
		},
		{
			name: "UnregisteredMap",
			edit: func(t *testing.T, src string) string {
				src = replaceOnce(t, src, "func TextKeys() []string {", "var otherStatusKey = map[string]string{\"launching\": \"side.status.launching\"}\n\nfunc TextKeys() []string {")
				return replaceOnce(t, src, "i18n.T(activityStatusKey[x.Status])", "i18n.T(otherStatusKey[x.Status])")
			},
			want: []string{"键表 otherStatusKey 没有被 TextKeys 枚举", "render.go"},
		},
		{
			name: "TableValueTypo",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, `"side.status.launching"`, `"side.status.typo"`)
			},
			want: []string{`"side.status.typo"`, "没进动态键集合", "render.go"},
		},
		{
			name: "ExtraMapLiteral",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, "func TextKeys() []string {", "var strayStatusKey = map[string]string{\"x\": \"side.status.typo\"}\n\nfunc TextKeys() []string {")
			},
			want: []string{`"side.status.typo"`, "没进动态键集合", "render.go"},
		},
		{
			name: "ClosureParam",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, "st := i18n.T(activityStatusKey[x.Status])",
					"st := func(activityStatusKey map[string]string) string {\n\t\t\tst := i18n.T(activityStatusKey[x.Status])\n\t\t\treturn st\n\t\t}(map[string]string{\"launching\": \"side.status.\" + \"typo\"})")
			},
			want: []string{"不是包级变量", "activityStatusKey", "render.go"},
		},
		{
			name: "StringShadow",
			file: "settings.go",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, `func tierName(t deckstore.Tier) string { return T("tier." + string(t)) }`,
					"func tierName(t deckstore.Tier) string {\n\tstring := func(deckstore.Tier) string { return \"ty\" + \"po\" }\n\treturn T(\"tier.\" + string(t))\n}")
			},
			want: []string{"没有绑定到预声明类型", "settings.go"},
		},
		{
			name: "LocalShadow",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, "st := i18n.T(activityStatusKey[x.Status])",
					"activityStatusKey := map[string]string{\"launching\": \"side.status.\" + \"typo\"}\n\t\tst := i18n.T(activityStatusKey[x.Status])")
			},
			want: []string{"不是包级变量", "activityStatusKey", "render.go"},
		},
		{
			name: "ReceiverShadow",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, "func TextKeys() []string {",
					"type activityStatusMap map[string]string\n\nfunc (activityStatusKey activityStatusMap) text(status string) string {\n\treturn i18n.T(activityStatusKey[status])\n}\n\nfunc TextKeys() []string {")
			},
			want: []string{"不是包级变量", "activityStatusKey", "render.go"},
		},
		{
			name: "FieldShadow",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, "st := i18n.T(activityStatusKey[x.Status])",
					"box := struct{ activityStatusKey map[string]string }{activityStatusKey: map[string]string{\"launching\": \"side.status.\" + \"typo\"}}\n\t\tst := i18n.T(box.activityStatusKey[x.Status])")
			},
			want: []string{"不是包级变量", "activityStatusKey", "render.go"},
		},
		{
			name: "ImportAlias",
			edit: func(t *testing.T, src string) string {
				src = replaceOnce(t, src, `"github.com/Tingschen287/terminal-deck/work/internal/i18n"`,
					"tr \"github.com/Tingschen287/terminal-deck/work/internal/i18n\"\n\t\"github.com/Tingschen287/terminal-deck/work/internal/i18n\"")
				return replaceOnce(t, src, "st := i18n.T(activityStatusKey[x.Status])",
					"st := i18n.T(activityStatusKey[x.Status])\n\t\tst = tr.T(\"side.status.\" + \"typo\")")
			},
			want: []string{"未登记的字符串拼接", "render.go"},
		},
		{
			name: "DotImport",
			edit: func(t *testing.T, src string) string {
				src = replaceOnce(t, src, `"github.com/Tingschen287/terminal-deck/work/internal/i18n"`,
					". \"github.com/Tingschen287/terminal-deck/work/internal/i18n\"\n\t\"github.com/Tingschen287/terminal-deck/work/internal/i18n\"")
				return replaceOnce(t, src, "st := i18n.T(activityStatusKey[x.Status])",
					"st := i18n.T(activityStatusKey[x.Status])\n\t\tst = T(\"side.status.\" + \"typo\")")
			},
			want: []string{"未登记的字符串拼接", "render.go"},
		},
		{
			name: "FuncValue",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, "st := i18n.T(activityStatusKey[x.Status])",
					"f := i18n.T\n\t\t_ = f(\"side.status.\" + \"typo\")\n\t\tst := i18n.T(activityStatusKey[x.Status])")
			},
			want: []string{"不在直接调用", "render.go"},
		},
		{
			name: "AliasTier",
			file: "settings.go",
			pass: true,
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, `func tierName(t deckstore.Tier) string { return T("tier." + string(t)) }`,
					"type Tier2 = deckstore.Tier\n\nfunc tierName(t Tier2) string { return T(\"tier.\" + string(t)) }")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			file := tc.file
			if file == "" {
				file = "render.go"
			}
			edited := tc.edit(t, orig[file])
			if edited == orig[file] {
				t.Fatal("源码没有改动")
			}
			probs, err := dynamicKeyProblems(map[string]string{pathOf[file]: edited})
			if err != nil {
				t.Fatal(err)
			}
			blob := strings.Join(probs, "\n")
			if tc.pass {
				if len(probs) != 0 {
					t.Fatalf("应通过，实际：\n%s", blob)
				}
				return
			}
			for _, w := range tc.want {
				if !strings.Contains(blob, w) {
					t.Fatalf("应报错含 %q，实际：\n%s", w, blob)
				}
			}
		})
	}
}

// checkOrphanKeyLiterals 要求：像键名、又不是 T 的字面量参数的字符串，必须在动态键集合里。
// 这样新写进 map 或切片、但忘了收进 TextKeys 的键过不了。
func checkOrphanKeyLiterals(t *testing.T, catalog map[string]bool) {
	t.Helper()
	probs, err := orphanProblems(nil, catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range probs {
		t.Error(p)
	}
}

func checkDynamicCallSites(t *testing.T) {
	t.Helper()
	probs, err := dynamicCallProblems(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range probs {
		t.Error(p)
	}
}

// checkTierConcat 锁住 "tier." + 档位名。整键不是字面量，只能从档位常量枚举。
func checkTierConcat(t *testing.T, catalog map[string]bool) {
	t.Helper()
	probs, err := tierProblems(nil, catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range probs {
		t.Error(p)
	}
}

// dynamicKeyProblems 对给定源码做动态取键检查：调用表达式、落单的键字面量、档位拼接。
// src 的键是绝对路径，值是整文件源码。nil 表示全部读磁盘。不写盘。
func dynamicKeyProblems(src map[string]string) ([]string, error) {
	catalog := map[string]bool{}
	for _, k := range dynamicTextKeys() {
		catalog[k] = true
	}
	var probs []string
	for _, part := range []func() ([]string, error){
		func() ([]string, error) { return dynamicCallProblems(src) },
		func() ([]string, error) { return orphanProblems(src, catalog) },
		func() ([]string, error) { return tierProblems(src, catalog) },
	} {
		p, err := part()
		if err != nil {
			return nil, err
		}
		probs = append(probs, p...)
	}
	return probs, nil
}

func dynamicCallProblems(src map[string]string) ([]string, error) {
	var probs []string
	err := withTypeImp(func(fset *token.FileSet, imp types.Importer) error {
		files, err := parseCoveredInto(fset, src)
		if err != nil {
			return err
		}
		text, err := i18nTextFuncs(imp)
		if err != nil {
			return err
		}
		checked, err := checkCovered(fset, imp, files)
		if err != nil {
			return err
		}
		probs = bindingProblems(fset, checked, text)
		return nil
	})
	return probs, err
}

func orphanProblems(src map[string]string, catalog map[string]bool) ([]string, error) {
	fset, files, err := parseCovered(src)
	if err != nil {
		return nil, err
	}
	var probs []string
	direct := map[token.Pos]bool{}
	for _, pf := range files {
		ast.Inspect(pf.file, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				name, _, _, literal := isTextCall(call)
				if name != "" && literal {
					direct[call.Args[0].Pos()] = true
				}
			}
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || direct[lit.Pos()] {
				return true
			}
			v, err := strconv.Unquote(lit.Value)
			if err != nil || !keyShape.MatchString(v) {
				return true
			}
			if !catalog[v] {
				probs = append(probs, fmt.Sprintf("%s 的 %q 不在 T 的字面量参数里，也没进动态键集合", fset.Position(lit.Pos()), v))
			}
			return true
		})
	}
	return probs, nil
}

func tierProblems(src map[string]string, catalog map[string]bool) ([]string, error) {
	fset, files, err := parseCovered(src)
	if err != nil {
		return nil, err
	}
	var probs []string
	var prefs []string
	for _, pf := range files {
		ast.Inspect(pf.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, _, _, literal := isTextCall(call)
			if name == "" || literal {
				return true
			}
			bin, ok := call.Args[0].(*ast.BinaryExpr)
			if !ok || bin.Op != token.ADD {
				return true
			}
			lit, ok := bin.X.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				probs = append(probs, fmt.Sprintf("%s：动态拼接的前缀不是字符串", fset.Position(call.Pos())))
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				probs = append(probs, err.Error())
				return true
			}
			prefs = append(prefs, s)
			return true
		})
	}
	if strings.Join(prefs, ",") != "tier." {
		probs = append(probs, fmt.Sprintf("档位键拼接前缀是 %q，要是 tier.", prefs))
	}
	for _, tier := range deckstore.Tiers {
		k := "tier." + string(tier)
		if !catalog[k] {
			probs = append(probs, "档位键 "+k+" 没有收进动态键集合")
		}
	}
	return probs, nil
}

// 依赖包的类型信息只建一次，读本地源码，不联网、不新增模块。
// 被测包每次用内存里的源码重新检查。FileSet 跟导入器绑在一起，所以一直复用。
var (
	typeImpMu   sync.Mutex
	typeImpFset *token.FileSet
	typeImp     types.Importer
)

const (
	moduleImport    = "github.com/Tingschen287/terminal-deck/work"
	deckstoreImport = moduleImport + "/internal/deckstore"
	i18nImport      = moduleImport + "/internal/i18n"
)

func moduleRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("caller")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

func pkgImportPath(dir string) string {
	rel, err := filepath.Rel(moduleRoot(), dir)
	if err != nil {
		panic(err)
	}
	return moduleImport + "/" + filepath.ToSlash(rel)
}

// i18nTextFuncs 取出 i18n.T 与 i18n.Tf 的函数对象。比对用对象，不用名字。
// 当前 i18n 没有 Tf。名字仍会查；以后加上，入口自动把它算进去。
func i18nTextFuncs(imp types.Importer) (map[types.Object]bool, error) {
	pkg, err := imp.Import(i18nImport)
	if err != nil {
		return nil, fmt.Errorf("导入 i18n：%w", err)
	}
	out := map[types.Object]bool{}
	for _, name := range []string{"T", "Tf"} {
		fn, ok := pkg.Scope().Lookup(name).(*types.Func)
		if !ok {
			if name == "T" {
				return nil, errors.New("i18n.T 不是函数")
			}
			continue
		}
		out[fn] = true
	}
	return out, nil
}

func withTypeImp(fn func(*token.FileSet, types.Importer) error) error {
	typeImpMu.Lock()
	defer typeImpMu.Unlock()
	if typeImp == nil {
		typeImpFset = token.NewFileSet()
		typeImp = importer.ForCompiler(typeImpFset, "source", nil)
		if typeImp == nil {
			return errors.New("go/importer 没有 source 导入器")
		}
	}
	return fn(typeImpFset, typeImp)
}

type checkedPkg struct {
	dir   string
	files []parsedFile
	pkg   *types.Package
	info  *types.Info
	enums map[*types.Var]enumInfo
}

func checkCovered(fset *token.FileSet, imp types.Importer, files []parsedFile) ([]checkedPkg, error) {
	var order []string
	by := map[string][]parsedFile{}
	for _, pf := range files {
		dir := filepath.Dir(pf.path)
		if _, ok := by[dir]; !ok {
			order = append(order, dir)
		}
		by[dir] = append(by[dir], pf)
	}
	var out []checkedPkg
	for _, dir := range order {
		group := by[dir]
		asts := make([]*ast.File, len(group))
		for i, pf := range group {
			asts[i] = pf.file
		}
		info := &types.Info{
			Types: make(map[ast.Expr]types.TypeAndValue),
			Defs:  make(map[*ast.Ident]types.Object),
			Uses:  make(map[*ast.Ident]types.Object),
		}
		var msgs []string
		conf := types.Config{
			Importer: imp,
			Error: func(e error) {
				if len(msgs) < 8 {
					msgs = append(msgs, e.Error())
				}
			},
		}
		ip := pkgImportPath(dir)
		pkg, err := conf.Check(ip, fset, asts, info)
		if err != nil {
			return nil, fmt.Errorf("类型检查 %s：%v\n%s", ip, err, strings.Join(msgs, "\n"))
		}
		out = append(out, checkedPkg{
			dir: dir, files: group, pkg: pkg, info: info,
			enums: enumsIn(info, pkg, asts),
		})
	}
	return out, nil
}

func bindingProblems(fset *token.FileSet, pkgs []checkedPkg, text map[types.Object]bool) []string {
	var probs []string
	got := map[string]bool{}
	seen := map[token.Pos]bool{}
	directCount := 0
	for _, cp := range pkgs {
		w := bindWorld{info: cp.info, pkg: cp.pkg, enums: cp.enums, text: text}
		direct, inFunc := directCalls(cp.files)
		for id, obj := range cp.info.Uses {
			if !text[obj] {
				continue
			}
			site, ok := direct[id]
			if !ok {
				probs = append(probs, fmt.Sprintf("%s：文字函数 %s 不在直接调用的位置", fset.Position(id.Pos()), id.Name))
				continue
			}
			directCount++
			if !dynamicKeyCall(site.call) {
				continue
			}
			seen[site.call.Pos()] = true
			fn := inFunc[site.call]
			if fn == "" {
				probs = append(probs, fmt.Sprintf("%s：动态取键不在函数里", fset.Position(site.call.Pos())))
				continue
			}
			recordDynamic(fset, &w, got, &probs, site.file, fn, site.call)
		}
		for _, pf := range cp.files {
			base := filepath.Base(pf.path)
			for _, d := range pf.file.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if id := callFunIdent(call); id != nil && text[objOf(cp.info, id)] {
						return true
					}
					name, _, _, literal := isTextCall(call)
					if name == "" || literal {
						return true
					}
					seen[call.Pos()] = true
					recordDynamic(fset, &w, got, &probs, base, fd.Name.Name, call)
					return true
				})
			}
			ast.Inspect(pf.file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name, _, _, literal := isTextCall(call)
				if name != "" && !literal && !seen[call.Pos()] {
					probs = append(probs, fmt.Sprintf("%s：动态取键不在函数里", fset.Position(call.Pos())))
				}
				return true
			})
		}
	}
	if directCount < len(dynamicCallAllow) {
		probs = append(probs, fmt.Sprintf("按对象找到的直接调用有 %d 个，少于返工 3 统计的 %d 个", directCount, len(dynamicCallAllow)))
	}
	want := map[string]bool{}
	for _, s := range dynamicCallAllow {
		want[s] = true
	}
	for s := range got {
		if !want[s] {
			probs = append(probs, "新的动态取键 "+s+"：先确认键来自已枚举的集合，再写进 dynamicCallAllow")
		}
	}
	for s := range want {
		if !got[s] {
			probs = append(probs, "dynamicCallAllow 里的 "+s+" 已经不是动态取键")
		}
	}
	sort.Strings(probs)
	return probs
}

// callSite 是一处调用，以及它所在的文件（不含目录）。
type callSite struct {
	call *ast.CallExpr
	file string
}

// directCalls 记下直接调用的函数位置：Fun 本身，或 Fun 这个选择器的 Sel。
// inFunc 的值是包住这处调用的函数名。不在函数里则为空。
// defer 与 go 的语句包着的仍是 CallExpr，算直接调用，不算取值。
func directCalls(files []parsedFile) (map[*ast.Ident]callSite, map[*ast.CallExpr]string) {
	direct := map[*ast.Ident]callSite{}
	inFunc := map[*ast.CallExpr]string{}
	for _, pf := range files {
		base := filepath.Base(pf.path)
		for _, d := range pf.file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					inFunc[call] = fd.Name.Name
				}
				return true
			})
		}
		ast.Inspect(pf.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			site := callSite{call: call, file: base}
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				direct[fun] = site
			case *ast.SelectorExpr:
				direct[fun.Sel] = site
			}
			return true
		})
	}
	return direct, inFunc
}

func callFunIdent(call *ast.CallExpr) *ast.Ident {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun
	case *ast.SelectorExpr:
		return fun.Sel
	default:
		return nil
	}
}

// dynamicKeyCall 是要交给参数检查的直接调用：有参数、不是展开、首参不是字符串字面量。
func dynamicKeyCall(call *ast.CallExpr) bool {
	if call == nil || len(call.Args) == 0 || call.Ellipsis.IsValid() {
		return false
	}
	if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
		return false
	}
	return true
}

func recordDynamic(fset *token.FileSet, w *bindWorld, got map[string]bool, probs *[]string, fileBase, fn string, call *ast.CallExpr) {
	expr, err := printExpr(fset, call.Args[0])
	if err != nil {
		*probs = append(*probs, err.Error())
		return
	}
	pos := fset.Position(call.Pos())
	if okCall, why := w.realTextCall(call); !okCall {
		*probs = append(*probs, fmt.Sprintf("%s：%s（%s）", pos, why, expr))
		return
	}
	if okShape, why := w.classify(call.Args[0]); !okShape {
		*probs = append(*probs, fmt.Sprintf("%s：%s（%s）", pos, why, expr))
		return
	}
	got[fileBase+" · "+fn+" · "+expr] = true
}

// enumInfo 描述一张被 TextKeys 枚举到的包级表。比对用对象，不用名字。
// valueIsKey：下标的结果就是文字键。fields：结构体元素上，TextKeys 实际读过的字段。
type enumInfo struct {
	valueIsKey bool
	fields     map[*types.Var]bool
}

// bindWorld 是一次类型检查里，核对名字实际绑到哪个对象用的。
type bindWorld struct {
	info  *types.Info
	pkg   *types.Package
	enums map[*types.Var]enumInfo
	text  map[types.Object]bool
}

func enumsIn(info *types.Info, pkg *types.Package, files []*ast.File) map[*types.Var]enumInfo {
	out := map[*types.Var]enumInfo{}
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil || fd.Recv != nil || fd.Name.Name != "TextKeys" {
				continue
			}
			fn, ok := info.Defs[fd.Name].(*types.Func)
			if !ok || fn.Parent() != pkg.Scope() {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.RangeStmt:
					noteTables(out, info, pkg, tableIdents(n.X), n)
				case *ast.SliceExpr:
					noteTables(out, info, pkg, tableIdents(n.X), nil)
				}
				return true
			})
		}
	}
	return out
}

func noteTables(out map[*types.Var]enumInfo, info *types.Info, pkg *types.Package, ids []*ast.Ident, rng *ast.RangeStmt) {
	var val types.Object
	var fields map[*types.Var]bool
	if rng != nil {
		if id, ok := rng.Value.(*ast.Ident); ok {
			val = info.Defs[id]
		}
		if val != nil {
			fields = fieldsOn(rng.Body, val, info)
		}
	}
	for _, id := range ids {
		v, ok := objOf(info, id).(*types.Var)
		if !ok || v.Parent() != pkg.Scope() {
			continue
		}
		ei := out[v]
		if valueIsKeyType(v.Type()) {
			ei.valueIsKey = true
		}
		if len(fields) > 0 {
			if ei.fields == nil {
				ei.fields = map[*types.Var]bool{}
			}
			for f := range fields {
				ei.fields[f] = true
			}
		}
		if ei.valueIsKey || len(ei.fields) > 0 {
			out[v] = ei
		}
	}
}

func tableIdents(e ast.Expr) []*ast.Ident {
	switch e := unparen(e).(type) {
	case *ast.Ident:
		return []*ast.Ident{e}
	case *ast.CompositeLit:
		var ids []*ast.Ident
		for _, elt := range e.Elts {
			if id, ok := unparen(elt).(*ast.Ident); ok {
				ids = append(ids, id)
			}
		}
		return ids
	default:
		return nil
	}
}

func fieldsOn(body ast.Node, val types.Object, info *types.Info) map[*types.Var]bool {
	if body == nil || val == nil {
		return nil
	}
	out := map[*types.Var]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || objOf(info, id) != val {
			return true
		}
		fv, ok := objOf(info, sel.Sel).(*types.Var)
		if ok && fv.IsField() {
			out[fv] = true
		}
		return true
	})
	return out
}

func valueIsKeyType(t types.Type) bool {
	if t == nil {
		return false
	}
	switch u := t.Underlying().(type) {
	case *types.Map:
		return isPlainString(u.Elem())
	case *types.Slice:
		return isPlainString(u.Elem())
	case *types.Array:
		return isPlainString(u.Elem())
	default:
		return false
	}
}

func isPlainString(t types.Type) bool {
	b, ok := t.(*types.Basic)
	return ok && b.Kind() == types.String
}

func objOf(info *types.Info, id *ast.Ident) types.Object {
	if id == nil || info == nil {
		return nil
	}
	if obj := info.Uses[id]; obj != nil {
		return obj
	}
	return info.Defs[id]
}

func (w *bindWorld) realTextCall(call *ast.CallExpr) (bool, string) {
	if id := callFunIdent(call); id != nil && w.text[objOf(w.info, id)] {
		return true, ""
	}
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		fn, ok := objOf(w.info, fun).(*types.Func)
		if !ok || fn.Parent() != w.pkg.Scope() || (fun.Name != "T" && fun.Name != "Tf") {
			return false, "函数 " + fun.Name + " 绑定的不是包级文字函数"
		}
		return true, ""
	case *ast.SelectorExpr:
		base, ok := fun.X.(*ast.Ident)
		if !ok {
			return false, "函数 " + fun.Sel.Name + " 绑定的不是 i18n 的文字函数"
		}
		if _, ok := objOf(w.info, base).(*types.PkgName); !ok {
			return false, "函数 " + fun.Sel.Name + " 绑定的不是 i18n 的文字函数"
		}
		fn, ok := objOf(w.info, fun.Sel).(*types.Func)
		if !ok || fn.Pkg() == nil || fn.Pkg().Path() != i18nImport {
			return false, "函数 " + fun.Sel.Name + " 绑定的不是 i18n 的文字函数"
		}
		switch fun.Sel.Name {
		case "T", "Tf", "Format", "Errorf":
			return true, ""
		default:
			return false, "函数 " + fun.Sel.Name + " 绑定的不是 i18n 的文字函数"
		}
	default:
		return false, "函数绑定的不是文字函数"
	}
}

func (w *bindWorld) classify(arg ast.Expr) (bool, string) {
	if ok, why := w.tierConcat(arg); ok || why != "" {
		return ok, why
	}
	if name := firstCall(arg); name != "" {
		return false, "键参数里有函数调用 " + name
	}
	if hasAdd(arg) {
		return false, "未登记的字符串拼接"
	}
	arg = unparen(arg)
	if id, ok := arg.(*ast.Ident); ok {
		return false, w.badIdent(id)
	}
	return w.indexForm(arg)
}

func (w *bindWorld) tierConcat(arg ast.Expr) (bool, string) {
	bin, ok := unparen(arg).(*ast.BinaryExpr)
	if !ok || bin.Op != token.ADD {
		return false, ""
	}
	lit, ok := bin.X.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false, ""
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil || s != "tier." {
		return false, ""
	}
	call, ok := bin.Y.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 || call.Ellipsis.IsValid() {
		return false, ""
	}
	fun, ok := call.Fun.(*ast.Ident)
	if !ok {
		return false, ""
	}
	if objOf(w.info, fun) != types.Universe.Lookup("string") {
		if fun.Name == "string" {
			return false, "string 没有绑定到预声明类型"
		}
		return false, "键参数里有函数调用 " + fun.Name
	}
	tv, ok := w.info.Types[call.Args[0]]
	if !ok || !isDeckstoreTier(tv.Type) {
		return false, "档位参数的类型不是 deckstore.Tier"
	}
	if name := firstCall(call.Args[0]); name != "" {
		return false, "档位参数里有函数调用 " + name
	}
	return true, ""
}

func isDeckstoreTier(t types.Type) bool {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Name() == "Tier" && obj.Pkg() != nil && obj.Pkg().Path() == deckstoreImport
}

func (w *bindWorld) badIdent(id *ast.Ident) string {
	if v, ok := objOf(w.info, id).(*types.Var); ok && v.Parent() != w.pkg.Scope() {
		return "键参数是局部变量或参数 " + id.Name
	}
	return "键参数不是已登记的形状 " + id.Name
}

func (w *bindWorld) indexForm(arg ast.Expr) (bool, string) {
	var field *ast.Ident
	arg = unparen(arg)
	if sel, ok := arg.(*ast.SelectorExpr); ok {
		field = sel.Sel
		arg = unparen(sel.X)
	}
	ix, ok := arg.(*ast.IndexExpr)
	if !ok {
		if id, ok := arg.(*ast.Ident); ok {
			if _, why := w.pkgVar(id); why != "" {
				return false, "键参数是局部变量或参数 " + id.Name
			}
		}
		return false, "键参数不是已登记的形状"
	}
	id, ok := baseIdent(ix.X)
	if !ok {
		return false, "键参数不是已登记的形状"
	}
	v, why := w.pkgVar(id)
	if v == nil {
		return false, why
	}
	ei, ok := w.enums[v]
	if !ok {
		return false, "键表 " + id.Name + " 没有被 TextKeys 枚举"
	}
	if field == nil {
		if !ei.valueIsKey {
			return false, "键表 " + id.Name + " 的下标结果不是文字键"
		}
		return true, ""
	}
	fv, ok := objOf(w.info, field).(*types.Var)
	if !ok || !fv.IsField() || !ei.fields[fv] {
		return false, "键表 " + id.Name + " 的字段 " + field.Name + " 没有被 TextKeys 枚举"
	}
	return true, ""
}

func (w *bindWorld) pkgVar(id *ast.Ident) (*types.Var, string) {
	obj := objOf(w.info, id)
	if obj == nil {
		return nil, "解析不到 " + id.Name + " 的绑定"
	}
	v, ok := obj.(*types.Var)
	if !ok || v.Parent() != w.pkg.Scope() {
		return nil, "键表 " + id.Name + " 不是包级变量"
	}
	return v, ""
}

func baseIdent(e ast.Expr) (*ast.Ident, bool) {
	e = unparen(e)
	if id, ok := e.(*ast.Ident); ok {
		return id, true
	}
	if sel, ok := e.(*ast.SelectorExpr); ok {
		return sel.Sel, true
	}
	return nil, false
}

func firstCall(e ast.Expr) string {
	name := ""
	ast.Inspect(e, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok || name != "" {
			return name == ""
		}
		name = callName(c)
		return false
	})
	return name
}

func callName(c *ast.CallExpr) string {
	switch f := c.Fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		if id, ok := f.X.(*ast.Ident); ok {
			return id.Name + "." + f.Sel.Name
		}
		return f.Sel.Name
	default:
		return "调用"
	}
}

func hasAdd(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		b, ok := n.(*ast.BinaryExpr)
		if ok && b.Op == token.ADD {
			found = true
			return false
		}
		return !found
	})
	return found
}

func unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

func printExpr(fset *token.FileSet, e ast.Expr) (string, error) {
	var b bytes.Buffer
	cfg := printer.Config{Mode: printer.UseSpaces, Tabwidth: 8}
	if err := cfg.Fprint(&b, fset, e); err != nil {
		return "", err
	}
	return b.String(), nil
}

func coveredPath(base string) (string, error) {
	var hits []string
	for _, dir := range coveredDirs() {
		p := filepath.Join(dir, base)
		if _, err := os.Stat(p); err == nil {
			hits = append(hits, p)
		}
	}
	if len(hits) != 1 {
		return "", fmt.Errorf("文件 %s 匹配到 %d 个", base, len(hits))
	}
	return hits[0], nil
}

func replaceOnce(t *testing.T, src, old, neu string) string {
	t.Helper()
	n := strings.Count(src, old)
	if n != 1 {
		t.Fatalf("片段出现 %d 次：%s", n, old)
	}
	return strings.Replace(src, old, neu, 1)
}

// checkDynamicKeysPlain 拦住带占位符的动态键。这种键的参数个数，字面量扫描数不到。
func checkDynamicKeysPlain(t *testing.T, keys []string) {
	t.Helper()
	verbs := func(s string) []string {
		return verbRe.FindAllString(strings.ReplaceAll(s, "%%", ""), -1)
	}
	seen := map[string]bool{}
	for _, k := range keys {
		if !seen[k] {
			seen[k] = true
			zh, zhOK := i18n.TextZH(k)
			en, enOK := i18n.TextEN(k)
			if zhOK && enOK {
				if vz, ve := verbs(zh), verbs(en); len(vz) > 0 || len(ve) > 0 {
					t.Errorf("动态键 %q 带占位符 %v / %v，参数个数扫不到", k, vz, ve)
				}
			}
		}
	}
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
