package deckui

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
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

// dynamicCallAllow 是放行的动态取键。粒度是「文件 · 函数 · 参数表达式原文」。
// 原文用 go/printer 打印。改一个字符就对不上，测试失败，要人工复核后再改清单。
// 清单挡不住坏形状：函数调用、未登记的 +、局部变量或参数，一律另报错。
//
// 过关的形状只有三种，每种都能证明键来自可枚举集合：
//   - 表[下标]：表是本包 TextKeys 里 range 到的 map[string]string，或 TextKeys 里切片过的字符串数组。
//     下标结果只能是表里的值，TextKeys 会把这些值交出去。
//   - 表[下标].字段：表是结构体切片，TextKeys 的 range 只读这个字段。
//   - "tier." + string(档位参数)：参数类型是 deckstore.Tier。string 是类型转换，不是拼键函数。
//     前缀另由 checkTierConcat 锁成 tier.，每个档位都在动态键集合里。
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
	var out []parsedFile
	for _, dir := range coveredDirs() {
		ms, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			return nil, nil, err
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
				return nil, nil, fmt.Errorf("解析 %s：%w", path, err)
			}
			out = append(out, parsedFile{path: path, file: f})
		}
	}
	return fset, out, nil
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

// TestDynamicCallExprMutationsFail 读真实的 render.go，在内存里改已放行的调用或键表。
// 检查器必须报错。不写回磁盘。
func TestDynamicCallExprMutationsFail(t *testing.T) {
	clean, err := dynamicKeyProblems(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(clean) != 0 {
		t.Fatalf("未改动的源码不应报错：\n%s", strings.Join(clean, "\n"))
	}
	renderPath, err := coveredPath("render.go")
	if err != nil {
		t.Fatal(err)
	}
	orig, err := os.ReadFile(renderPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		got, err := os.ReadFile(renderPath)
		if err != nil {
			t.Errorf("重读源码：%v", err)
			return
		}
		if string(got) != string(orig) {
			t.Error("检查器把 render.go 写回了磁盘")
		}
	})
	cases := []struct {
		name string
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			edited := tc.edit(t, string(orig))
			if edited == string(orig) {
				t.Fatal("源码没有改动")
			}
			probs, err := dynamicKeyProblems(map[string]string{renderPath: edited})
			if err != nil {
				t.Fatal(err)
			}
			blob := strings.Join(probs, "\n")
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
	fset, files, err := parseCovered(src)
	if err != nil {
		return nil, err
	}
	tables := enumeratedTables(files)
	var probs []string
	got := map[string]bool{}
	seen := map[token.Pos]bool{}
	for _, pf := range files {
		pkgTables := tables[pf.file.Name.Name]
		for _, d := range pf.file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			params, locals := bindNames(fd)
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name, _, _, literal := isTextCall(call)
				if name == "" || literal {
					return true
				}
				seen[call.Pos()] = true
				expr, err := printExpr(fset, call.Args[0])
				if err != nil {
					probs = append(probs, err.Error())
					return true
				}
				pos := fset.Position(call.Pos())
				if okShape, why := classifyKey(call.Args[0], fd, pkgTables, params, locals); !okShape {
					probs = append(probs, fmt.Sprintf("%s：%s（%s）", pos, why, expr))
					return true
				}
				got[filepath.Base(pf.path)+" · "+fd.Name.Name+" · "+expr] = true
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
	return probs, nil
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

// enumInfo 描述一张被 TextKeys 枚举的包级表。
// valueIsKey：下标的结果就是文字键（map[string]string 或字符串数组）。
// fields：结构体切片里，TextKeys 实际读取的字段。
type enumInfo struct {
	valueIsKey bool
	fields     map[string]bool
}

func enumeratedTables(files []parsedFile) map[string]map[string]enumInfo {
	vars := map[string]map[string]ast.Expr{}
	for _, pf := range files {
		pkg := pf.file.Name.Name
		if vars[pkg] == nil {
			vars[pkg] = map[string]ast.Expr{}
		}
		for _, d := range pf.file.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, sp := range gd.Specs {
				vs, ok := sp.(*ast.ValueSpec)
				if !ok {
					continue
				}
				typ := specType(vs)
				for _, name := range vs.Names {
					vars[pkg][name.Name] = typ
				}
			}
		}
	}
	out := map[string]map[string]enumInfo{}
	for _, pf := range files {
		pkg := pf.file.Name.Name
		if out[pkg] == nil {
			out[pkg] = map[string]enumInfo{}
		}
		pv := vars[pkg]
		for _, d := range pf.file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Name.Name != "TextKeys" || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.RangeStmt:
					for _, name := range rangeTableNames(n.X, pv) {
						info := out[pkg][name]
						if fields := rangeFields(n); len(fields) > 0 {
							if info.fields == nil {
								info.fields = map[string]bool{}
							}
							for _, f := range fields {
								info.fields[f] = true
							}
							out[pkg][name] = info
						} else if varValueIsKey(pv[name]) {
							info.valueIsKey = true
							out[pkg][name] = info
						}
					}
				case *ast.SliceExpr:
					id, ok := n.X.(*ast.Ident)
					if !ok || !varValueIsKey(pv[id.Name]) {
						return true
					}
					info := out[pkg][id.Name]
					info.valueIsKey = true
					out[pkg][id.Name] = info
				}
				return true
			})
		}
	}
	return out
}

func specType(vs *ast.ValueSpec) ast.Expr {
	if vs.Type != nil {
		return vs.Type
	}
	if len(vs.Values) == 1 {
		if lit, ok := vs.Values[0].(*ast.CompositeLit); ok {
			return lit.Type
		}
	}
	return nil
}

func varValueIsKey(typ ast.Expr) bool {
	switch t := typ.(type) {
	case *ast.MapType:
		return isStringType(t.Value)
	case *ast.ArrayType:
		return isStringType(t.Elt)
	default:
		return false
	}
}

func isStringType(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "string"
}

func rangeTableNames(x ast.Expr, vars map[string]ast.Expr) []string {
	switch x := unparen(x).(type) {
	case *ast.Ident:
		if _, ok := vars[x.Name]; ok {
			return []string{x.Name}
		}
	case *ast.CompositeLit:
		var names []string
		for _, elt := range x.Elts {
			id, ok := elt.(*ast.Ident)
			if !ok {
				continue
			}
			if _, ok := vars[id.Name]; ok {
				names = append(names, id.Name)
			}
		}
		return names
	}
	return nil
}

func rangeFields(n *ast.RangeStmt) []string {
	id, ok := n.Value.(*ast.Ident)
	if !ok || id.Name == "_" || n.Body == nil {
		return nil
	}
	var fields []string
	seen := map[string]bool{}
	ast.Inspect(n.Body, func(node ast.Node) bool {
		sel, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		base, ok := sel.X.(*ast.Ident)
		if !ok || base.Name != id.Name || seen[sel.Sel.Name] {
			return true
		}
		seen[sel.Sel.Name] = true
		fields = append(fields, sel.Sel.Name)
		return true
	})
	return fields
}

func classifyKey(arg ast.Expr, fd *ast.FuncDecl, tables map[string]enumInfo, params, locals map[string]bool) (bool, string) {
	if isTierConcat(arg, fd) {
		return true, ""
	}
	if name := firstCall(arg); name != "" {
		return false, "键参数里有函数调用 " + name
	}
	if hasAdd(arg) {
		return false, "未登记的字符串拼接"
	}
	arg = unparen(arg)
	if id, ok := arg.(*ast.Ident); ok {
		if params[id.Name] || locals[id.Name] {
			return false, "键参数是局部变量或参数 " + id.Name
		}
		return false, "键参数不是已登记的形状 " + id.Name
	}
	if sel, ok := arg.(*ast.SelectorExpr); ok {
		if id, ok := sel.X.(*ast.Ident); ok && (params[id.Name] || locals[id.Name]) {
			return false, "键参数是局部变量或参数 " + id.Name
		}
	}
	table, field, ok := indexTable(arg)
	if !ok {
		return false, "键参数不是已登记的形状"
	}
	if params[table] || locals[table] {
		return false, "键参数是局部变量或参数 " + table
	}
	info, enumerated := tables[table]
	if !enumerated {
		return false, "键表 " + table + " 没有被 TextKeys 枚举"
	}
	if field == "" {
		if !info.valueIsKey {
			return false, "键表 " + table + " 的下标结果不是文字键"
		}
		return true, ""
	}
	if !info.fields[field] {
		return false, "键表 " + table + " 的字段 " + field + " 没有被 TextKeys 枚举"
	}
	return true, ""
}

func isTierConcat(arg ast.Expr, fd *ast.FuncDecl) bool {
	bin, ok := unparen(arg).(*ast.BinaryExpr)
	if !ok || bin.Op != token.ADD {
		return false
	}
	lit, ok := bin.X.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil || s != "tier." {
		return false
	}
	call, ok := bin.Y.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 || call.Ellipsis.IsValid() {
		return false
	}
	fun, ok := call.Fun.(*ast.Ident)
	if !ok || fun.Name != "string" {
		return false
	}
	id, ok := call.Args[0].(*ast.Ident)
	if !ok {
		return false
	}
	return isTierParam(fd, id.Name)
}

func isTierParam(fd *ast.FuncDecl, name string) bool {
	if fd == nil || fd.Type == nil || fd.Type.Params == nil {
		return false
	}
	for _, f := range fd.Type.Params.List {
		sel, ok := f.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Tier" {
			continue
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "deckstore" {
			continue
		}
		for _, n := range f.Names {
			if n.Name == name {
				return true
			}
		}
	}
	return false
}

func indexTable(arg ast.Expr) (table, field string, ok bool) {
	arg = unparen(arg)
	if sel, isSel := arg.(*ast.SelectorExpr); isSel {
		field = sel.Sel.Name
		arg = unparen(sel.X)
	}
	ix, isIx := arg.(*ast.IndexExpr)
	if !isIx {
		return "", "", false
	}
	id, isID := ix.X.(*ast.Ident)
	if !isID {
		return "", "", false
	}
	return id.Name, field, true
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

func bindNames(fd *ast.FuncDecl) (params, locals map[string]bool) {
	params = map[string]bool{}
	locals = map[string]bool{}
	addFields := func(fl *ast.FieldList, dst map[string]bool) {
		if fl == nil {
			return
		}
		for _, f := range fl.List {
			for _, n := range f.Names {
				if n.Name != "_" {
					dst[n.Name] = true
				}
			}
		}
	}
	addFields(fd.Recv, locals)
	if fd.Type != nil {
		addFields(fd.Type.Params, params)
		addFields(fd.Type.Results, locals)
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if n.Tok != token.DEFINE {
				return true
			}
			for _, lhs := range n.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
					locals[id.Name] = true
				}
			}
		case *ast.RangeStmt:
			if n.Tok != token.DEFINE {
				return true
			}
			for _, idn := range []ast.Expr{n.Key, n.Value} {
				if id, ok := idn.(*ast.Ident); ok && id.Name != "_" {
					locals[id.Name] = true
				}
			}
		case *ast.DeclStmt:
			gd, ok := n.Decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				return true
			}
			for _, sp := range gd.Specs {
				vs, ok := sp.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, name := range vs.Names {
					if name.Name != "_" {
						locals[name.Name] = true
					}
				}
			}
		}
		return true
	})
	return params, locals
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
