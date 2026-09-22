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

			// **被追踪 ≠ 有来源。** roots 是迭代出来的闭包，里面混着
			// 「本函数赋过值的局部变量」和「只是出现在某个右侧的名字」。
			// 后者（比如一个包级变量）在本函数里根本没有赋值语句，
			// 它的值从哪来本测试并不知道——放它过去等于自己把洞挖回来。
			assigned := assignedNames(fn)
			ctx := argCtx{
				fset: fset, where: fn.Name.Name, fn: fn, params: params, assigned: assigned,
				declared: declared, localHelpers: localHelpers, builtinHelpers: builtinHelpers,
			}
			for _, e := range direct {
				checkArgExpr(t, ctx, e)
			}
			for _, rhs := range assignmentsTo(fn, roots) {
				checkArgExpr(t, ctx, rhs)
			}
		}
	}
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

// stringBuilders 是造字符串的原语。它们出现在命令参数里就是红的。
var stringBuilders = map[string]bool{"string": true, "fmt.Sprintf": true, "fmt.Sprint": true}

// findPackageFunc / returnedExprs 由 TestCommandArguments 初始化一次，
// 用来追本包包级函数的返回值。
var pkgFuncIndex map[string]*ast.FuncDecl

// pkgMethodIndex 按方法名收本包的方法。同名的全收——
// 调用点写的是变量名，这里没有类型信息分不出是哪一个，那就全追一遍。
var pkgMethodIndex map[string][]*ast.FuncDecl

// tracing 挡住互相调用导致的无限下钻。
var tracing = map[string]bool{}

func findPackageFunc(name string) *ast.FuncDecl { return pkgFuncIndex[name] }

// stringResultIndexes 说这个函数的第几个返回值是 string。
//
// 只追字符串。追一个返回 MenuEvent 的方法没有意义——
// 结构体不会变成命令参数，真正变成参数的是从它里面取出来的那个字段，
// 而取字段那一步是另一次调用，会被单独检查。
func stringResultIndexes(fn *ast.FuncDecl) []int {
	if fn.Type.Results == nil {
		return nil
	}
	var out []int
	pos := 0
	for _, field := range fn.Type.Results.List {
		n := len(field.Names)
		if n == 0 {
			n = 1
		}
		if id, ok := field.Type.(*ast.Ident); ok && id.Name == "string" {
			for k := 0; k < n; k++ {
				out = append(out, pos+k)
			}
		}
		pos += n
	}
	return out
}

// returnedExprs 取出那些会成为字符串返回值的表达式。
func returnedExprs(fn *ast.FuncDecl) []ast.Expr {
	want := stringResultIndexes(fn)
	if len(want) == 0 {
		return nil
	}
	total := 0
	if fn.Type.Results != nil {
		for _, f := range fn.Type.Results.List {
			n := len(f.Names)
			if n == 0 {
				n = 1
			}
			total += n
		}
	}
	var out []ast.Expr
	ast.Inspect(fn, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		if len(ret.Results) != total {
			// return f() 这种一次返回多值的写法，位置对不上，全追一遍。
			out = append(out, ret.Results...)
			return true
		}
		for _, i := range want {
			if i < len(ret.Results) {
				out = append(out, ret.Results[i])
			}
		}
		return true
	})
	return out
}

// argCtx 把一次检查要用到的几张表打包，省得到处传。
type argCtx struct {
	fset                         *token.FileSet
	where                        string
	fn                           *ast.FuncDecl
	params, assigned             map[string]bool
	declared                     packageNames
	localHelpers, builtinHelpers map[string]bool
}

// checkArgExpr 检查一段会成为命令参数的表达式。
//
// **位置决定语义，所以检查也按位置分。** tmux 的 argv 里：
//
//	tmux -L <socket> attach-session -t <target>
//	     ^^flag ^^data ^^subcommand  ^^flag ^^data
//
// 跟在字面量 flag 后面的那个值，tmux 只会当成这个 flag 的取值；
// 它就算等于 send-keys 也不会被执行。**能变成子命令的是那些
// 前面没有 flag、自己独立站着的位置。**
//
// 上一轮这里不分位置，一律要求「字面量或入参」，于是把
// menu.TmuxTargetOf() 这类取会话名的正常调用也拦了——那是误报，
// 会逼人把白名单越加越长，最后谁都能进。
func checkArgExpr(t *testing.T, ctx argCtx, e ast.Expr) {
	t.Helper()
	checkArgExprFrom(t, ctx, e, false)
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
			// append(args, ...) 头一个是被追加的切片，其余是新元素，
			// 位置状态从前者接着往后走。
			carried := checkArgExprFrom(t, ctx, v.Args[0], prevIsFlag)
			return checkArgSequence(t, ctx, v.Args[1:], carried)
		}
	case *ast.Ident:
		// 引用一个切片变量（args）——它自己的赋值会被单独检查，
		// 这里不知道它末尾是不是 flag，保守当成不是。
		return false
	}
	// 单个值（比如直接写在 exec.Command 里的那几个），按独立位置办。
	return checkArgSequence(t, ctx, []ast.Expr{e}, prevIsFlag)
}

// valueFlags 是本包实际用到的、**后面必须跟一个取值**的命令行 flag。
//
// 这张表是显式的，不是「以 - 开头就算」，更不是「是个常量就算」。
//
// 上一轮那版用的是后者，独立评审一行就破了：
//
//	exec.Command("tmux", append([]string{}, "list-sessions", ";", opaque())...)
//	// 实际 argv：tmux list-sessions ; send-keys
//
// `;` 是个常量，于是被当成 flag，后面那个位置就被当成「它的取值」放过了——
// 可 `;` 在 tmux 里是**命令分隔符**，它后面正是下一条子命令的位置。
//
// 新增 flag 要往这里加一行，加的时候得想清楚它是不是真的吃一个参数。
var valueFlags = map[string]bool{
	"-L": true, "-t": true, "-c": true, "-g": true, "-m": true,
	"--model": true, "--listen": true, "--token": true,
}

// checkArgSequence 按 argv 位置检查一串命令参数。
//
// 位置状态跨调用连续：一次 append 的最后一个元素是 flag 时，
// 下一次 append 的第一个元素就在它的取值位上。
func checkArgSequence(t *testing.T, ctx argCtx, elems []ast.Expr, prevIsFlag bool) bool {
	t.Helper()
	for _, el := range elems {
		if s, ok := constStringOf(el); ok {
			// 字面量：它可能就是子命令名，写死在源码里，守边测试扫得到。
			// **只有认得的取值型 flag 才把下一位变成数据位。**
			prevIsFlag = valueFlags[s]
			continue
		}
		if prevIsFlag {
			// 数据位：前面那个 flag 已经把它的含义定死了，
			// tmux 不会把 -t 的取值当成子命令。
			prevIsFlag = false
			continue
		}
		// 独立位置——这里的值可能被当成可执行文件或子命令，要有来源。
		checkNoOpaqueString(t, ctx.fset, ctx.where, el, ctx.fn,
			ctx.params, ctx.assigned, ctx.declared, ctx.localHelpers, ctx.builtinHelpers)
		prevIsFlag = false
	}
	return prevIsFlag
}

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

// checkNoOpaqueString 在一段表达式里找「算出来的字符串」。
func checkNoOpaqueString(t *testing.T, fset *token.FileSet, where string, e ast.Expr,
	fn *ast.FuncDecl, params, assigned map[string]bool, declared packageNames,
	localHelpers, builtinHelpers map[string]bool) {
	t.Helper()
	// 追一个局部变量的来历时，防止它自己引用自己绕圈。
	chasing := map[string]bool{}
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
			case params[v.Name]:
				// 入参由调用方负责——它的来源在调用点查。
			case assigned[v.Name]:
				// **本函数里赋过值的局部变量，得追它到底被赋了什么。**
				// 上一版到这里就放行，于是
				//
				//	x := string([]byte{…})
				//	return x
				//
				// 一路绿灯：返回的是个「已赋值的局部变量」，没人问它赋了什么。
				if fn != nil && !chasing[v.Name] {
					chasing[v.Name] = true
					for _, rhs := range assignmentsTo(fn, map[string]bool{v.Name: true}) {
						walk(rhs)
					}
					delete(chasing, v.Name)
				}
			case declared.visible[v.Name], declared.funcs[v.Name]:
				// 值折叠得出来的包级声明，守边测试扫得到它的内容。
			case declared.opaque[v.Name]:
				t.Errorf("%s 里命令参数用到了包级的 %s（%s）："+
					"它的值折叠不出来，守边测试也看不见它运行时是什么",
					where, v.Name, at(v))
			case v.Name == "nil" || v.Name == "true" || v.Name == "false":
			default:
				t.Errorf("%s 里命令参数用到了来路不明的 %s（%s）："+
					"它既不是本函数入参，也不是被追踪的局部变量或本包声明",
					where, v.Name, at(v))
			}
			return
		case *ast.SelectorExpr:
			// 只认「接收者或入参的字段」。别的（包级变量、全局状态）说不清来源。
			if id, ok := v.X.(*ast.Ident); ok && (params[id.Name] || assigned[id.Name]) {
				return
			}
			if id, ok := v.X.(*ast.Ident); ok && declared.visible[id.Name] {
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
			switch {
			case builtinHelpers[name] || (localHelpers[name] && declared.funcs[name]):
				for _, a := range v.Args {
					walk(a)
				}
			case stringBuilders[name] || strings.HasPrefix(name, "strings.") ||
				strings.HasPrefix(name, "strconv.") || strings.HasPrefix(name, "bytes."):
				// 造字符串的原语。它们的结果运行时才知道是什么。
				t.Errorf("%s 里命令参数用到了 %s(...)（%s）："+
					"这是在**造**一个字符串，造出来的可以是任何东西——"+
					"包括替人敲键的那几个子命令。这个位置只接受字面量、"+
					"本函数入参，或读取现成数据", where, name, at(v))
			case declared.funcs[name]:
				// 本包的包级函数：追进去看它 return 什么。
				// 独立评审的反例就是这一种——把 string([]byte{…}) 包进一个
				// 小函数，外面看起来只是个普通调用。
				//
				// 追进去要**换上下文**：里面的名字是它自己的入参和局部变量，
				// 跟调用者那一套没关系。互相调用用 tracing 挡住。
				if callee := findPackageFunc(name); callee != nil && !tracing[name] {
					tracing[name] = true
					for _, res := range returnedExprs(callee) {
						checkNoOpaqueString(t, fset, where+" → "+name, res,
							callee, paramNames(callee), assignedNames(callee),
							declared, localHelpers, builtinHelpers)
					}
					delete(tracing, name)
				}
			default:
				// **本包的方法跟包级函数一样要追。**
				//
				// 上一版这里写着「方法通常是在读数据，不是在造字符串」，
				// 独立评审四行就破了：
				//
				//	type runner struct{}
				//	func (runner) command() string { return string([]byte{…}) }
				//	exec.Command("tmux", runner{}.command())
				//
				// 三条测试全绿，跑起来就是 tmux send-keys。
				// 「通常是」不是边界，是我自己编的分类——本包的方法源码就在这儿，
				// 没有任何理由不追。
				//
				// 真正追不进去的只有**外部包**：它们的实现不在本包，
				// 这里读不到。兜着它的是位置规则——这类值只出现在
				// 取值型 flag 后面的数据位，tmux 不会把 -t 的取值当子命令。
				tail := lastSegment(name)
				if ms := pkgMethodIndex[tail]; len(ms) > 0 && !tracing[tail] {
					tracing[tail] = true
					for _, m := range ms {
						for _, res := range returnedExprs(m) {
							checkNoOpaqueString(t, fset, where+" → "+tail, res,
								m, paramNames(m), assignedNames(m),
								declared, localHelpers, builtinHelpers)
						}
					}
					delete(tracing, tail)
				}
			}
			return
		}
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
