package agentdeck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"fmt"
	"strconv"
	"strings"
)

// 本文件是 attempt 13 的核心：**用语法树核对上游，不再用文本匹配。**
//
// 为什么要换。attempt 9 到 12 的核对全是按行读文本：
// 找 `^func ` 当函数开头、按子串搜禁词、要求断言逐字出现在某一行上。
// 四轮独立评审，四次被绕过，最后两次的反例都不需要什么技巧：
//
//	原始字符串里写一行顶格的 `func Fake() {`  → 函数体在这里被截断，
//	                                            后面的禁词搜不到了（漏报）
//	把禁词写成 "send" + "-keys"                → 源码里根本不存在那个词
//
// 这两条都不是「检查还不够严」，而是**文本匹配本来就不认识 Go 语法**。
// 字符串里的 func 不是函数，拼出来的常量也还是那个常量——
// 这些事只有解析器知道。所以这一轮改成真解析。

// upstreamFile 解析上游源码树里的一个文件。
func upstreamFile(src, path string) (*ast.File, *token.FileSet, error) {
	data, err := os.ReadFile(filepath.Join(src, path))
	if err != nil {
		return nil, nil, err
	}
	fset := token.NewFileSet()
	// ParseComments：注释也要在树里，否则位置会对不上。
	f, err := parser.ParseFile(fset, path, data, parser.ParseComments)
	if err != nil {
		return nil, nil, err
	}
	return f, fset, nil
}

// findFunc 按名字找函数或方法声明。
//
// 跟原来那个「从上往下找第一行 `func ` 开头、名字后面跟括号」的做法比，
// 差别在于它认的是语法树上的 FuncDecl.Name——
// 字符串字面量里写什么都影响不到它。
func findFunc(f *ast.File, name string) *ast.FuncDecl {
	fn, _ := findFuncUnique(f, name)
	return fn
}

// recvTypeName 取方法接收者的类型名，包级函数返回空串。
func recvTypeName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	t := fn.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// findFuncUnique 按名字找函数，**同名多个时拒绝猜**。
//
// name 可以写成 "Foo"（包级或唯一），也可以写成 "Session.Foo"（指定接收者）。
//
// 为什么非要这样：上游 internal/tmux/socket.go 里同时有包级 runBoundedOutput
// 和 (*Session).runBoundedOutput，internal/session/instance.go 里同时有
// (*Instance).getCodexHomeDir 和包级 getCodexHomeDir。
// 原来那个「从上往下找第一个同名的」会固定返回其中一个，选错了也不说。
// 独立评审把它列为机制缺陷——找不准就该说找不准，不该沉默地猜。
func findFuncUnique(f *ast.File, name string) (*ast.FuncDecl, error) {
	wantRecv, wantName := "", name
	if i := strings.LastIndex(name, "."); i >= 0 {
		wantRecv, wantName = name[:i], name[i+1:]
	}
	var hits []*ast.FuncDecl
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != wantName {
			continue
		}
		if wantRecv != "" && recvTypeName(fn) != wantRecv {
			continue
		}
		hits = append(hits, fn)
	}
	switch len(hits) {
	case 0:
		return nil, fmt.Errorf("找不到 %s", name)
	case 1:
		return hits[0], nil
	}
	var where []string
	for _, fn := range hits {
		if r := recvTypeName(fn); r != "" {
			where = append(where, r+"."+fn.Name.Name)
		} else {
			where = append(where, "（包级）"+fn.Name.Name)
		}
	}
	return nil, fmt.Errorf("%s 在这个文件里有 %d 个同名声明（%s），"+
		"请写明接收者，例如 Session.%s", name, len(hits), strings.Join(where, " / "), wantName)
}

// callAtLine 找出**恰好起始于这一行**的那些调用。
//
// 这是 attempt 14 的关键：以前核对 Effect 时，在整个 Within 函数里找一个同名
// 调用就算数，跟 Evidence 指的那一行没有关系。独立评审用一行改动证明了后果：
// 把 session attach 的 Target 从 Attach 换成同函数错误分支里的 Exit，
// Anchor 还指着 Attach 那一行，测试照样通过。
//
// 断言和它的出处必须钉在同一行，否则「有出处」只是碰巧同处一室。
func callAtLine(fn *ast.FuncDecl, fset *token.FileSet, line int) []*ast.CallExpr {
	var out []*ast.CallExpr
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if ok && fset.Position(call.Pos()).Line == line {
			out = append(out, call)
		}
		return true
	})
	return out
}

// funcDeclAtLine 找恰好声明在这一行上的函数。
func funcDeclAtLine(f *ast.File, fset *token.FileSet, line int) *ast.FuncDecl {
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok &&
			fset.Position(fn.Pos()).Line == line {
			return fn
		}
	}
	return nil
}

// valueSpecAtLine 找恰好声明在这一行上的常量或变量，返回名字与算得出的值。
func valueSpecAtLine(f *ast.File, fset *token.FileSet, line int) map[string]string {
	out := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, id := range vs.Names {
			if fset.Position(id.Pos()).Line != line {
				continue
			}
			val := ""
			if i < len(vs.Values) {
				if v, good := constStringOf(vs.Values[i]); good {
					val = v
				}
			}
			out[id.Name] = val
		}
		return true
	})
	return out
}

// qualifiedFuncName 把一个函数声明写成 Recv.Name 或 Name。
func qualifiedFuncName(fn *ast.FuncDecl) string {
	if r := recvTypeName(fn); r != "" {
		return r + "." + fn.Name.Name
	}
	return fn.Name.Name
}

// enclosingFuncOf 说出某一行落在哪个函数里，没有就返回空串。
//
// 按位置区间判断，不是往上找最近的 `func `。
func enclosingFuncOf(f *ast.File, fset *token.FileSet, line int) string {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		start := fset.Position(fn.Pos()).Line
		end := fset.Position(fn.End()).Line
		if line >= start && line <= end {
			return fn.Name.Name
		}
	}
	return ""
}

// callSite 是函数体里的一处调用。
type callSite struct {
	Name string   // 被调用的名字，取最后一段：pkg.Foo 与 x.Foo 都记成 Foo
	Args []string // 能算出常量值的那些参数
}

// callsIn 列出一个函数体里的全部调用。
//
// 这是 NotCovered 的核对基准：函数体级的搜索不追进被调用的函数，
// 所以「有哪些没被追进去」这件事必须能机器算出来，不能靠人写作文。
func callsIn(fn *ast.FuncDecl) []callSite {
	var out []callSite
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := calleeName(call.Fun)
		if name == "" {
			return true
		}
		site := callSite{Name: name}
		for _, arg := range call.Args {
			if v, ok := constStringOf(arg); ok {
				site.Args = append(site.Args, v)
			}
		}
		out = append(out, site)
		return true
	})
	return out
}

// calleeName 取被调用者写在源码里的**完整**形式。
//
// 以前它只取最后一段，于是 safe.Foo() 和 evil.Foo() 压成同一个名字——
// 清单声称调用 safe.Foo，函数里只有 evil.Foo 也能通过。
// 独立评审把这一点列为 EffectCalls 身份模型的真实缺陷，所以改成原样返回。
func calleeName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		if x := calleeName(v.X); x != "" {
			return x + "." + v.Sel.Name
		}
		return v.Sel.Name
	case *ast.IndexExpr: // 泛型实例化：Foo[T](...)
		return calleeName(v.X)
	case *ast.IndexListExpr:
		return calleeName(v.X)
	case *ast.ParenExpr:
		return calleeName(v.X)
	}
	return ""
}

// lastSegment 取调用名的最后一段，供 NotCovered 这类只关心「调了谁」的地方用。
func lastSegment(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}

// constStringOf 把一个表达式算成字符串常量，算不出就说算不出。
//
// **拼接会被折叠**：`"send" + "-keys"` 算出来就是那个完整的子命令名。
// 独立评审用这一招绕过了上一轮按文本扫源码的守边测试——
// 源码里确实没有连着的那个词，跑起来却会拼出来。
func constStringOf(e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			return "", false
		}
		return s, true
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		l, lok := constStringOf(v.X)
		r, rok := constStringOf(v.Y)
		if !lok || !rok {
			return "", false
		}
		return l + r, true
	case *ast.ParenExpr:
		return constStringOf(v.X)
	}
	return "", false
}

// stringsIn 列出一个节点底下所有能算出常量值的字符串，拼接已折叠。
func stringsIn(n ast.Node) []string {
	var out []string
	ast.Inspect(n, func(node ast.Node) bool {
		switch node.(type) {
		case *ast.BasicLit, *ast.BinaryExpr:
			if s, ok := constStringOf(node.(ast.Expr)); ok {
				out = append(out, s)
				return false // 折叠过的表达式不必再拆
			}
		}
		return true
	})
	return out
}

// constStringDecl 找一个字符串常量或变量的声明值。
func constStringDecl(f *ast.File, name string) (string, bool) {
	var found string
	var ok bool
	ast.Inspect(f, func(n ast.Node) bool {
		spec, isSpec := n.(*ast.ValueSpec)
		if !isSpec {
			return true
		}
		for i, ident := range spec.Names {
			if ident.Name != name || i >= len(spec.Values) {
				continue
			}
			if v, good := constStringOf(spec.Values[i]); good {
				found, ok = v, true
				return false
			}
		}
		return true
	})
	return found, ok
}
