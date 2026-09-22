package agentdeck

import (
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
// 这条是 attempt 14 加的，替掉原来那条只会永远通过的
// TestRuntimeBuiltStringsAreNotGuessed。独立评审说得对：
// 那条测试断言「Sprintf 的结果算不出来」，而求值器本来就不处理 CallExpr——
// **它固化的是实现行为，不是安全边界**，而且正式记录了一种已知绕过能力。
//
// 评审给的反例是：
//
//	var hidden = string([]byte{115, 101, 110, 100, 45, 107, 101, 121, 115})
//
// 运行时值是那个注入按键的子命令，按常量折叠扫源码看不见。
// strings.Join 和 fmt.Sprintf 同理。
//
// 堵住「所有算不出的字符串」是做不到的。但**真正的风险有个很窄的出口**：
// 一个字符串要造成伤害，得先成为跑出去的命令的参数。所以这条测试不扫全包，
// 只盯那个出口：凡是函数里构造过 exec 命令，构成那些参数的表达式
// 就只能是字面量、本函数的入参，或白名单里那几个辅助函数。
//
// 现在包里的子命令名全是字面量（"attach-session"、"has-session"、
// "show-environment"），变量只承载数据（socket 名、会话名）。
// 谁要往里塞一个算出来的子命令名，这条会红。
func TestCommandArgumentsAreBuiltFromConstantsOnly(t *testing.T) {
	builders := map[string]bool{
		"exec.Command": true, "exec.CommandContext": true, "interactiveCommand": true,
	}
	// 这几个的输出不可能是子命令名：append 拼切片，exactTarget 只在前面加等号。
	allowedHelpers := map[string]bool{"append": true, "exactTarget": true}

	files, fset := packageASTs(t)
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
			allowed := paramNames(fn)
			roots := map[string]bool{}
			for _, call := range builderCalls {
				// exec.CommandContext 的第一个参数是 context，不是命令的一部分。
				first := 0
				if calleeName(call.Fun) == "exec.CommandContext" {
					first = 1
				}
				for _, arg := range call.Args[min(first, len(call.Args)):] {
					collectIdents(arg, roots)
					// 直接写在调用里的参数也要查。
					checkNoOpaqueString(t, fset, fn.Name.Name, arg, allowed, allowedHelpers, roots)
				}
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				as, ok := n.(*ast.AssignStmt)
				if !ok {
					return true
				}
				var touches bool
				for _, lhs := range as.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && roots[id.Name] {
						touches = true
					}
				}
				if !touches {
					return true
				}
				for _, rhs := range as.Rhs {
					checkNoOpaqueString(t, fset, fn.Name.Name, rhs, allowed, allowedHelpers, roots)
				}
				return true
			})
		}
	}
}

// paramNames 收集一个函数的入参与接收者名字。它们的值由调用方负责，本测试不追。
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
func checkNoOpaqueString(t *testing.T, fset *token.FileSet, where string,
	e ast.Expr, params, helpers, roots map[string]bool) {
	t.Helper()
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
			if params[v.Name] || roots[v.Name] || v.Name == "nil" {
				return
			}
			// 包级常量也算数：它的值在源码里写死，守边测试扫得到。
			return
		case *ast.SelectorExpr:
			return // c.Binary 这类字段读取，由类型和调用方负责
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
		case *ast.CallExpr:
			name := calleeName(v.Fun)
			if !helpers[name] {
				pos := fset.Position(v.Pos())
				t.Errorf("%s 里命令参数用到了 %s(...)（%s:%d）："+
					"跑出去的命令参数只能是字面量、本函数入参，或白名单里的辅助函数。"+
					"一个算出来的字符串可以是任何东西——包括替人敲键的那几个子命令",
					where, name, filepath.Base(pos.Filename), pos.Line)
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
