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
// 管这些构造的是 TestCommandArgumentsAreBuiltFromConstantsOnly——
// 它盯的是「算出来的字符串能不能成为跑出去的命令参数」，那才是出口。
// 它也有边界：拦得住对照脚本列出的写法，拦不住 GAPS 列出的写法。
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
// **这条测试有边界，名字比它做到的多。** 名字说「只由常量构成」，实际是：
// 对照脚本列出的写法它拦得住，GAPS 列出的写法它拦不住。
// 第二个返回值、函数变量与接口调用、切片中转、字段与下标原地写入，都在拦不住那边。
// 边界与剩余风险见 4.eval/results/n037_workbench_attach/boundary-limits.md，
// owner 2026-09-23 裁定按这个有限边界验收。名字保留，是因为改名要连动对照脚本和
// 多轮评审记录里的引用；读名字的人请先读这一段。
func TestCommandArgumentsAreBuiltFromConstantsOnly(t *testing.T) {
	// 起点只有标准库那两个。**本包里替它们转发参数的函数自己长出来。**
	//
	// 上一轮这里是写死的三个名字，独立评审一眼看出后果：
	//
	//	func wrap(cmd string) { exec.Command(cmd) }
	//	wrap(opaqueFn())                          // 绿
	//
	// 「命令参数只能是常量或入参」这句话，实际只在**同一个函数内**成立——
	// 包一层就没了。评审说得对：这不是罕见写法，是普通的 helper 重构。
	//
	// 所以现在迭代：谁把自己的入参喂给了 builder，谁就也是 builder，
	// 它的调用点同样要查。到不动点为止。
	builders := map[string]bool{"exec.Command": true, "exec.CommandContext": true}
	for grew := true; grew; {
		grew = false
		for _, f := range mustPackageASTs(t) {
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil || builders[fn.Name.Name] {
					continue
				}
				if forwardsParamsToBuilder(fn, builders) {
					builders[fn.Name.Name] = true
					grew = true
				}
			}
		}
	}
	// append 是内建，只拼切片不造字符串；exactTarget 必须是本包声明的那一个。
	localHelpers := map[string]bool{"exactTarget": true}
	builtinHelpers := map[string]bool{"append": true}

	files, fset := packageASTs(t)
	declared := packageDeclNames(files)
	pkgFuncIndex = map[string]*ast.FuncDecl{}
	pkgMethodIndex = map[string][]*ast.FuncDecl{}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fn.Recv == nil {
				pkgFuncIndex[fn.Name.Name] = fn
			} else {
				pkgMethodIndex[fn.Name.Name] = append(pkgMethodIndex[fn.Name.Name], fn)
			}
		}
	}
	// 每个 wrapper 先自己算一遍：哪些入参最终站在独立位置上。
	// 这样调用点就知道该严还是该松。
	// 只认入参**直接**站在 builder 参数里的情形：先装进切片再展开（GAPS K3）
	// 算不出来，strict 集合是空的，调用点就放过了。
	wrapperStrictParams = map[string]map[string]bool{}
	for grew := true; grew; {
		grew = false
		for name := range builders {
			fn := findPackageFunc(name)
			if fn == nil {
				continue
			}
			next := strictParamNames(fn, builders, wrapperStrictParams)
			if len(next) != len(wrapperStrictParams[name]) {
				wrapperStrictParams[name] = next
				grew = true
			}
		}
	}
	// 白名单辅助函数不能同时是 builder，否则它会替自己的实参开后门。
	for name := range localHelpers {
		if builders[name] {
			t.Errorf("%s 既在辅助函数白名单里，又会把入参喂给命令构造器——"+
				"这两个身份不能同时有", name)
		}
	}
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
			assigned := assignedNames(fn)
			ctx := argCtx{
				fset: fset, where: fn.Name.Name, fn: fn, params: params, assigned: assigned,
				declared: declared, localHelpers: localHelpers, builtinHelpers: builtinHelpers,
				chasing: map[string]bool{},
			}
			for _, call := range builderCalls {
				name := calleeName(call.Fun)
				if callee := findPackageFunc(name); callee != nil && callee != fn {
					// 本包的 wrapper：位置事实已经在它内部算过了，
					// 按参数名决定这个实参严不严。
					order := paramOrder(callee)
					strictSet := wrapperStrictParams[name]
					for i, arg := range call.Args {
						if i < len(order) && !strictSet[order[i]] {
							continue // 那个入参最终落在数据位
						}
						checkNoOpaqueString(t, fset, fn.Name.Name, arg, fn,
							params, assigned, declared, localHelpers, builtinHelpers, true)
					}
					continue
				}
				// 标准库的命令构造器：直接按 argv 位置走。
				first := 0
				if name == "exec.CommandContext" {
					first = 1 // 第一个参数是 context，不是命令的一部分
				}
				prevIsFlag := false
				for _, arg := range call.Args[min(first, len(call.Args)):] {
					prevIsFlag = checkArgExprFrom(t, ctx, arg, prevIsFlag)
				}
			}
		}
	}
}

// paramOrder 按声明顺序列出入参名。
func paramOrder(fn *ast.FuncDecl) []string {
	var out []string
	if fn.Type.Params == nil {
		return out
	}
	for _, field := range fn.Type.Params.List {
		if len(field.Names) == 0 {
			out = append(out, "")
			continue
		}
		for _, id := range field.Names {
			out = append(out, id.Name)
		}
	}
	return out
}

// wrapperStrictParams 记下每个本包 wrapper 的哪些入参会落到独立位置。
var wrapperStrictParams map[string]map[string]bool

// assignedNames 列出本函数体里真正被赋过值的名字。
func assignedNames(fn *ast.FuncDecl) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(fn, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range v.Lhs {
				if id, ok := lhs.(*ast.Ident); ok {
					out[id.Name] = true
				}
			}
		case *ast.RangeStmt:
			for _, e := range []ast.Expr{v.Key, v.Value} {
				if id, ok := e.(*ast.Ident); ok {
					out[id.Name] = true
				}
			}
		case *ast.DeclStmt:
			// var x = ... 跟 x := ... 是一回事，别漏。
			if gd, ok := v.Decl.(*ast.GenDecl); ok {
				for _, spec := range gd.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						for _, id := range vs.Names {
							out[id.Name] = true
						}
					}
				}
			}
		}
		return true
	})
	return out
}

// assignmentsTo 找出所有给这些名字赋过值的右侧表达式。
func assignmentsTo(fn *ast.FuncDecl, names map[string]bool) []ast.Expr {
	var out []ast.Expr
	ast.Inspect(fn, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.AssignStmt:
			var touches bool
			for _, lhs := range v.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && names[id.Name] {
					touches = true
				}
			}
			if touches {
				out = append(out, v.Rhs...)
			}
		case *ast.DeclStmt:
			gd, ok := v.Decl.(*ast.GenDecl)
			if !ok {
				return true
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, id := range vs.Names {
					if names[id.Name] && i < len(vs.Values) {
						out = append(out, vs.Values[i])
					}
				}
			}
		}
		return true
	})
	return out
}

// packageNames 是本包声明过的名字，用来判断一个标识符是不是本地有据可查的。
//
// 包级值分成两堆是有讲究的：**只有算得出常量字符串的那些才算「守边测试看得见」**。
//
//	var ok   = "attach-session"                      // 折叠得出来，守边扫得到
//	var bad  = string([]byte{115, 101, 110, 100})    // 折叠不出来，守边看不见
//
// 如果这里不分堆，`bad` 就能以「本包声明的名字」这个身份混进命令参数——
// 而那正是独立评审用来绕过上一版的那类构造。
type packageNames struct {
	funcs map[string]bool
	// visible：值能折叠成常量的包级声明，守边测试扫得到它的内容。
	visible map[string]bool
	// opaque：值算不出来的包级声明，谁也不知道它运行时是什么。
	opaque map[string]bool
}

func packageDeclNames(files []*ast.File) packageNames {
	out := packageNames{
		funcs:   map[string]bool{},
		visible: map[string]bool{},
		opaque:  map[string]bool{},
	}
	for _, f := range files {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					out.funcs[d.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, id := range vs.Names {
						seen := false
						if i < len(vs.Values) {
							if _, good := constStringOf(vs.Values[i]); good {
								seen = true
							}
							// 字符串切片这类复合字面量：元素全算得出就算看得见。
							if lit, isLit := vs.Values[i].(*ast.CompositeLit); isLit && !seen {
								seen = true
								for _, el := range lit.Elts {
									if _, good := constStringOf(el); !good {
										seen = false
										break
									}
								}
							}
						}
						if seen {
							out.visible[id.Name] = true
						} else {
							out.opaque[id.Name] = true
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

// checkNoOpaqueString 检查一段会成为命令参数的表达式。
//
// **默认是拒绝，不是放行。** 这一轮改的就是这个默认值。
//
// 前几轮每修一次就多一条「这类通常安全」的分类：方法通常在读数据、
// 返回类型写着 string 才算字符串、外部返回值只会落在数据位。
// 独立评审每次都给出可编译可运行的反例，三条全中。
//
// 分类的毛病在于它是我顺手划的，而反例只需要找到分类的边上那一格。
// 所以现在反过来：**能说出来源的才放行，说不出的拒绝**——
//
// 这个默认值只管**到了这个函数面前**的表达式。
// attempt 20 的独立评审指出，有几条路在到这里之前就被跳过了（GAPS K1–K4）：
// 第二个返回值、经函数变量或接口的调用、切片中转、字段与下标原地写入。
//
//	字面量                 源头写在源码里，守边测试扫得到
//	本函数入参             来源在调用点。调用点按名字认 builder，
//	                       经函数变量、接口、接收者变量的调用认不出（GAPS K2）
//	本包声明且值折叠得出来  同上，值在源码里
//	本包函数 / 方法调用      追进去看它 return 什么，不看它声明的返回类型
//	本函数赋过值的变量      追那次赋值
//	入参或本地变量的字段     追那个变量的来源
//
// 剩下的全部报错：外部包调用、类型断言、通道接收、索引取值……
// 它们不是「危险」，是**这里说不出它的来源**，而说不出来源的东西
// 不该出现在能被解释成子命令的位置上。
//
// strict 为 false 时（取值型 flag 后面的数据位）只放宽一条：
// 允许外部包的返回值。那个位置 tmux 只当成 flag 的取值，不会执行它。
// 所以 valueFlags 写错一行（把 tmux 的布尔选项如 `-u` 列进去），后一位就能放进一个外部值。
// 那一位实际是独立位置，外部值可能就是一条子命令（GAPS K5）。
// 这张表写错是真风险，不是「最多放宽一点」。
func checkNoOpaqueString(t *testing.T, fset *token.FileSet, where string, e ast.Expr,
	fn *ast.FuncDecl, params, assigned map[string]bool, declared packageNames,
	localHelpers, builtinHelpers map[string]bool, strict bool) {
	t.Helper()
	// 追一个局部变量的来历时，防止它自己引用自己绕圈。
	chasing := map[string]bool{}
	at := func(n ast.Node) string {
		pos := fset.Position(n.Pos())
		return fmt.Sprintf("%s:%d", filepath.Base(pos.Filename), pos.Line)
	}
	reject := func(n ast.Node, what string) {
		if !strict {
			return // 数据位：tmux 只把它当成前一个 flag 的取值
		}
		t.Errorf("%s 里命令参数用到了%s（%s）："+
			"这个位置可能被当成可执行文件或子命令，只接受说得出来源的值——"+
			"字面量、本函数入参、本包声明的常量，或能追进去的本包函数与变量",
			where, what, at(n))
	}
	var walk func(ast.Expr)
	chaseIdent := func(name string) bool {
		if fn == nil || chasing[name] {
			return false
		}
		chasing[name] = true
		for _, rhs := range assignmentsTo(fn, map[string]bool{name: true}) {
			walk(rhs)
		}
		delete(chasing, name)
		return true
	}
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
			case params[v.Name]:
				// 入参由调用方负责——它的来源在调用点查。
			case assigned[v.Name]:
				// 本函数里赋过值：追那次赋值。
				chaseIdent(v.Name)
			case declared.visible[v.Name], declared.funcs[v.Name]:
				// 值折叠得出来的包级声明，守边测试扫得到它的内容。
			case v.Name == "nil" || v.Name == "true" || v.Name == "false":
			case declared.opaque[v.Name]:
				t.Errorf("%s 里命令参数用到了包级的 %s（%s）："+
					"它的值折叠不出来，守边测试也看不见它运行时是什么",
					where, v.Name, at(v))
			default:
				reject(v, "来路不明的 "+v.Name)
			}
			return
		case *ast.SelectorExpr:
			// x.Field：**要追 x 从哪来**，不能因为 x 是本地变量就放行。
			// 上一轮正是这里漏的：p := build(); exec.Command("tmux", p.Command)，
			// build 返回结构体所以没被追，p.Command 又因为 p 赋过值直接放行。
			if id, ok := v.X.(*ast.Ident); ok {
				switch {
				case params[id.Name]:
					return // 入参的字段，来源在调用点
				case assigned[id.Name]:
					if chaseIdent(id.Name) {
						return
					}
				case declared.visible[id.Name]:
					return
				}
			}
			reject(v, "字段读取 "+calleeName(v))
			return
		case *ast.CompositeLit:
			for _, el := range v.Elts {
				walk(el)
			}
			return
		case *ast.KeyValueExpr:
			walk(v.Value)
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
		case *ast.UnaryExpr:
			walk(v.X)
			return
		case *ast.CallExpr:
			name := calleeName(v.Fun)
			tail := lastSegment(name)
			switch {
			case builtinHelpers[name] || (localHelpers[name] && declared.funcs[name]):
				for _, a := range v.Args {
					walk(a)
				}
			case stringBuilders[name] || strings.HasPrefix(name, "strings.") ||
				strings.HasPrefix(name, "strconv.") || strings.HasPrefix(name, "bytes."):
				t.Errorf("%s 里命令参数用到了 %s(...)（%s）："+
					"这是在**造**一个字符串，造出来的可以是任何东西——"+
					"包括替人敲键的那几个子命令", where, name, at(v))
			case declared.funcs[name] && !tracing[name]:
				callee := findPackageFunc(name)
				tracing[name] = true
				for _, res := range returnedExprs(callee) {
					checkNoOpaqueString(t, fset, where+" → "+name, res,
						callee, paramNames(callee), assignedNames(callee),
						declared, localHelpers, builtinHelpers, strict)
				}
				delete(tracing, name)
			case len(pkgMethodIndex[tail]) > 0 && !tracing[tail]:
				tracing[tail] = true
				for _, m := range pkgMethodIndex[tail] {
					for _, res := range returnedExprs(m) {
						checkNoOpaqueString(t, fset, where+" → "+tail, res,
							m, paramNames(m), assignedNames(m),
							declared, localHelpers, builtinHelpers, strict)
					}
				}
				delete(tracing, tail)
			case declared.funcs[name] || len(pkgMethodIndex[tail]) > 0:
				// 正在追它，别绕圈
			default:
				// 外部包：源码不在这儿，追不进去。
				// **追不进去不等于安全**——上一轮我把这条写成了「它只会落在
				// 数据位」，评审用 exec.Command("tmux", os.Getenv("X")) 证明是错的。
				reject(v, "外部调用 "+name+"(...) 的返回值")
			}
			return
		}
		reject(x, fmt.Sprintf("一个 %T", x))
	}
	walk(e)
}

// 锚点匹配必须无视注释。
//
// 独立评审的反例：在另一个同名调用里写一段注释，内容就抄原锚点——
//
//	inst.PreviewFull(/* tmuxSession.Attach(ctx, detachByte) */)
//
// 锚点确实落在这个 CallExpr 的源码区间里，但它只是注释，不是那件事。
// 把证据指到这个调用上，冒充就成立了。
func TestAnchorMatchingIgnoresComments(t *testing.T) {
	const src = `package p

func f() {
	a.Foo( /* b.Bar(x, y) */ )
	b.Bar(x, y)
}
`
	fset, path := fixture(t, src)
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	var foo *ast.CallExpr
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && calleeName(call.Fun) == "a.Foo" {
			foo = call
		}
		return true
	})
	if foo == nil {
		t.Fatal("测试自己写坏了：找不到 a.Foo 那个调用")
	}
	const anchor = "b.Bar(x, y)"
	if raw := nodeText([]byte(src), fset, foo); !strings.Contains(raw, anchor) {
		t.Fatalf("测试前提不成立：注释本该让原始文本含有锚点，实际是 %q", raw)
	}
	clean := nodeTextWithoutComments([]byte(src), fset, file, foo)
	if strings.Contains(clean, anchor) {
		t.Errorf("剔除注释后仍含锚点 %q：%q——注释里抄一份锚点就能冒充", anchor, clean)
	}
	// 真正的那个调用当然还得匹配得上。
	var bar *ast.CallExpr
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && calleeName(call.Fun) == "b.Bar" {
			bar = call
		}
		return true
	})
	if got := nodeTextWithoutComments([]byte(src), fset, file, bar); !strings.Contains(got, anchor) {
		t.Errorf("真正那个调用反而匹配不上了：%q", got)
	}
}

// —— 以下是命令参数检查用到的索引与位置分析 ——

// stringBuilders 是造字符串的原语。它们出现在命令参数里就是红的。
var stringBuilders = map[string]bool{"string": true, "fmt.Sprintf": true, "fmt.Sprint": true}

// valueFlags 是本包实际用到的、**后面必须跟一个取值**的命令行 flag。
//
// 这张表是显式的，不是「以 - 开头就算」，更不是「是个常量就算」。
// 上一版用后者，独立评审一行就破了：
//
//	exec.Command("tmux", append([]string{}, "list-sessions", ";", opaque())...)
//	// 实际 argv：tmux list-sessions ; send-keys
//
// `;` 是个常量，于是被当成 flag，后面那个位置就被当成「它的取值」放过了——
// 可 `;` 在 tmux 里是命令分隔符，它后面正是下一条子命令的位置。
//
// **写错这张表有真后果**：数据位允许外部包的返回值。把布尔 flag 误列进来，
// 后一位其实是独立位置，却会放进外部值（GAPS K5）。
// 所以每加一行都要去上游核对这个 flag 真的吃一个取值。
// 本包实际用到的布尔 flag 是 launch.go 的 `--no-wait`（agent-deck 的），
// tmux 那边有 `-u` 这类全局布尔选项，它们都**不能**进这张表。
var valueFlags = map[string]bool{
	"-L": true, "-t": true, "-c": true, "-g": true,
	"--model": true, "--listen": true, "--token": true,
	"--extra-arg":    true, // n055：上游 launch --extra-arg 吃一个取值（cmd/agent-deck/launch_cmd.go fs.Func("extra-arg")）
	"--message-file": true, // 评审指出的遗漏，见 send.go
}

var pkgFuncIndex map[string]*ast.FuncDecl

// pkgMethodIndex 按方法名收本包的方法。同名的全收——
// 调用点写的是变量名，这里没有类型信息分不出是哪一个，那就全追一遍。
var pkgMethodIndex map[string][]*ast.FuncDecl

// tracing 挡住互相调用导致的无限下钻。
var tracing = map[string]bool{}

func findPackageFunc(name string) *ast.FuncDecl { return pkgFuncIndex[name] }

// returnedExprs 取出一个函数每个 return 的第一个表达式。
//
// **不按返回类型挑。** 上一轮按「返回类型的字面名字是不是 string」过滤，
// 独立评审三行就绕过了：
//
//	type S = string
//	func opaque() S { return string([]byte{…}) }
//
// 别名的拼写不是 `string`，于是根本没被追。同一轮还有一条：返回结构体的
// 函数被整个跳过，而 `p := build(); exec.Command("tmux", p.Command)` 也是绿的。
//
// 两条都是按类型的写法分类。现在改成按**位置**：只看第一个返回值。
// 追 error 那一路会一路追进 HTTP 错误分支，全是噪音，所以只追第一个。
//
// **这是一个缺口，不是一条事实。** 第二个返回值同样能流进命令参数：
// `_, x := f(); exec.Command("tmux", x)` 守边测试是绿的（GAPS K1）。
// 只追第一个，是为了压噪音做的取舍，不是因为别的位置流不进去。
func returnedExprs(fn *ast.FuncDecl) []ast.Expr {
	var out []ast.Expr
	ast.Inspect(fn, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) == 0 {
			return true
		}
		out = append(out, ret.Results[0])
		return true
	})
	return out
}

// mustPackageASTs 是 packageASTs 的缓存版，迭代找 wrapper 时会反复用到。
var packageASTCache []*ast.File

func mustPackageASTs(t *testing.T) []*ast.File {
	t.Helper()
	if packageASTCache == nil {
		packageASTCache, _ = packageASTs(t)
	}
	return packageASTCache
}

// forwardsParamsToBuilder 判断这个函数是不是「把自己的入参转手交给命令构造器」。
//
// 判得宽一点：只要有任何一个入参出现在 builder 调用的参数里就算。
// 宁可多查几个函数，也不放过一层 wrapper。
func forwardsParamsToBuilder(fn *ast.FuncDecl, builders map[string]bool) bool {
	params := paramNames(fn)
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !builders[calleeName(call.Fun)] {
			return true
		}
		for _, arg := range call.Args {
			used := map[string]bool{}
			collectIdents(arg, used)
			for name := range used {
				if params[name] {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

// argCtx 把一次检查要用到的几张表打包，省得到处传。
type argCtx struct {
	fset                         *token.FileSet
	where                        string
	fn                           *ast.FuncDecl
	params, assigned             map[string]bool
	declared                     packageNames
	localHelpers, builtinHelpers map[string]bool
	// chasing 挡住 args = append(args, …) 这种自引用导致的无限下钻。
	chasing map[string]bool
}

// checkArgExprFrom 带着「上一个位置是不是取值型 flag」往下走，
// 这样 append 串起来的 argv 位置才是连续的。
func checkArgExprFrom(t *testing.T, ctx argCtx, e ast.Expr, prevIsFlag bool) bool {
	t.Helper()
	switch v := e.(type) {
	case *ast.CompositeLit: // []string{...}
		return checkArgSequence(t, ctx, v.Elts, prevIsFlag)
	case *ast.CallExpr:
		if calleeName(v.Fun) == "append" && len(v.Args) > 0 {
			carried := checkArgExprFrom(t, ctx, v.Args[0], prevIsFlag)
			return checkArgSequence(t, ctx, v.Args[1:], carried)
		}
	case *ast.Ident:
		// 引用一个切片变量（args）：追它的赋值，位置状态接着走。
		// args = append(args, …) 里右边也有 args，所以要挡住自引用。
		if ctx.assigned[v.Name] && !ctx.chasing[v.Name] {
			ctx.chasing[v.Name] = true
			last := prevIsFlag
			for _, rhs := range assignmentsTo(ctx.fn, map[string]bool{v.Name: true}) {
				last = checkArgExprFrom(t, ctx, rhs, prevIsFlag)
			}
			delete(ctx.chasing, v.Name)
			return last
		}
		// 别的名字（入参等）落到下面按位置检查，不能直接放过。
	}
	return checkArgSequence(t, ctx, []ast.Expr{e}, prevIsFlag)
}

// checkArgSequence 按 argv 位置检查一串命令参数。
func checkArgSequence(t *testing.T, ctx argCtx, elems []ast.Expr, prevIsFlag bool) bool {
	t.Helper()
	return walkArgPositions(elems, prevIsFlag, func(el ast.Expr, strict bool) {
		checkNoOpaqueString(t, ctx.fset, ctx.where, el, ctx.fn,
			ctx.params, ctx.assigned, ctx.declared, ctx.localHelpers, ctx.builtinHelpers, strict)
	})
}

// walkArgPositions 按 argv 语义走一串参数，对每个非字面量元素回调，
// 并说明它落在独立位置（strict）还是取值型 flag 后面的数据位。
func walkArgPositions(elems []ast.Expr, prevIsFlag bool, visit func(ast.Expr, bool)) bool {
	for _, el := range elems {
		if s, ok := constStringOf(el); ok {
			prevIsFlag = valueFlags[s]
			continue
		}
		visit(el, !prevIsFlag)
		prevIsFlag = false
	}
	return prevIsFlag
}

// strictParamNames 找出这个函数的哪些入参会落到命令的**独立位置**上。
//
// 位置信息本来卡在函数边界上：`probeSocket(ctx, e.Name(), …)` 在调用点看不出
// 那个 socket 名进去之后是跟在 `-L` 后面的数据位，于是被当成独立位置严查，
// 报出一堆跟命令无关的误报。
//
// 所以先把每个 wrapper 分析一遍：它的哪个入参最终站在独立位置上。
// 调用点按这个结果决定严不严——**这不是「相信这个函数」，
// 是把它内部的位置事实算出来带到调用点**。
func strictParamNames(fn *ast.FuncDecl, builders map[string]bool,
	known map[string]map[string]bool) map[string]bool {
	out := map[string]bool{}
	params := paramNames(fn)
	assigned := assignedNames(fn)
	chasing := map[string]bool{}
	var walkExpr func(ast.Expr, bool) bool
	mark := func(e ast.Expr, strict bool) {
		if !strict {
			return
		}
		ids := map[string]bool{}
		collectIdents(e, ids)
		for name := range ids {
			if params[name] {
				out[name] = true
			}
		}
	}
	walkExpr = func(e ast.Expr, prevIsFlag bool) bool {
		switch v := e.(type) {
		case *ast.CompositeLit:
			return walkArgPositions(v.Elts, prevIsFlag, mark)
		case *ast.CallExpr:
			if calleeName(v.Fun) == "append" && len(v.Args) > 0 {
				carried := walkExpr(v.Args[0], prevIsFlag)
				return walkArgPositions(v.Args[1:], carried, mark)
			}
		case *ast.Ident:
			if assigned[v.Name] && !chasing[v.Name] {
				chasing[v.Name] = true
				last := prevIsFlag
				for _, rhs := range assignmentsTo(fn, map[string]bool{v.Name: true}) {
					last = walkExpr(rhs, prevIsFlag)
				}
				delete(chasing, v.Name)
				return last
			}
			// 入参或别处来的名字：它就站在这个位置上，按位置记一笔。
			// （早先这里直接 return false，于是所有 wrapper 的 strict 集合
			// 全是空的——位置事实一个也没传出去。）
		}
		return walkArgPositions([]ast.Expr{e}, prevIsFlag, mark)
	}
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := calleeName(call.Fun)
		if !builders[name] {
			return true
		}
		// **对本包 wrapper 的调用不按 argv 位置算。** 它的参数不是 argv，
		// 落在哪由那个 wrapper 内部决定——用它已经算出来的结论传播过来。
		// 这就是为什么整件事要迭代到不动点：probeSocket 的 socket 是不是
		// 独立位置，取决于它转手调用的 instanceIDOf 怎么用它。
		if callee := findPackageFunc(name); callee != nil && callee != fn {
			order := paramOrder(callee)
			calleeStrict := known[name]
			for i, arg := range call.Args {
				if i < len(order) && !calleeStrict[order[i]] {
					continue
				}
				ids := map[string]bool{}
				collectIdents(arg, ids)
				for id := range ids {
					if params[id] {
						out[id] = true
					}
				}
			}
			return true
		}
		first := 0
		if name == "exec.CommandContext" {
			first = 1
		}
		prevIsFlag := false
		for _, arg := range call.Args[min(first, len(call.Args)):] {
			prevIsFlag = walkExpr(arg, prevIsFlag)
		}
		return true
	})
	return out
}
