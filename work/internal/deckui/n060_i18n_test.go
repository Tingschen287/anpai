// n060 文字键检查器。
//
// Key 的底层是 string。不靠 reflect / unsafe，i18n 包外要得到一个不在表里的 Key，
// Go 规范里只剩下面几条。本检查器对模块内 i18n 包外的全部非测试包逐条处理。
// 表达式先 ast.Unparen 去括号，类型先 types.Unalias。
//
//  1. 转换。目标类型「含有 Key」就要查。含有是递归的：Key 本身、指针、切片、数组、
//     map、chan、函数签名、结构体字段（mentionsKey）。
//     - 目标恰是 Key，操作数是常量：交给第 5 条。
//     - 目标恰是 Key，操作数非常量：只放行返工 3 登记过的形状，按对象绑定。
//     - 目标是别的含 Key 的类型（例如 *Key、带 Key 字段的结构体指针），操作数非常量：一律报错。
//  2. 运算。结果类型是 Key 的非常量二元表达式 + 报错。左边类型是 Key 的 += 报错。
//  3. 切片。结果类型是 Key 的切片表达式 k[i:j] 报错。
//  4. 泛型。类型实参里含有 Key 的实例化（info.Instances，含 *Key、[]Key）报错，
//     除非 genericKeyAllow 写明。
//  5. 常量。类型是 Key 的最外层常量，值必须在 Keys() 里。
//
// 这就盖住了规范里的全部途径：
//   - 字符串不可变。要改出一段新文本，只能 +、+= 或切片，上面都报了。
//   - 下标 k[i] 得到的是 byte，不是 Key。
//   - min / max 只返回其中一个操作数，不造新字符串。
//   - 接口断言、信道接收、map 或数组下标，取回的都是已经有的 Key，不造新值。
//   - 方法只能写在声明 Key 的 i18n 包里，包外加不了。包内的转换和运算由 i18n 的证明测试锁住。
//
// 零值来源：var k i18n.Key、*new(i18n.Key)、map 里没有的项，得到的都是空键。
// 这几种造不出新文本，检查器不报。运行时保险兜住：空键不在中文表里，
// T 会记一笔缺键并返回空串。渲染路径上出现空键时，缺键记录不为零
// （TestEmptyKeyOnRenderRecordsMiss）。
//
// 不在范围：reflect、unsafe、go:linkname、cgo、代码生成，
// 以及经 encoding/json、fmt.Sscan 等反射式解码得到的 Key。
// 这些路径编译器追踪不到。运行时由 RecordMisses 在渲染测试里兜住。
package deckui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"os"
	"os/exec"
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
	// sep.nl 是 i18n.ListOf 的换行分隔符（deckd.whyList、errors.Join 的线路写法）。ListOf 的第一个参数不在 T / E 的扫描里，
	// 键由 ListOf 在 deckd 里按字面量传入，存在性由 TestN060ListSepKeys 查（返工 3）。
	out = append(out, "sep.nl")
	return out
}

// dynamicCallAllow 是「非常量、目标恰为 Key」的转换放行清单。粒度是「文件 · 函数 · 操作数原文」。
// 原文用 go/printer 打印。改一个字符就对不上，测试失败，要人工复核后再改清单。
// 清单不是放行依据。依据是 go/types 的符号绑定，坏绑定写进清单也过不了：
//   - 表[下标]、表[下标].字段：被下标的标识符必须绑定到包级变量（Parent 是包作用域），
//     并且就是本包 TextKeys 枚举到的那张表。按对象比对，不按名字。字段也按对象比对。
//   - "tier." + string(参数)：string 必须是 Universe 里的预声明类型。参数类型先 Unalias，再认 deckstore.Tier。
//     前缀另由 checkTierConcat 锁成 tier.。
//   - 线路码：操作数是当前函数自己的参数。函数体里对真正的 i18n.Keys 做 range。
//     比较必须写在 if 的条件里（可以是 && / ||），而且这个 if 在转换之前。
//     if 的函数体要真的用上比较结果（赋值、return 或 break）。
//     函数里不能再给这个参数赋值。转换必须直接写在这个函数里。
//     形状不齐就按原来的规则拒绝，不因为名字像就放行。
//
// 表的值本身已经是 Key 时不再套转换，所以那些下标不在这张清单里。
// 函数调用、局部变量、参数、闭包参数、接收者、字段、未登记拼接、被遮蔽的名字，一律拒绝。
// 目标不是 Key、但类型里含有 Key 的非常量转换，不走这张清单，一律拒绝。
// 常量键不走这张清单，直接对 i18n.Keys()。
var dynamicCallAllow = []string{
	"settings.go · tierName · \"tier.\" + string(t)",
	"wire.go · renderCoded · code",
}

// genericKeyAllow 是类型实参里含有 Key 的泛型实例化。现在没有。
// 写进来之前先说明这次实例化为什么造不出表外的键。
var genericKeyAllow []string

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
		if ok && id.Name == "i18n" && (fun.Sel.Name == "T" || fun.Sel.Name == "Tf" || fun.Sel.Name == "Format" || fun.Sel.Name == "Errorf" || fun.Sel.Name == "E" || fun.Sel.Name == "MustTextZH" || fun.Sel.Name == "TextZH") {
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
	// 第 2 段的错误码用在 deckd、deckstore、agentdeck、deckclient。这里只记「有人用」，不禁中文。
	// 给 owner 看的中文另由 TestOwnerFacingChineseIsCoded 按函数卡。
	// 键算用过，只认 i18n.E / T / Tf / Format / Errorf / MustTextZH / TextZH 的字面量参数，
	// 以及 dynamicTextKeys。单独的字符串字面量不算。
	walkProd(t, func(_ string, f *ast.File, _ *token.FileSet) {
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
		if _, ok := i18n.TextZH(i18n.Key(k)); !ok {
			t.Errorf("文字表里没有 %q", k)
		}
		if _, ok := i18n.TextEN(i18n.Key(k)); !ok {
			t.Errorf("英文没有 %q", k)
		}
	}
	for _, k := range i18n.Keys() {
		if !used[string(k)] {
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
	check := func(_ string, f *ast.File, fset *token.FileSet) {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, key, argc, literal := isTextCall(call)
			if !literal {
				return true
			}
			zh, ok := i18n.TextZH(i18n.Key(key))
			en, enOK := i18n.TextEN(i18n.Key(key))
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
	}
	walkCovered(t, check)
	walkProd(t, check)
}

// prodDirs 是第 2 段要核对「键有人用、参数个数对」的包。不把它们放进 coveredDirs：那里禁中文字面量。
// viewstate 在这份名单里，这样哨兵的字面量调用才算「有人用」。
func prodDirs() []string {
	return append(pkgDirs("deckd", "deckstore", "agentdeck", "deckclient", "viewstate", "lease", "writerlock", "projectctl"), cmdDeckDir())
}

// cmdDeckDir 是 cmd/deck：deck center 自己往中间窗格画字（fence、Pause），也在「键有人用、参数个数对」和中文字面量的核对之内（返工 4）。
func cmdDeckDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("caller")
	}
	return filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(file))), "cmd", "deck")
}

// ownerScanDirs 是「函数里不能再留中文字面量」的包。
// viewstate 也在里面，按函数登记哨兵正文，不整包排除。
func ownerScanDirs() []string {
	return append(pkgDirs("deckd", "deckstore", "agentdeck", "deckclient", "viewstate"), cmdDeckDir())
}

func pkgDirs(names ...string) []string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("caller")
	}
	root := filepath.Dir(filepath.Dir(file))
	var out []string
	for _, name := range names {
		out = append(out, filepath.Join(root, name))
	}
	return out
}

func walkProd(t *testing.T, fn func(path string, f *ast.File, fset *token.FileSet)) {
	t.Helper()
	walkDirs(t, prodDirs(), fn)
}

func walkDirs(t *testing.T, dirs []string, fn func(path string, f *ast.File, fset *token.FileSet)) {
	t.Helper()
	fset := token.NewFileSet()
	for _, dir := range dirs {
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
				t.Fatalf("解析 %s：%v", path, err)
			}
			fn(path, f, fset)
		}
	}
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
		_, zhOK := i18n.TextZH(i18n.Key(k))
		_, enOK := i18n.TextEN(i18n.Key(k))
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
	bases := []string{"render.go", "settings.go", "idle.go"}
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
	// wire.go 在 deckclient，不在 coveredDirs。单独读入，供反例改内存副本。
	wirePath := filepath.Join(moduleRoot(), "internal", "deckclient", "wire.go")
	wireBytes, err := os.ReadFile(wirePath)
	if err != nil {
		t.Fatal(err)
	}
	orig["wire.go"] = string(wireBytes)
	pathOf["wire.go"] = wirePath
	watch := append(append([]string{}, bases...), "wire.go")
	sanitizePath := filepath.Join(moduleRoot(), "internal", "deckstore", "sanitize.go")
	sanBytes, err := os.ReadFile(sanitizePath)
	if err != nil {
		t.Fatal(err)
	}
	sanitizeOrig := string(sanBytes)
	t.Cleanup(func() {
		for _, base := range watch {
			got, err := os.ReadFile(pathOf[base])
			if err != nil {
				t.Errorf("重读 %s：%v", base, err)
				continue
			}
			if string(got) != orig[base] {
				t.Errorf("检查器把 %s 写回了磁盘", base)
			}
		}
		got, err := os.ReadFile(sanitizePath)
		if err != nil {
			t.Errorf("重读 sanitize.go：%v", err)
		} else if string(got) != sanitizeOrig {
			t.Errorf("检查器把 sanitize.go 写回了磁盘")
		}
	})
	cases := []struct {
		name    string
		file    string
		pass    bool
		edit    func(t *testing.T, src string) string
		also    func(t *testing.T) map[string]string
		want    []string
		need    int
		needle  string
		runtime bool
	}{
		{
			name: "Sprintf",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, "i18n.T(activityStatusKey[x.Status])", `i18n.T(i18n.Key(fmt.Sprintf("side.status.%s", "typo")))`)
			},
			want: []string{"函数调用 fmt.Sprintf", "render.go"},
		},
		{
			name: "LocalVar",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, "st := i18n.T(activityStatusKey[x.Status])", "k := \"side.status.\" + x.Status\n\t\tst := i18n.T(i18n.Key(k))")
			},
			want: []string{"局部变量或参数 k", "render.go"},
		},
		{
			name: "UnregisteredConcat",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, "i18n.T(activityStatusKey[x.Status])", `i18n.T(i18n.Key("side.status." + x.Status))`)
			},
			want: []string{"未登记的字符串拼接", "render.go"},
		},
		{
			name: "UnregisteredMap",
			edit: func(t *testing.T, src string) string {
				src = replaceOnce(t, src, "func TextKeys() []string {", "var otherStatusKey = map[string]i18n.Key{\"launching\": \"side.status.launching\"}\n\nfunc TextKeys() []string {")
				return replaceOnce(t, src, "i18n.T(activityStatusKey[x.Status])", "i18n.T(i18n.Key(otherStatusKey[x.Status]))")
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
					"st := func(activityStatusKey map[string]string) string {\n\t\t\tst := i18n.T(i18n.Key(activityStatusKey[x.Status]))\n\t\t\treturn st\n\t\t}(map[string]string{\"launching\": \"side.status.\" + \"typo\"})")
			},
			want: []string{"不是包级变量", "activityStatusKey", "render.go"},
		},
		{
			name: "StringShadow",
			file: "settings.go",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, `func tierName(t deckstore.Tier) string { return T(i18n.Key("tier." + string(t))) }`,
					"func tierName(t deckstore.Tier) string {\n\tstring := func(deckstore.Tier) string { return \"ty\" + \"po\" }\n\treturn T(i18n.Key(\"tier.\" + string(t)))\n}")
			},
			want: []string{"没有绑定到预声明类型", "settings.go"},
		},
		{
			name: "LocalShadow",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, "st := i18n.T(activityStatusKey[x.Status])",
					"activityStatusKey := map[string]string{\"launching\": \"side.status.\" + \"typo\"}\n\t\tst := i18n.T(i18n.Key(activityStatusKey[x.Status]))")
			},
			want: []string{"不是包级变量", "activityStatusKey", "render.go"},
		},
		{
			name: "ReceiverShadow",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, "func TextKeys() []string {",
					"type activityStatusMap map[string]string\n\nfunc (activityStatusKey activityStatusMap) text(status string) string {\n\treturn i18n.T(i18n.Key(activityStatusKey[status]))\n}\n\nfunc TextKeys() []string {")
			},
			want: []string{"不是包级变量", "activityStatusKey", "render.go"},
		},
		{
			name: "FieldShadow",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, "st := i18n.T(activityStatusKey[x.Status])",
					"box := struct{ activityStatusKey map[string]string }{activityStatusKey: map[string]string{\"launching\": \"side.status.\" + \"typo\"}}\n\t\tst := i18n.T(i18n.Key(box.activityStatusKey[x.Status]))")
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
			want: []string{`"side.status.typo"`, "不在 Keys() 里", "render.go"},
		},
		{
			name: "DotImport",
			edit: func(t *testing.T, src string) string {
				src = replaceOnce(t, src, `"github.com/Tingschen287/terminal-deck/work/internal/i18n"`,
					". \"github.com/Tingschen287/terminal-deck/work/internal/i18n\"\n\t\"github.com/Tingschen287/terminal-deck/work/internal/i18n\"")
				return replaceOnce(t, src, "st := i18n.T(activityStatusKey[x.Status])",
					"st := i18n.T(activityStatusKey[x.Status])\n\t\tst = T(\"side.status.\" + \"typo\")")
			},
			want: []string{`"side.status.typo"`, "不在 Keys() 里", "render.go"},
		},
		{
			name: "FuncValue",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, "st := i18n.T(activityStatusKey[x.Status])",
					"f := i18n.T\n\t\t_ = f(\"side.status.\" + \"typo\")\n\t\tst := i18n.T(activityStatusKey[x.Status])")
			},
			want: []string{`"side.status.typo"`, "不在 Keys() 里", "render.go"},
		},
		{
			name: "AliasTier",
			file: "settings.go",
			pass: true,
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, `func tierName(t deckstore.Tier) string { return T(i18n.Key("tier." + string(t))) }`,
					"type Tier2 = deckstore.Tier\n\nfunc tierName(t Tier2) string { return T(i18n.Key(\"tier.\" + string(t))) }")
			},
		},
		{
			name: "Ellipsis",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, "st := i18n.T(activityStatusKey[x.Status])",
					"st := i18n.T(activityStatusKey[x.Status])\n\t\tst = i18n.T(i18n.Key(\"side.status.\"+\"typo\"), []any{}...)\n\t\tst = i18n.T(\"side.status.\" + \"typo\", []any{}...)")
			},
			want:   []string{"不在 Keys() 里", "render.go"},
			need:   2,
			needle: `"side.status.typo"`,
		},
		{
			name: "WrapperFuncValue",
			file: "idle.go",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, "\t}\n\tfor _, k := range layout.Keys() {",
					"\t}\n\tf := T\n\tlines[2] = f(\"idle.\" + \"typo\")\n\tfor _, k := range layout.Keys() {")
			},
			want: []string{`"idle.typo"`, "不在 Keys() 里", "idle.go"},
		},
		{
			name: "OutsidePackage",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, "st := i18n.T(activityStatusKey[x.Status])",
					"st := deckstore.ActivityTypoText()")
			},
			also: func(t *testing.T) map[string]string {
				src := replaceOnce(t, sanitizeOrig, "import (\n\t\"reflect\"\n\t\"regexp\"\n\t\"strings\"\n\t\"unicode/utf8\"\n\n\t\"github.com/Tingschen287/terminal-deck/work/internal/i18n\"\n)",
					"import (\n\t\"reflect\"\n\t\"regexp\"\n\t\"strings\"\n\t\"unicode/utf8\"\n\n\t\"github.com/Tingschen287/terminal-deck/work/internal/i18n\"\n)\n\nfunc ActivityTypoText() string { return i18n.T(\"side.status.\" + \"typo\") }")
				return map[string]string{sanitizePath: src}
			},
			want: []string{`"side.status.typo"`, "不在 Keys() 里", "sanitize.go"},
		},
		{
			name: "ConstTypo",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, "st := i18n.T(activityStatusKey[x.Status])",
					"st := i18n.T(activityStatusKey[x.Status])\n\t\tst = i18n.T(\"side.status.typoo\")")
			},
			want: []string{`"side.status.typoo"`, "不在 Keys() 里", "render.go"},
		},
		{
			name: "FormatErrorfDynamic",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, "st := i18n.T(activityStatusKey[x.Status])",
					"st := i18n.T(activityStatusKey[x.Status])\n\t\t_ = i18n.Format(i18n.Key(\"side.status.\" + x.Status))\n\t\t_ = i18n.Errorf(i18n.Key(\"act.status.\" + x.Status))")
			},
			want: []string{`"side.status." + x.Status`, `"act.status." + x.Status`, "未登记的字符串拼接", "render.go"},
		},
		{
			name: "GenericKey",
			edit: func(t *testing.T, src string) string {
				src = replaceOnce(t, src, "func TextKeys() []string {",
					"func keyAs[S ~string](s string) S { return S(s) }\n\nfunc TextKeys() []string {")
				return replaceOnce(t, src, "st := i18n.T(activityStatusKey[x.Status])",
					"st := i18n.T(keyAs[i18n.Key](\"side.status.typo\"))")
			},
			want: []string{"泛型实例化", "i18n.Key", "render.go"},
		},
		{
			name: "ParenConv",
			file: "idle.go",
			edit: func(t *testing.T, src string) string {
				src = withI18nImport(t, src)
				return beforeReturn(t, src, "\ts := \"idle.\" + \"typo\"\n\tlines[2] = T((i18n.Key)(s))\n")
			},
			want: []string{"局部变量或参数 s", "idle.go"},
		},
		{
			name: "KeyAddAssign",
			file: "idle.go",
			edit: func(t *testing.T, src string) string {
				return beforeReturn(t, src, "\tk := pageText[0]\n\tk += k\n\tlines[2] = T(k)\n")
			},
			want: []string{"复合赋值 += 的左边是 Key", "idle.go"},
		},
		{
			name: "PtrConv",
			file: "idle.go",
			edit: func(t *testing.T, src string) string {
				src = withI18nImport(t, src)
				return beforeReturn(t, src, "\ts := \"idle.\" + \"typo\"\n\tlines[2] = T(*(*i18n.Key)(&s))\n")
			},
			want: []string{"非常量转换成含有 Key 的类型", "*i18n.Key", "idle.go"},
		},
		{
			name: "GenericPtr",
			file: "idle.go",
			edit: func(t *testing.T, src string) string {
				src = withI18nImport(t, src)
				src = replaceOnce(t, src, "func IdleLines", "func reviewKeyPtr[P ~*i18n.Key](s *string) P { return P(s) }\n\nfunc IdleLines")
				return beforeReturn(t, src, "\ts := \"idle.\" + \"typo\"\n\tlines[2] = T(*reviewKeyPtr[*i18n.Key](&s))\n")
			},
			want: []string{"泛型实例化", "*i18n.Key", "idle.go"},
		},
		{
			name: "KeyBinaryAdd",
			file: "idle.go",
			edit: func(t *testing.T, src string) string {
				return beforeReturn(t, src, "\tk := pageText[0]\n\tlines[2] = T(k + k)\n")
			},
			want: []string{"二元表达式 + 的结果是 Key", "idle.go"},
		},
		{
			name: "KeySlice",
			file: "idle.go",
			edit: func(t *testing.T, src string) string {
				return beforeReturn(t, src, "\tlines[2] = T(pageText[0][1:])\n")
			},
			want: []string{"切片表达式的结果是 Key", "idle.go"},
		},
		{
			name: "NestedTypeConv",
			file: "idle.go",
			edit: func(t *testing.T, src string) string {
				src = withI18nImport(t, src)
				return beforeReturn(t, src, "\ttype keyNest struct{ K i18n.Key }\n\tb := keyNest{K: pageText[0]}\n\t_ = (*struct{ K i18n.Key })(&b)\n")
			},
			want: []string{"非常量转换成含有 Key 的类型", "i18n.Key", "idle.go"},
		},
		{
			name: "WireCompareDiscarded",
			file: "wire.go",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, "\t\tif string(k) == code {\n\t\t\tknown = true\n\t\t\tbreak\n\t\t}", "\t\t_ = string(k) == code")
			},
			want: []string{"键参数是局部变量或参数 code", "wire.go"},
		},
		{
			name: "WireCompareAfterConvert",
			file: "wire.go",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src,
					"\tknown := false\n\tfor _, k := range i18n.Keys() {\n\t\tif string(k) == code {\n\t\t\tknown = true\n\t\t\tbreak\n\t\t}\n\t}\n\tkey := i18n.Key(code)",
					"\tknown := false\n\tkey := i18n.Key(code)\n\tfor _, k := range i18n.Keys() {\n\t\tif string(k) == code {\n\t\t\tknown = true\n\t\t\tbreak\n\t\t}\n\t}")
			},
			want: []string{"键参数是局部变量或参数 code", "wire.go"},
		},
		{
			name: "WireCodeReassigned",
			file: "wire.go",
			edit: func(t *testing.T, src string) string {
				return replaceOnce(t, src, "\tknown := false\n\tfor _, k := range i18n.Keys() {", "\tknown := false\n\tcode = \"forged\"\n\tfor _, k := range i18n.Keys() {")
			},
			want: []string{"键参数是局部变量或参数 code", "wire.go"},
		},
		{
			name:    "RuntimeNet",
			file:    "idle.go",
			pass:    true,
			runtime: true,
			edit: func(t *testing.T, src string) string {
				src = replaceOnce(t, src, "import (\n\t\"fmt\"\n\n\t\"github.com/Tingschen287/terminal-deck/work/internal/layout\"\n)",
					"import (\n\t\"fmt\"\n\t\"reflect\"\n\n\t\"github.com/Tingschen287/terminal-deck/work/internal/i18n\"\n\t\"github.com/Tingschen287/terminal-deck/work/internal/layout\"\n)")
				return beforeReturn(t, src, "\ts := \"idle.\" + \"typo\"\n\tvar k i18n.Key\n\treflect.ValueOf(&k).Elem().Set(reflect.ValueOf(s).Convert(reflect.TypeOf(k)))\n\tlines[2] = T(k)\n")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			file := tc.file
			if file == "" {
				file = "render.go"
			}
			overlay := map[string]string{}
			if tc.edit != nil {
				edited := tc.edit(t, orig[file])
				if edited == orig[file] {
					t.Fatal("源码没有改动")
				}
				overlay[pathOf[file]] = edited
			}
			if tc.also != nil {
				for p, s := range tc.also(t) {
					overlay[p] = s
				}
			}
			probs, err := dynamicKeyProblems(overlay)
			blob := strings.Join(probs, "\n")
			if err != nil {
				blob = err.Error() + "\n" + blob
			}
			if tc.pass {
				if err != nil || len(probs) != 0 {
					t.Fatalf("应通过，实际：\n%s", blob)
				}
				if tc.runtime {
					assertRenderMiss(t, overlay)
				}
				return
			}
			if strings.TrimSpace(blob) == "" {
				t.Fatal("应失败，类型检查和检查器都通过了")
			}
			for _, w := range tc.want {
				if !strings.Contains(blob, w) {
					t.Fatalf("应报错含 %q，实际：\n%s", w, blob)
				}
			}
			if tc.need > 0 && strings.Count(blob, tc.needle) < tc.need {
				t.Fatalf("应至少出现 %d 次 %q，实际：\n%s", tc.need, tc.needle, blob)
			}
			if err != nil && !tc.pass {
				for _, w := range tc.want {
					if strings.Contains(err.Error(), w) && !strings.Contains(strings.Join(probs, "\n"), w) {
						t.Fatalf("失败来自类型检查，不是检查器：\n%s", blob)
					}
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

// dynamicCallProblems 对 darwin 和 linux 各做一遍完整的类型检查（n067）：_linux.go 与 _darwin.go 里的文件各只在自己的平台上
// 参与编译，只检查本机平台会让另一个平台的文件从来没被检查过（审查 1：Linux 专用文件里的错键在 Mac 上漏检）。
// 每个平台各扫一遍问题，动态取键的白名单（dynamicCallAllow、genericKeyAllow）对两个平台合起来的结果核对。
func dynamicCallProblems(src map[string]string) ([]string, error) {
	plats, err := checkModuleAll(src)
	if err != nil {
		return nil, err
	}
	var probs []string
	got, genGot := map[string]bool{}, map[string]bool{}
	for _, pp := range plats {
		p, g, gg := keyInvariantScan(pp.fset, pp.pkgs)
		probs = append(probs, p...)
		for k := range g {
			got[k] = true
		}
		for k := range gg {
			genGot[k] = true
		}
	}
	probs = append(probs, allowlistProblems(got, genGot)...)
	return dedupSorted(probs), nil
}

// dedupSorted 去重并排序：同一个文件在两个平台上都参与编译时，同一个问题会出现两次。
func dedupSorted(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
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
			bin, ok := unwrapKeyConv(call.Args[0]).(*ast.BinaryExpr)
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

// 依赖包的类型信息每个平台只建一次，读本地源码，不联网、不新增模块。
// 被测包每次用内存里的源码重新检查。FileSet 跟导入器绑在一起，所以一直复用。
// 源码导入器按 go/build.Default 找文件，所以每次检查期间把 build.Default 的 GOOS 换成要查的平台（持锁，查完还原）。
var (
	typeImpMu      sync.Mutex
	platformWorlds = map[string]*platformWorld{}
)

// typeCheckPlatforms 是类型检查要覆盖的平台。每个源文件至少在其中一个平台上被检查（TestN060TypeCheckCoversEveryFile 核对）。
var typeCheckPlatforms = []string{"darwin", "linux"}

// platformWorld 是一个平台的类型检查环境：FileSet、源码导入器，和没被改动的包的检查结果缓存。
type platformWorld struct {
	goos  string
	fset  *token.FileSet
	imp   types.Importer
	cache map[string]checkedPkg
}

// platformPkgs 是一个平台上全模块的检查结果。
type platformPkgs struct {
	goos string
	fset *token.FileSet
	pkgs []checkedPkg
}

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
	if rel == "." {
		return moduleImport
	}
	return moduleImport + "/" + filepath.ToSlash(rel)
}

func isModulePath(path string) bool {
	return path == moduleImport || strings.HasPrefix(path, moduleImport+"/")
}

// withPlatform 在 goos 平台的环境里运行 fn：持锁、把 build.Default 换成这个平台（不用 cgo，两个平台一致），fn 返回后还原。
func withPlatform(goos string, fn func(w *platformWorld) error) error {
	typeImpMu.Lock()
	defer typeImpMu.Unlock()
	saved := build.Default
	defer func() { build.Default = saved }()
	build.Default.GOOS = goos
	build.Default.CgoEnabled = false
	w := platformWorlds[goos]
	if w == nil {
		fset := token.NewFileSet()
		imp := importer.ForCompiler(fset, "source", nil)
		if imp == nil {
			return errors.New("go/importer 没有 source 导入器")
		}
		w = &platformWorld{goos: goos, fset: fset, imp: imp, cache: map[string]checkedPkg{}}
		platformWorlds[goos] = w
	}
	return fn(w)
}

// checkModuleAll 在 typeCheckPlatforms 的每个平台上各装载、各做一遍全模块的类型检查。src 的含义同 checkModule。
func checkModuleAll(src map[string]string) ([]platformPkgs, error) {
	var out []platformPkgs
	for _, goos := range typeCheckPlatforms {
		var pp platformPkgs
		if err := withPlatform(goos, func(w *platformWorld) error {
			pkgs, err := checkModule(w, src)
			if err != nil {
				return fmt.Errorf("GOOS=%s：%w", goos, err)
			}
			pp = platformPkgs{goos: goos, fset: w.fset, pkgs: pkgs}
			return nil
		}); err != nil {
			return nil, err
		}
		out = append(out, pp)
	}
	return out, nil
}

type checkedPkg struct {
	dir   string
	files []parsedFile
	pkg   *types.Package
	info  *types.Info
	enums map[*types.Var]enumInfo
}

func modulePkgDirs() ([]string, error) {
	root := moduleRoot()
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if path != root && (name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".")) {
			return filepath.SkipDir
		}
		ents, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, e := range ents {
			n := e.Name()
			if !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
				dirs = append(dirs, path)
				break
			}
		}
		return nil
	})
	sort.Strings(dirs)
	return dirs, err
}

func pkgSetProblems(dirs []string) []string {
	if len(dirs) == 0 {
		return []string{"枚举到的非测试包是 0 个"}
	}
	have := map[string]bool{}
	for _, d := range dirs {
		have[filepath.Base(d)] = true
	}
	var probs []string
	for _, name := range []string{"deckui", "layout", "panels"} {
		if !have[name] {
			probs = append(probs, fmt.Sprintf("枚举到的包里没有 %s（共 %d 个）", name, len(dirs)))
		}
	}
	return probs
}

type pkgGroup struct {
	dir     string
	path    string
	files   []parsedFile
	imports []string
}

func parseModule(fset *token.FileSet, src map[string]string) ([]*pkgGroup, error) {
	dirs, err := modulePkgDirs()
	if err != nil {
		return nil, err
	}
	if probs := pkgSetProblems(dirs); len(probs) > 0 {
		return nil, errors.New(strings.Join(probs, "\n"))
	}
	var out []*pkgGroup
	for _, dir := range dirs {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		var names []string
		for _, e := range ents {
			n := e.Name()
			if !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
				// n067：一次类型检查只装载这个平台（build.Default.GOOS，withPlatform 设的）会编进去的文件。
				// _linux.go 与 _darwin.go 各自声明同名函数，一起检查会报重复声明；所以每个平台各检查一遍（checkModuleAll）。
				// 文字扫描类的守卫（parseCovered）仍然看全部文件，不受影响
				if ok, err := build.Default.MatchFile(dir, n); err == nil && !ok {
					continue
				}
				names = append(names, n)
			}
		}
		sort.Strings(names)
		if len(names) == 0 {
			continue // 这个平台上没有任何文件会编进这个包
		}
		g := &pkgGroup{dir: dir, path: pkgImportPath(dir)}
		seenImp := map[string]bool{}
		for _, n := range names {
			path := filepath.Join(dir, n)
			var body any
			if s, ok := src[path]; ok {
				body = s
			}
			f, err := parser.ParseFile(fset, path, body, 0)
			if err != nil {
				return nil, fmt.Errorf("解析 %s：%w", path, err)
			}
			g.files = append(g.files, parsedFile{path: path, file: f})
			for _, im := range f.Imports {
				p, err := strconv.Unquote(im.Path.Value)
				if err != nil || seenImp[p] {
					continue
				}
				seenImp[p] = true
				g.imports = append(g.imports, p)
			}
		}
		out = append(out, g)
	}
	return out, nil
}

func topoGroups(gs []*pkgGroup) ([]*pkgGroup, error) {
	by := map[string]*pkgGroup{}
	indeg := map[string]int{}
	users := map[string][]string{}
	for _, g := range gs {
		if _, ok := by[g.path]; ok {
			return nil, fmt.Errorf("导入路径重复：%s", g.path)
		}
		by[g.path] = g
		indeg[g.path] = 0
	}
	for _, g := range gs {
		seen := map[string]bool{}
		for _, imp := range g.imports {
			if _, ok := by[imp]; !ok || seen[imp] || imp == g.path {
				continue
			}
			seen[imp] = true
			users[imp] = append(users[imp], g.path)
			indeg[g.path]++
		}
	}
	var ready []string
	for p, n := range indeg {
		if n == 0 {
			ready = append(ready, p)
		}
	}
	var out []*pkgGroup
	for len(ready) > 0 {
		sort.Strings(ready)
		p := ready[0]
		ready = ready[1:]
		out = append(out, by[p])
		for _, u := range users[p] {
			indeg[u]--
			if indeg[u] == 0 {
				ready = append(ready, u)
			}
		}
	}
	if len(out) != len(gs) {
		return nil, fmt.Errorf("包依赖有环，排好 %d / %d", len(out), len(gs))
	}
	return out, nil
}

type overlayImporter struct {
	base types.Importer
	have map[string]*types.Package
}

func (o *overlayImporter) Import(path string) (*types.Package, error) {
	if p := o.have[path]; p != nil {
		return p, nil
	}
	if isModulePath(path) {
		return nil, fmt.Errorf("内部包 %s 还没按依赖顺序检查", path)
	}
	return o.base.Import(path)
}

func (o *overlayImporter) ImportFrom(path, dir string, mode types.ImportMode) (*types.Package, error) {
	if p := o.have[path]; p != nil {
		return p, nil
	}
	if isModulePath(path) {
		return nil, fmt.Errorf("内部包 %s 还没按依赖顺序检查", path)
	}
	if from, ok := o.base.(types.ImporterFrom); ok {
		return from.ImportFrom(path, dir, mode)
	}
	return o.base.Import(path)
}

// moduleCheckCache 存没被改动的包的类型检查结果（返工 4，建议 1）。反例每次只改 deckd/agents.go 这类一个文件，
// 其余包重新装载、类型检查是纯重复：21 个参数反例 + 32 个动态键反例各做一遍，占了这组测试的大半耗时。
// 只缓存「自己没有被覆盖、依赖的模块内部包也都没被覆盖」的包，所以结果与每次重做逐字相同。
// 缓存在 platformWorld 里（每个平台一份），调用都在 typeImpMu 里，不用另外加锁。
func checkModule(w *platformWorld, src map[string]string) ([]checkedPkg, error) {
	fset, base := w.fset, w.imp
	groups, err := parseModule(fset, src)
	if err != nil {
		return nil, err
	}
	order, err := topoGroups(groups)
	if err != nil {
		return nil, err
	}
	over := &overlayImporter{base: base, have: map[string]*types.Package{}}
	var out []checkedPkg
	dirty := map[string]bool{}
	for _, g := range order {
		changed := false
		for _, pf := range g.files {
			if _, ok := src[pf.path]; ok {
				changed = true
			}
		}
		for _, imp := range g.imports {
			if dirty[imp] {
				changed = true
			}
		}
		if changed {
			dirty[g.path] = true
		}
		useCache := !changed
		if useCache {
			if cp, ok := w.cache[g.path]; ok {
				over.have[g.path] = cp.pkg
				out = append(out, cp)
				continue
			}
		}
		asts := make([]*ast.File, len(g.files))
		for i, pf := range g.files {
			asts[i] = pf.file
		}
		info := &types.Info{
			Types:     make(map[ast.Expr]types.TypeAndValue),
			Defs:      make(map[*ast.Ident]types.Object),
			Uses:      make(map[*ast.Ident]types.Object),
			Instances: make(map[*ast.Ident]types.Instance),
		}
		var msgs []string
		conf := types.Config{
			Importer: over,
			Error: func(e error) {
				if len(msgs) < 8 {
					msgs = append(msgs, e.Error())
				}
			},
		}
		pkg, err := conf.Check(g.path, fset, asts, info)
		if err != nil {
			return nil, fmt.Errorf("类型检查 %s：%v\n%s", g.path, err, strings.Join(msgs, "\n"))
		}
		over.have[g.path] = pkg
		cp := checkedPkg{
			dir: g.dir, files: g.files, pkg: pkg, info: info,
			enums: enumsIn(info, pkg, asts),
		}
		if useCache {
			w.cache[g.path] = cp
		}
		out = append(out, cp)
	}
	return out, nil
}

// keyInvariantScan 扫一个平台的检查结果：返回问题，以及这个平台上看到的动态取键、泛型实例化的站点（白名单的核对要对
// 所有平台合起来做，见 allowlistProblems）。
func keyInvariantScan(fset *token.FileSet, pkgs []checkedPkg) (probs []string, got, genGot map[string]bool) {
	var dirs []string
	for _, cp := range pkgs {
		dirs = append(dirs, cp.dir)
	}
	probs = pkgSetProblems(dirs)
	known := map[string]bool{}
	for _, k := range i18n.Keys() {
		known[string(k)] = true
	}
	genAllow := map[string]bool{}
	for _, s := range genericKeyAllow {
		genAllow[s] = true
	}
	got = map[string]bool{}
	genGot = map[string]bool{}
	for _, cp := range pkgs {
		w := bindWorld{info: cp.info, pkg: cp.pkg, enums: cp.enums}
		outside := cp.pkg.Path() != i18nImport
		for _, pf := range cp.files {
			base := filepath.Base(pf.path)
			walkStack(pf.file, func(n ast.Node, stack []ast.Node) {
				if outside {
					if as, ok := n.(*ast.AssignStmt); ok && as.Tok == token.ADD_ASSIGN {
						for _, lhs := range as.Lhs {
							if tv, ok := cp.info.Types[lhs]; ok && isI18nKey(tv.Type) {
								probs = append(probs, fmt.Sprintf("%s：复合赋值 += 的左边是 Key", fset.Position(lhs.Pos())))
							}
						}
					}
				}
				e, ok := n.(ast.Expr)
				if !ok {
					return
				}
				if isConstKey(cp.info, e) && !isConstKey(cp.info, nearestExpr(stack)) {
					val, okVal := keyString(cp.info.Types[e].Value)
					if !okVal || !known[val] {
						expr, err := printExpr(fset, e)
						if err != nil {
							probs = append(probs, err.Error())
						} else {
							probs = append(probs, fmt.Sprintf("%s：常量键 %q 不在 Keys() 里（%s）", fset.Position(e.Pos()), val, expr))
						}
					}
				}
				if !outside {
					return
				}
				if bin, ok := e.(*ast.BinaryExpr); ok && bin.Op == token.ADD {
					if tv, ok := cp.info.Types[e]; ok && isI18nKey(tv.Type) && tv.Value == nil {
						probs = append(probs, fmt.Sprintf("%s：二元表达式 + 的结果是 Key", fset.Position(e.Pos())))
					}
				}
				if _, ok := e.(*ast.SliceExpr); ok {
					if tv, ok := cp.info.Types[e]; ok && isI18nKey(tv.Type) {
						probs = append(probs, fmt.Sprintf("%s：切片表达式的结果是 Key", fset.Position(e.Pos())))
					}
				}
				call, ok := e.(*ast.CallExpr)
				if !ok {
					return
				}
				target, op, ok := conversionTarget(cp.info, call)
				if !ok {
					return
				}
				pos := fset.Position(call.Pos())
				// 目标恰是 Key 的常量交给上面的常量规则。其它含 Key 的类型没有常量转换可放。
				if isI18nKey(target) && cp.info.Types[call].Value != nil {
					return
				}
				if !isI18nKey(target) {
					if cp.info.Types[call].Value == nil {
						probs = append(probs, fmt.Sprintf("%s：非常量转换成含有 Key 的类型 %s", pos, shortType(target)))
					}
					return
				}
				fn := nearestFunc(stack)
				expr, err := printExpr(fset, op)
				if err != nil {
					probs = append(probs, err.Error())
					return
				}
				if fn == "" {
					probs = append(probs, fmt.Sprintf("%s：非常量转换不在函数里（%s）", pos, expr))
					return
				}
				if wireCodeShape(cp.info, op, stack) {
					got[base+" · "+fn+" · "+expr] = true
					return
				}
				if okShape, why := w.classify(op); !okShape {
					probs = append(probs, fmt.Sprintf("%s：%s（%s）", pos, why, expr))
					return
				}
				got[base+" · "+fn+" · "+expr] = true
			})
		}
		if !outside {
			continue
		}
		for id, inst := range cp.info.Instances {
			if inst.TypeArgs == nil {
				continue
			}
			var hit []string
			for i := 0; i < inst.TypeArgs.Len(); i++ {
				arg := inst.TypeArgs.At(i)
				if mentionsKey(arg) {
					hit = append(hit, shortType(arg))
				}
			}
			if len(hit) == 0 {
				continue
			}
			fileBase := filepath.Base(fset.Position(id.Pos()).Filename)
			fn := funcNameAt(cp.files, id.Pos())
			site := fileBase + " · " + fn + " · " + id.Name
			if !genAllow[site] {
				probs = append(probs, fmt.Sprintf("%s：泛型实例化的类型实参含有 Key（%s）", fset.Position(id.Pos()), strings.Join(hit, ", ")))
				continue
			}
			genGot[site] = true
		}
	}
	return probs, got, genGot
}

// allowlistProblems 把两个平台合起来看到的动态取键站点与白名单互相核对。
func allowlistProblems(got, genGot map[string]bool) []string {
	var probs []string
	want := map[string]bool{}
	for _, s := range dynamicCallAllow {
		want[s] = true
		if !got[s] {
			probs = append(probs, "dynamicCallAllow 里的 "+s+" 已经不是动态取键")
		}
	}
	for s := range got {
		if !want[s] {
			probs = append(probs, "新的动态取键 "+s+"：先确认键来自已枚举的集合，再写进 dynamicCallAllow")
		}
	}
	for _, s := range genericKeyAllow {
		if !genGot[s] {
			probs = append(probs, "genericKeyAllow 里的 "+s+" 已经不是泛型实例化")
		}
	}
	return probs
}

func walkStack(root ast.Node, fn func(n ast.Node, stack []ast.Node)) {
	var stack []ast.Node
	ast.Inspect(root, func(n ast.Node) bool {
		if n == nil {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			return false
		}
		fn(n, stack)
		stack = append(stack, n)
		return true
	})
}

func isConstKey(info *types.Info, e ast.Expr) bool {
	if info == nil || e == nil {
		return false
	}
	tv, ok := info.Types[e]
	return ok && tv.Value != nil && isI18nKey(tv.Type)
}

func nearestExpr(stack []ast.Node) ast.Expr {
	for i := len(stack) - 1; i >= 0; i-- {
		if e, ok := stack[i].(ast.Expr); ok {
			return e
		}
	}
	return nil
}

func nearestFunc(stack []ast.Node) string {
	for i := len(stack) - 1; i >= 0; i-- {
		fd, ok := stack[i].(*ast.FuncDecl)
		if ok && fd.Name != nil {
			return fd.Name.Name
		}
	}
	return ""
}

func keyString(v constant.Value) (string, bool) {
	if v == nil || v.Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(v), true
}

// conversionTarget 认出一次类型转换。括号先去掉。Fun 在类型信息里必须是类型，不是函数。
// 目标类型含有 Key 才返回真。操作数原样返回，调用方再分类。
func conversionTarget(info *types.Info, call *ast.CallExpr) (types.Type, ast.Expr, bool) {
	if info == nil || call == nil || len(call.Args) != 1 || call.Ellipsis.IsValid() {
		return nil, nil, false
	}
	fun := ast.Unparen(call.Fun)
	tv, ok := info.Types[fun]
	if !ok || !tv.IsType() {
		tv, ok = info.Types[call.Fun]
	}
	if !ok || !tv.IsType() || tv.Type == nil || !mentionsKey(tv.Type) {
		return nil, nil, false
	}
	return tv.Type, call.Args[0], true
}

// mentionsKey 为真表示 t 里递归出现了 i18n.Key。
// 看：Key 本身、指针、切片、数组、map、chan、函数签名、结构体字段。别名先 Unalias。
func mentionsKey(t types.Type) bool {
	return mentionKey(t, map[types.Type]bool{})
}

func mentionKey(t types.Type, seen map[types.Type]bool) bool {
	if t == nil {
		return false
	}
	t = types.Unalias(t)
	if seen[t] {
		return false
	}
	seen[t] = true
	if isI18nKey(t) {
		return true
	}
	switch u := t.Underlying().(type) {
	case *types.Pointer:
		return mentionKey(u.Elem(), seen)
	case *types.Slice:
		return mentionKey(u.Elem(), seen)
	case *types.Array:
		return mentionKey(u.Elem(), seen)
	case *types.Map:
		return mentionKey(u.Key(), seen) || mentionKey(u.Elem(), seen)
	case *types.Chan:
		return mentionKey(u.Elem(), seen)
	case *types.Signature:
		if u.Recv() != nil && mentionKey(u.Recv().Type(), seen) {
			return true
		}
		return mentionTuple(u.Params(), seen) || mentionTuple(u.Results(), seen)
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			if mentionKey(u.Field(i).Type(), seen) {
				return true
			}
		}
	case *types.Union:
		for i := 0; i < u.Len(); i++ {
			if mentionKey(u.Term(i).Type(), seen) {
				return true
			}
		}
	}
	return false
}

func mentionTuple(t *types.Tuple, seen map[types.Type]bool) bool {
	if t == nil {
		return false
	}
	for i := 0; i < t.Len(); i++ {
		if mentionKey(t.At(i).Type(), seen) {
			return true
		}
	}
	return false
}

func shortType(t types.Type) string {
	return types.TypeString(t, func(p *types.Package) string {
		if p == nil {
			return ""
		}
		return p.Name()
	})
}

func funcNameAt(files []parsedFile, pos token.Pos) string {
	for _, pf := range files {
		for _, d := range pf.file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Name == nil {
				continue
			}
			if fd.Pos() <= pos && pos < fd.End() {
				return fd.Name.Name
			}
		}
	}
	return ""
}

func callFunIdent(call *ast.CallExpr) *ast.Ident {
	if call == nil {
		return nil
	}
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		return fun
	case *ast.SelectorExpr:
		return fun.Sel
	default:
		return nil
	}
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
	var elem types.Type
	switch u := t.Underlying().(type) {
	case *types.Map:
		elem = u.Elem()
	case *types.Slice:
		elem = u.Elem()
	case *types.Array:
		elem = u.Elem()
	default:
		return false
	}
	return isPlainString(elem) || isI18nKey(elem)
}

func isI18nKey(t types.Type) bool {
	if t == nil {
		return false
	}
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Name() == "Key" && obj.Pkg() != nil && obj.Pkg().Path() == i18nImport
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

// wireCodeShape 认「线路码，转换前已用 Keys() 校验」。
// 只认一个函数里的这一处形状。对不齐就返回 false，调用方按原分类报错。
func wireCodeShape(info *types.Info, op ast.Expr, stack []ast.Node) bool {
	id, ok := unparen(op).(*ast.Ident)
	if !ok {
		return false
	}
	var fn *ast.FuncDecl
	for i := len(stack) - 1; i >= 0; i-- {
		if _, isLit := stack[i].(*ast.FuncLit); isLit {
			return false
		}
		fd, ok := stack[i].(*ast.FuncDecl)
		if ok {
			fn = fd
			break
		}
	}
	if fn == nil || fn.Body == nil || fn.Type == nil || fn.Type.Params == nil {
		return false
	}
	codeObj := objOf(info, id)
	if codeObj == nil {
		return false
	}
	param := false
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			if objOf(info, name) == codeObj {
				param = true
			}
		}
	}
	if !param {
		return false
	}
	// 参数被重新赋值后，前面的比较不再能证明转换用的是原值。
	if codeAssigned(info, fn.Body, codeObj) {
		return false
	}
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found || n == nil {
			return false
		}
		if _, isLit := n.(*ast.FuncLit); isLit {
			return false
		}
		rng, ok := n.(*ast.RangeStmt)
		if !ok || !isI18nKeysCall(info, rng.X) {
			return true
		}
		val, ok := rng.Value.(*ast.Ident)
		if !ok {
			return true
		}
		valObj := objOf(info, val)
		if valObj == nil || rng.Body == nil {
			return true
		}
		ast.Inspect(rng.Body, func(m ast.Node) bool {
			if found || m == nil {
				return false
			}
			if _, isLit := m.(*ast.FuncLit); isLit {
				return false
			}
			ifs, ok := m.(*ast.IfStmt)
			if !ok {
				return true
			}
			// 比较必须发生在转换之前。写在转换后面的 if 不算。
			if !ifs.Pos().IsValid() || !op.Pos().IsValid() || ifs.Pos() >= op.Pos() {
				return true
			}
			if !condHasWireEq(info, ifs.Cond, valObj, codeObj) {
				return true
			}
			if !ifBodyUses(ifs.Body) {
				return true
			}
			found = true
			return false
		})
		return !found
	})
	return found
}

// condHasWireEq 认 if 条件里的 string(k) == code。条件可以是 && 或 ||。
func condHasWireEq(info *types.Info, e ast.Expr, valObj, codeObj types.Object) bool {
	bin, ok := unparen(e).(*ast.BinaryExpr)
	if !ok {
		return false
	}
	switch bin.Op {
	case token.EQL:
		return wireStringEq(info, bin, valObj, codeObj)
	case token.LAND, token.LOR:
		return condHasWireEq(info, bin.X, valObj, codeObj) || condHasWireEq(info, bin.Y, valObj, codeObj)
	default:
		return false
	}
}

// ifBodyUses 要求 if 的函数体真的用上比较结果：赋值、return 或 break/continue。
func ifBodyUses(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	used := false
	ast.Inspect(body, func(n ast.Node) bool {
		if used || n == nil {
			return false
		}
		if _, isLit := n.(*ast.FuncLit); isLit {
			return false
		}
		switch n.(type) {
		case *ast.AssignStmt, *ast.ReturnStmt, *ast.BranchStmt:
			used = true
			return false
		}
		return true
	})
	return used
}

// codeAssigned 看函数体里有没有给这个参数赋值。
func codeAssigned(info *types.Info, body ast.Node, codeObj types.Object) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found || n == nil {
			return false
		}
		if _, isLit := n.(*ast.FuncLit); isLit {
			return false
		}
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range as.Lhs {
			if identSame(info, lhs, codeObj) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func isI18nKeysCall(info *types.Info, e ast.Expr) bool {
	call, ok := unparen(e).(*ast.CallExpr)
	if !ok || len(call.Args) != 0 || call.Ellipsis.IsValid() {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	pn, ok := objOf(info, pkg).(*types.PkgName)
	if !ok || pn.Imported() == nil || pn.Imported().Path() != i18nImport {
		return false
	}
	fn, ok := objOf(info, sel.Sel).(*types.Func)
	return ok && fn.Name() == "Keys" && fn.Pkg() != nil && fn.Pkg().Path() == i18nImport
}

func wireStringEq(info *types.Info, bin *ast.BinaryExpr, valObj, codeObj types.Object) bool {
	left := predeclaredStringOf(info, bin.X, valObj) && identSame(info, bin.Y, codeObj)
	right := predeclaredStringOf(info, bin.Y, valObj) && identSame(info, bin.X, codeObj)
	return left || right
}

func predeclaredStringOf(info *types.Info, e ast.Expr, argObj types.Object) bool {
	call, ok := unparen(e).(*ast.CallExpr)
	if !ok || len(call.Args) != 1 || call.Ellipsis.IsValid() {
		return false
	}
	fun, ok := call.Fun.(*ast.Ident)
	if !ok || objOf(info, fun) != types.Universe.Lookup("string") {
		return false
	}
	return identSame(info, call.Args[0], argObj)
}

func identSame(info *types.Info, e ast.Expr, obj types.Object) bool {
	id, ok := unparen(e).(*ast.Ident)
	return ok && obj != nil && objOf(info, id) == obj
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

// unwrapKeyConv 只给档位前缀的语法扫描用。真正的转换认 go/types。
func unwrapKeyConv(e ast.Expr) ast.Expr {
	call, ok := unparen(e).(*ast.CallExpr)
	if !ok || len(call.Args) != 1 || call.Ellipsis.IsValid() {
		return e
	}
	id := callFunIdent(call)
	if id == nil || id.Name != "Key" {
		return e
	}
	return call.Args[0]
}

func unparen(e ast.Expr) ast.Expr {
	if e == nil {
		return nil
	}
	return ast.Unparen(e)
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

func withI18nImport(t *testing.T, src string) string {
	t.Helper()
	const old = "\t\"github.com/Tingschen287/terminal-deck/work/internal/layout\""
	const neu = "\t\"github.com/Tingschen287/terminal-deck/work/internal/i18n\"\n\t\"github.com/Tingschen287/terminal-deck/work/internal/layout\""
	return replaceOnce(t, src, old, neu)
}

func beforeReturn(t *testing.T, src, stmt string) string {
	t.Helper()
	return replaceOnce(t, src, "\treturn lines\n", stmt+"\treturn lines\n")
}

// assertRenderMiss 用临时目录里的源码跑英文渲染。仓库里的文件不动。
// 静态检查按威胁模型不管 reflect。渲染测试必须因为缺键记录不为零而失败。
func assertRenderMiss(t *testing.T, overlay map[string]string) {
	t.Helper()
	if len(overlay) != 1 {
		t.Fatalf("运行时反例应只改一个文件，实际 %d 个", len(overlay))
	}
	dir := t.TempDir()
	repl := map[string]string{}
	for srcPath, body := range overlay {
		dst := filepath.Join(dir, filepath.Base(srcPath))
		if err := os.WriteFile(dst, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		repl[srcPath] = dst
	}
	buf, err := json.Marshal(map[string]any{"Replace": repl})
	if err != nil {
		t.Fatal(err)
	}
	ov := filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(ov, buf, 0o600); err != nil {
		t.Fatal(err)
	}
	// go 可执行文件：按当前测试进程的 GOROOT 找（以前写死 /home/linux/.local/go/bin/go，换机器就找不到；n067）
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBin); err != nil {
		goBin = "go"
	}
	cmd := exec.Command(goBin, "test", "-overlay", ov, "-count=1", "-timeout", "180s", "-run", "^TestEnglishUIHasNoHan$", "./internal/deckui")
	cmd.Dir = moduleRoot()
	env := make([]string, 0, len(os.Environ())+3)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "GOTOOLCHAIN=") || strings.HasPrefix(e, "GOPROXY=") || strings.HasPrefix(e, "GOFLAGS=") {
			continue
		}
		env = append(env, e)
	}
	cmd.Env = append(env, "GOTOOLCHAIN=local", "GOPROXY=off", "GOFLAGS=-mod=readonly")
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err == nil {
		t.Fatalf("渲染测试应因缺键失败，实际通过：\n%s", text)
	}
	if !strings.Contains(text, "渲染用了表外的键") || !strings.Contains(text, "idle.typo") {
		t.Fatalf("失败原因不是缺键记录：\n%s", text)
	}
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
			zh, zhOK := i18n.TextZH(i18n.Key(k))
			en, enOK := i18n.TextEN(i18n.Key(k))
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

// enViewKey 是英文渲染里每个主要画面对上的一条译文。期望值从英文表取，不写死。
// 像 idle.typo 这种纯 ASCII 错键过不了「没有汉字」，但过不了这里。
var enViewKey = map[string]i18n.Key{
	"sidebar":          "side.status.waiting",
	"idle-wide":        "idle.hint",
	"idle-narrow":      "idle.hint",
	"tabs":             "tabs.taken",
	"tabs-taken":       "tabs.taken",
	"tabs-pending":     "tabs.pending",
	"tabs-readonly":    "tabs.readonly",
	"tabs-empty":       "tabs.empty",
	"settings-1":       "project.tiers",
	"settings-2":       "agents.add",
	"settings-3":       "trust.title",
	"sub-agent":        "agent.keys",
	"sub-add-loading":  "add.loading",
	"sub-add":          "add.title",
	"sub-tier":         "tierview.keys",
	"form-browse":      "form.browse.title",
	"form-confirm":     "form.confirm.title",
	"quota":            "quota.title",
	"quota-empty":      "quota.empty",
	"quota-err":        "ui.loadfail",
	"decisions":        "decide.kind.cost",
	"progress":         "progress.accepted",
	"progress-nograph": "progress.nograph",
	"notify-empty":     "notify.empty",
	"notify":           "notify.title",
	"activity":         "act.role.main",
	"activity-empty":   "act.empty",
	"layout-base":      "layout.conf.base",
	"layout-keys":      "layout.conf.keys",
}

// enViewSkip 是渲染了、但不能用一条无占位符的英文当子串去对的画面。必须写理由。没登记的画面让测试失败。
var enViewSkip = map[string]string{
	"layout-err": "layout.err.restore 带占位符，英文整句对不上子串；中文原文由这条错误自己的 Error() 保住",
}

// TestEnglishUIHasNoHan 在英文下渲染本段的主要画面。
// 英文产品名一律 Anpai，不出现「安排」。
// 「安排」只允许出现在中文界面、而且 BrandFor 的列数 ≥ 10 的时候（n058）。本测试是英文，所以一处都不允许。
// 会话标题、项目名、模型 id、决定摘要、通知正文、从外面传进来的报错保持原样，夹具里这些都用英文，避免和界面文字混在一起。
func TestEnglishUIHasNoHan(t *testing.T) {
	i18n.SetLang(i18n.EN)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	renderStage1(t, true)
}

// TestChineseUINoKeyMiss 用同一组画面走中文。缺键记录必须是空的。
func TestChineseUINoKeyMiss(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	renderStage1(t, false)
}

// stage1Capture 不为 nil 时，renderStage1 把每个画面的纯文字也交给它（段 3：运行中切换的测试拿来前后对比）。
var stage1Capture func(name, text string)

func renderStage1(t *testing.T, english bool) {
	t.Helper()
	stop := i18n.RecordMisses()
	t.Cleanup(func() { stop() })

	var got []string
	type snap struct{ name, text string }
	var snaps []snap
	keep := func(name, s string) {
		plain := ansi.Strip(s)
		if english {
			addView(t, &got, name, plain)
		} else {
			got = append(got, name)
		}
		snaps = append(snaps, snap{name, plain})
		if stage1Capture != nil {
			stage1Capture(name, plain)
		}
	}

	sb := &Sidebar{Data: englishData(), Mode: "none"}
	f := sb.Render(80, 80)
	keep("sidebar", strings.Join(f.Lines, "\n"))

	wide, narrow := IdleLines(80), IdleLines(8)
	if english && (wide[0] != "Anpai" || narrow[0] != "Anpai") {
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

	if miss := stop(); len(miss) != 0 {
		t.Fatalf("渲染用了表外的键：%v", miss)
	}
	if english {
		for _, v := range snaps {
			key, ok := enViewKey[v.name]
			if !ok {
				if enViewSkip[v.name] == "" {
					t.Errorf("%s 渲染了，但没有登记英文样本，也没有写例外理由", v.name)
				}
				continue
			}
			want, ok := i18n.TextEN(key)
			if !ok {
				t.Errorf("英文表没有 %s", key)
				continue
			}
			if !strings.Contains(v.text, want) {
				t.Errorf("%s 应含 %s 的英文 %q", v.name, key, want)
			}
		}
	}
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
