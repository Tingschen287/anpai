package agentdeck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 这一组测试直接对着独立评审给出的绕过法写。
//
// attempt 12 的核对全按文本走，评审举了三个反例，每个都不需要技巧：
//
//  1. 原始字符串里放一行顶格的 `func Fake() {`，按文本切的函数体在这里断掉，
//     后面的禁词就搜不到了——**漏报**，比误报严重。
//  2. `"send" + "-keys"`：源码里不存在连着的那个词，按文本扫的守边测试看不见。
//  3. 另起一个名字含 `keyInjectionCommands` 的变量，整行被跳过规则放过。
//
// 换成语法树之后这三条应该都不成立。**这几条测试就是用来证明这件事的**，
// 不是摆设：把 upstream_ast_test.go 里的解析换回文本切分，它们会红。

// fixture 把一段源码写进临时文件并解析。
func fixture(t *testing.T, src string) (*token.FileSet, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.go")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return token.NewFileSet(), path
}

// 反例 1：原始字符串里的顶格 func 不得截断函数体。
func TestFuncBodyIsNotFooledByRawStringsThatLookLikeCode(t *testing.T) {
	const src = "package p\n" +
		"\n" +
		"func Target() {\n" +
		"\tconst text = `\n" +
		"func Fake() {\n" +
		"`\n" +
		"\trun(\"send-keys\")\n" +
		"}\n" +
		"\n" +
		"func After() {}\n"

	fset, path := fixture(t, src)
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	fn := findFunc(f, "Target")
	if fn == nil {
		t.Fatal("语法树里找不到 Target")
	}
	// 按文本切会在原始字符串里那行 func Fake 处结束，漏掉后面的 run("send-keys")。
	var found bool
	for _, s := range stringsIn(fn) {
		if s == "send-keys" {
			found = true
		}
	}
	if !found {
		t.Error("Target 的函数体里有 send-keys，却没搜到——原始字符串又把函数体截断了")
	}
	// 顺带确认那个假函数没被当成真的。
	if findFunc(f, "Fake") != nil {
		t.Error("原始字符串里的 func Fake 被当成了真的函数声明")
	}
}

// 反例 1 的另一半：所属函数的判定也不能被原始字符串骗。
func TestEnclosingFuncIsNotFooledByRawStrings(t *testing.T) {
	const src = "package p\n" +
		"\n" +
		"func Real() {\n" +
		"\tconst text = `\n" +
		"func Fake() {\n" +
		"`\n" +
		"\tmarker()\n" +
		"}\n"

	fset, path := fixture(t, src)
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	markerLine := 0
	for i, line := range strings.Split(src, "\n") {
		if strings.Contains(line, "marker()") {
			markerLine = i + 1
		}
	}
	if markerLine == 0 {
		t.Fatal("测试自己写坏了：找不到 marker 那一行")
	}
	if got := enclosingFuncOf(f, fset, markerLine); got != "Real" {
		t.Errorf("marker() 在 Real 里，却判成了 %q——往上找最近的 func 又上当了", got)
	}
}

// 反例 2：拼出来的字符串要能算出来。
func TestConcatenatedStringsAreFolded(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{`package p
var a = "send" + "-keys"`, "send-keys"},
		{`package p
var a = "send" + "-" + "keys"`, "send-keys"},
		{`package p
var a = ("kill" + "-session")`, "kill-session"},
	}
	for _, c := range cases {
		fset, path := fixture(t, c.src)
		f, err := parser.ParseFile(fset, path, c.src, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := constStringDecl(f, "a")
		if !ok {
			t.Errorf("%q 算不出常量值", c.src)
			continue
		}
		if got != c.want {
			t.Errorf("%q 算出来是 %q，应该是 %q", c.src, got, c.want)
		}
	}
}

// 常量折叠有盲区，这条把盲区列出来。
//
// **它不是安全保证，别当保证读。** 独立评审批评过它的上一版：
// 断言「Sprintf 算不出来」只是固化了求值器不处理 CallExpr 这个实现事实，
// 测试会长期稳定通过，却什么也没保护——反而把一种已知绕过写成了预期行为。
//
// 保留它的理由变了：**盲区得有人记着**。哪天求值器开始处理某一种，
// 这条会红，提醒人去看边界是不是可以收窄。
// 真正拦住这些构造的是 TestCommandArgumentsAreBuiltFromConstantsOnly——
// 它盯的是「算出来的字符串能不能成为跑出去的命令参数」，那才是出口。
func TestConstantFolderKnowsItsBlindSpots(t *testing.T) {
	const src = `package p

import "fmt"

var a = fmt.Sprintf("send-%s", "keys")`

	fset, path := fixture(t, src)
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := constStringDecl(f, "a"); ok {
		t.Errorf("求值器现在能算 Sprintf 了（得到 %q）——"+
			"盲区清单该更新，顺便看看命令参数那条边界能不能收窄", got)
	}
}

// 命令参数只许由常量和函数入参构成。
//
// 这条测试的上一版被独立评审用题目里给的写法原样绕过了：
//
//	evil := string([]byte{115, 101, 110, 100, 45, 107, 101, 121, 115})
//	args = append(args, evil, "-t", exactTarget(tmuxSession), "AGENTDECK_INSTANCE_ID")
//
// 两条守边测试都 PASS，而运行时那个值就是替人敲键的子命令，且确实进了
// exec.CommandContext。原因有四个，这一轮四个都堵：
//
//  1. 只从 builder 的直接参数收集 roots——`evil` 不是直接参数，它的赋值没人追。
//     现在**迭代到不动点**：凡是给 roots 赋过值的表达式里出现的标识符，
//     也进 roots，再追它们的赋值。
//  2. 普通标识符无条件放行。现在只放行函数入参、追踪过的 root、包级声明；
//     来路不明的报错。
//  3. `SelectorExpr` 无条件放行（注释写着「由类型和调用方负责」，
//     其实没有任何检查）。现在只放行接收者与入参的字段读取。
//  4. helper 白名单只按名字匹配。现在白名单资格绑定到**本包里那一个声明**，
//     本包找不到这个函数就不认。
//
// 堵不住的那部分照旧说明白：跨函数的数据流不追，函数入参的值由调用方负责。
func TestCommandArgumentsAreBuiltFromConstantsOnly(t *testing.T) {
	builders := map[string]bool{
		"exec.Command": true, "exec.CommandContext": true, "interactiveCommand": true,
	}
	// append 是内建，只拼切片不造字符串；exactTarget 必须是本包声明的那一个。
	localHelpers := map[string]bool{"exactTarget": true}
	builtinHelpers := map[string]bool{"append": true}

	files, fset := packageASTs(t)
	declared := packageDeclNames(files)
	for name := range localHelpers {
		if !declared.funcs[name] {
			t.Errorf("白名单里的 %s 在本包里找不到声明——"+
				"白名单资格要绑到具体那一个函数，不能只认名字", name)
		}
	}

	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var builderCalls []*ast.CallExpr
			ast.Inspect(fn, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok && builders[calleeName(call.Fun)] {
					builderCalls = append(builderCalls, call)
				}
				return true
			})
			if len(builderCalls) == 0 {
				continue
			}
			params := paramNames(fn)

			// roots：所有可能流进命令参数的本地名字，迭代到不动点。
			roots := map[string]bool{}
			var direct []ast.Expr
			for _, call := range builderCalls {
				first := 0
				if calleeName(call.Fun) == "exec.CommandContext" {
					first = 1 // 第一个参数是 context，不是命令的一部分
				}
				for _, arg := range call.Args[min(first, len(call.Args)):] {
					collectIdents(arg, roots)
					direct = append(direct, arg)
				}
			}
			for grew := true; grew; {
				grew = false
				for _, rhs := range assignmentsTo(fn, roots) {
					before := len(roots)
					collectIdents(rhs, roots)
					if len(roots) != before {
						grew = true
					}
				}
			}

			check := func(e ast.Expr) {
				checkNoOpaqueString(t, fset, fn.Name.Name, e,
					params, roots, declared, localHelpers, builtinHelpers)
			}
			for _, e := range direct {
				check(e)
			}
			for _, rhs := range assignmentsTo(fn, roots) {
				check(rhs)
			}
		}
	}
}

// assignmentsTo 找出所有给这些名字赋过值的右侧表达式。
func assignmentsTo(fn *ast.FuncDecl, names map[string]bool) []ast.Expr {
	var out []ast.Expr
	ast.Inspect(fn, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		var touches bool
		for _, lhs := range as.Lhs {
			if id, ok := lhs.(*ast.Ident); ok && names[id.Name] {
				touches = true
			}
		}
		if touches {
			out = append(out, as.Rhs...)
		}
		return true
	})
	return out
}

// packageNames 是本包声明过的名字，用来判断一个标识符是不是本地有据可查的。
type packageNames struct {
	funcs  map[string]bool
	values map[string]bool
}

func packageDeclNames(files []*ast.File) packageNames {
	out := packageNames{funcs: map[string]bool{}, values: map[string]bool{}}
	for _, f := range files {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					out.funcs[d.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						for _, id := range vs.Names {
							out.values[id.Name] = true
						}
					}
				}
			}
		}
	}
	return out
}

// paramNames 收集一个函数的入参与接收者名字。它们的值由调用方负责，本测试不跨函数追。
func paramNames(fn *ast.FuncDecl) map[string]bool {
	out := map[string]bool{}
	add := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, field := range fl.List {
			for _, id := range field.Names {
				out[id.Name] = true
			}
		}
	}
	add(fn.Recv)
	add(fn.Type.Params)
	return out
}

// collectIdents 把表达式里出现的标识符名字收进集合。
func collectIdents(e ast.Expr, into map[string]bool) {
	ast.Inspect(e, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			into[id.Name] = true
		}
		return true
	})
}

// checkNoOpaqueString 在一段表达式里找「算出来的字符串」。
func checkNoOpaqueString(t *testing.T, fset *token.FileSet, where string, e ast.Expr,
	params, roots map[string]bool, declared packageNames,
	localHelpers, builtinHelpers map[string]bool) {
	t.Helper()
	at := func(n ast.Node) string {
		pos := fset.Position(n.Pos())
		return fmt.Sprintf("%s:%d", filepath.Base(pos.Filename), pos.Line)
	}
	var walk func(ast.Expr)
	walk = func(x ast.Expr) {
		if x == nil {
			return
		}
		if _, ok := constStringOf(x); ok {
			return // 算得出常量值，到此为止
		}
		switch v := x.(type) {
		case *ast.Ident:
			switch {
			case params[v.Name], roots[v.Name]:
				// 入参由调用方负责；root 的每一次赋值都在本测试的检查范围内。
			case declared.values[v.Name], declared.funcs[v.Name]:
				// 本包声明的名字，值写在源码里，守边测试扫得到。
			case v.Name == "nil" || v.Name == "true" || v.Name == "false":
			default:
				t.Errorf("%s 里命令参数用到了来路不明的 %s（%s）："+
					"它既不是本函数入参，也不是被追踪的局部变量或本包声明",
					where, v.Name, at(v))
			}
			return
		case *ast.SelectorExpr:
			// 只认「接收者或入参的字段」。别的（包级变量、全局状态）说不清来源。
			if id, ok := v.X.(*ast.Ident); ok && (params[id.Name] || roots[id.Name]) {
				return
			}
			if id, ok := v.X.(*ast.Ident); ok && declared.values[id.Name] {
				return
			}
			t.Errorf("%s 里命令参数读了 %s 的字段（%s）："+
				"只有本函数的接收者、入参或本包声明的值可以这样读",
				where, calleeName(v), at(v))
			return
		case *ast.CompositeLit:
			for _, el := range v.Elts {
				walk(el)
			}
			return
		case *ast.BinaryExpr:
			walk(v.X)
			walk(v.Y)
			return
		case *ast.ParenExpr:
			walk(v.X)
			return
		case *ast.Ellipsis:
			walk(v.Elt)
			return
		case *ast.IndexExpr:
			walk(v.X)
			return
		case *ast.CallExpr:
			name := calleeName(v.Fun)
			ok := builtinHelpers[name] || (localHelpers[name] && declared.funcs[name])
			if !ok {
				t.Errorf("%s 里命令参数用到了 %s(...)（%s）："+
					"跑出去的命令参数只能是字面量、本函数入参，或白名单里那几个"+
					"本包声明的辅助函数。一个算出来的字符串可以是任何东西——"+
					"包括替人敲键的那几个子命令", where, name, at(v))
				return
			}
			for _, a := range v.Args {
				walk(a)
			}
			return
		}
	}
	walk(e)
}
