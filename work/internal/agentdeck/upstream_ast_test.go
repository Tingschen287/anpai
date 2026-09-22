package agentdeck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
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
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
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

// calleeName 取被调用者的名字，只要最后一段。
func calleeName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
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
