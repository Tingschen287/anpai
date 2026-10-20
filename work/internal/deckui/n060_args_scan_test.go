package deckui

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// n060 段 2 返工 2：本地中文不能进参数。
//
// 返工 1 之后，「本地写的中文句子被先压成字符串、再当参数交给 i18n.E」这类问题出现了第 2 次，
// 所以这里不再靠人眼找，而是用 go/ast + go/types 扫一遍，命中就失败。
//
// 「中文来源」（tainted）指下面几种，按数据流追：
//   - 类型检查得出的常量值是含中文的字符串（字面量、具名常量、常量拼接）；
//   - 局部变量、包级变量：任何一处赋给它的值是中文来源；
//   - 对本模块函数的调用：任何一条 return 返回的值是中文来源（形参按调用处的实参算）；
//   - fmt.Sprintf、fmt.Errorf、errors.New、strings.*、append 等标准库调用：任何实参是中文来源；
//   - deckstore.Clean / Clip / ArgText / Arg：按第一个实参；
//   - x.Error()：x 是中文来源；
//   - 切片、数组字面量：任何元素是中文来源。
//
// 辅助函数里经局部变量中转（out := s; return out）也能追到：形参对到调用处的实参时带着调用处自己的环境，
// 局部变量的赋值也在被调函数的环境里求值，所以多层辅助函数、局部变量中转都不会丢。
//
// i18n.E 的返回值不算中文来源：它走码表，中文只在 Error() 里现取，不在源码里。
//
// 「落点」（sink）指：
//   - i18n.E / i18n.Errorf / i18n.ListOf 的参数（第一个键之后的所有参数）；
//   - 直接造 i18n.Error：组合字面量 i18n.Error{Key: …, Args: …}（含 &），以及对 Key / Args 字段的赋值
//     （e.Args = …、e.Args[i] = …、e.Key = …）；
//   - 名叫 note、reason、why、detail、hint 的结构体字段（组合字面量或赋值）；
//   - 键是这几个词的 map 字面量或下标赋值；
//   - 本模块函数里名叫这几个词的形参，调用处传进去的实参。
//
// 中文来源流进落点就是命中。命中必须登记在 argChineseAllow，登记键是「文件 · 函数」，写理由。
// 登记了但已经没有命中，测试也失败，免得例外过期。

// errSuffix 是 .Error() 来源命中的登记键后缀。
const errSuffix = " · .Error()"

var argSinkNames = map[string]bool{"note": true, "reason": true, "why": true, "detail": true, "hint": true}

// argChineseAllow：第 2、3 类（给模型读的、日志与内部记账）里中文来源流进落点的地方。
// 第 1 类（给 owner 看的）不允许登记，要改成码。
var argChineseAllow = map[string]string{
	"lease.go · Store.OwnerTakeover":    "租约事件 Event.Reason：只写进写入权记账文件 leases.json，当审计记录，不经 deckd 响应，不进界面",
	"lease.go · Store.ExternalTakeover": "租约事件 Event.Reason：同上，只进记账文件",
	"lease.go · Store.OwnerReturn":      "租约事件 Event.Reason：同上，只进记账文件",
	"lease.go · Store.AutoHandback":     "租约事件 Event.Reason：同上，只进记账文件；owner 看的交还摘要是 Handback 结构，不是这条备注",
	"lease.go · Store.ConfirmHandback":  "租约事件 Event.Reason：同上，只进记账文件",
	"lease.go · Store.Renew":            "租约事件 Event.Reason：同上，只进记账文件",
	"manifest.go · (pkg)":               "agentdeck 证明清单的 FuncRef.Note：给评审看的说明，不进界面（与 ownerChineseAllow 同一条理由）",
}

type argExpr struct {
	e    ast.Expr
	info *types.Info
	idx  int    // 多返回值赋值时是第几个，单值为 -1
	env  argEnv // 求值 e 时的环境：形参对到调用处实参时，实参要在调用处自己的环境里求值
}

type argFunc struct {
	decl *ast.FuncDecl
	info *types.Info
	path string
}

type argWorld struct {
	assigns map[types.Object][]argExpr // 变量、字段 → 赋给它的值
	funcs   map[types.Object]*argFunc  // 函数、方法 → 声明
}

func newArgWorld(pkgs []checkedPkg) *argWorld {
	w := &argWorld{assigns: map[types.Object][]argExpr{}, funcs: map[types.Object]*argFunc{}}
	for _, cp := range pkgs {
		info := cp.info
		for _, pf := range cp.files {
			for _, d := range pf.file.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok {
					if obj := info.Defs[fd.Name]; obj != nil {
						w.funcs[obj] = &argFunc{decl: fd, info: info, path: pf.path}
					}
				}
			}
			ast.Inspect(pf.file, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.ValueSpec:
					lhs := make([]ast.Expr, len(v.Names))
					for i, id := range v.Names {
						lhs[i] = id
					}
					w.bind(info, lhs, v.Values)
				case *ast.AssignStmt:
					if v.Tok == token.ASSIGN || v.Tok == token.DEFINE || v.Tok == token.ADD_ASSIGN {
						w.bind(info, v.Lhs, v.Rhs)
					}
				case *ast.CompositeLit:
					// 结构体字段：T{Note: x}
					for _, el := range v.Elts {
						kv, ok := el.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						if id, ok := kv.Key.(*ast.Ident); ok {
							if obj := info.Uses[id]; obj != nil {
								w.assigns[obj] = append(w.assigns[obj], argExpr{e: kv.Value, info: info, idx: -1})
							}
						}
					}
				}
				return true
			})
		}
	}
	return w
}

func (w *argWorld) bind(info *types.Info, lhs, rhs []ast.Expr) {
	objOf := func(e ast.Expr) types.Object {
		switch x := ast.Unparen(e).(type) {
		case *ast.Ident:
			if o := info.Defs[x]; o != nil {
				return o
			}
			return info.Uses[x]
		case *ast.SelectorExpr:
			return info.Uses[x.Sel]
		}
		return nil
	}
	switch {
	case len(lhs) == len(rhs):
		for i := range lhs {
			if o := objOf(lhs[i]); o != nil {
				w.assigns[o] = append(w.assigns[o], argExpr{e: rhs[i], info: info, idx: -1})
			}
		}
	case len(rhs) == 1:
		for i := range lhs {
			if o := objOf(lhs[i]); o != nil {
				w.assigns[o] = append(w.assigns[o], argExpr{e: rhs[0], info: info, idx: i})
			}
		}
	}
}

// taint 是一次判断的上下文。env 把被调函数的形参对到调用处的实参。
type taint struct {
	w     *argWorld
	seen  map[string]bool
	depth int
	// errText 为真时换一种来源：不看中文常量，只把 x.Error() 的结果当来源。
	// 抓的是「本地带码错误先被 .Error() 压成中文，再当参数」（返工 2 评审点名的 agents.go:477 那一类）。
	errText bool
}

type argEnv map[types.Object]argExpr

func (t *taint) tainted(e ast.Expr, info *types.Info, idx int, env argEnv) bool {
	if e == nil || t.depth > 12 {
		return false
	}
	t.depth++
	defer func() { t.depth-- }()
	e = ast.Unparen(e)
	if tv, ok := info.Types[e]; ok && tv.Value != nil && tv.Value.Kind() == constant.String {
		return !t.errText && cjk(constant.StringVal(tv.Value))
	}
	switch x := e.(type) {
	case *ast.Ident:
		obj := info.Uses[x]
		if obj == nil {
			obj = info.Defs[x]
		}
		if obj == nil {
			return false
		}
		if a, ok := env[obj]; ok {
			return t.tainted(a.e, a.info, a.idx, a.env)
		}
		return t.varTainted(obj, env)
	case *ast.BinaryExpr:
		return t.tainted(x.X, info, -1, env) || t.tainted(x.Y, info, -1, env)
	case *ast.UnaryExpr:
		return t.tainted(x.X, info, -1, env)
	case *ast.StarExpr:
		return t.tainted(x.X, info, -1, env)
	case *ast.TypeAssertExpr:
		return t.tainted(x.X, info, -1, env)
	case *ast.CompositeLit:
		switch info.TypeOf(x).Underlying().(type) {
		case *types.Slice, *types.Array:
			for _, el := range x.Elts {
				if t.tainted(el, info, -1, env) {
					return true
				}
			}
		}
		return false
	case *ast.CallExpr:
		return t.callTainted(x, info, idx, env)
	}
	return false
}

// varTainted：变量的任何一处赋值是中文来源就算。赋值在被调函数里时要用被调函数的环境求值，
// 否则 out := s; return out 这样的局部中转会把形参丢掉（Codex 复审 3 必须改 3）。
func (t *taint) varTainted(obj types.Object, env argEnv) bool {
	key := fmt.Sprintf("v%p", obj)
	if t.seen[key] {
		return false
	}
	t.seen[key] = true
	defer delete(t.seen, key)
	for _, a := range t.w.assigns[obj] {
		if t.tainted(a.e, a.info, a.idx, env) {
			return true
		}
	}
	return false
}

func calleeObj(info *types.Info, call *ast.CallExpr) types.Object {
	switch f := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		return info.Uses[f]
	case *ast.SelectorExpr:
		return info.Uses[f.Sel]
	}
	return nil
}

func objPkgPath(o types.Object) string {
	if o == nil || o.Pkg() == nil {
		return ""
	}
	return o.Pkg().Path()
}

func (t *taint) callTainted(call *ast.CallExpr, info *types.Info, idx int, env argEnv) bool {
	// 类型转换：string(x)、any(x)
	if tv, ok := info.Types[call.Fun]; ok && tv.IsType() {
		return len(call.Args) == 1 && t.tainted(call.Args[0], info, -1, env)
	}
	obj := calleeObj(info, call)
	pkgPath := objPkgPath(obj)
	name := ""
	if obj != nil {
		name = obj.Name()
	}
	// i18n.E 返回码，不是中文来源
	if pkgPath == i18nImport && (name == "E" || name == "Errorf" || name == "ListOf") {
		return false
	}
	if sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr); ok && name == "Error" && len(call.Args) == 0 {
		if t.errText {
			return true
		}
		return t.tainted(sel.X, info, -1, env)
	}
	if pkgPath == deckstoreImport && (name == "Clean" || name == "Clip" || name == "ArgText" || name == "Arg") {
		return len(call.Args) > 0 && t.tainted(call.Args[0], info, -1, env)
	}
	if fn := t.w.funcs[obj]; fn != nil && isModulePath(pkgPath) {
		return t.returnsTainted(fn, call, info, idx, env)
	}
	if isModulePath(pkgPath) {
		return false // 接口方法、函数值：看不到函数体
	}
	// 标准库和内建：任何实参是中文来源，结果就是
	for _, a := range call.Args {
		if t.tainted(a, info, -1, env) {
			return true
		}
	}
	return false
}

func (t *taint) returnsTainted(fn *argFunc, call *ast.CallExpr, info *types.Info, idx int, env argEnv) bool {
	if fn.decl.Body == nil {
		return false
	}
	key := fmt.Sprintf("f%p", fn.decl)
	if t.seen[key] {
		return false
	}
	t.seen[key] = true
	defer delete(t.seen, key)

	callee := argEnv{}
	if fn.decl.Type.Params != nil {
		i := 0
		for _, field := range fn.decl.Type.Params.List {
			for _, name := range field.Names {
				if i < len(call.Args) {
					callee[fn.info.Defs[name]] = argExpr{e: call.Args[i], info: info, idx: -1, env: env}
				}
				i++
			}
			if len(field.Names) == 0 {
				i++
			}
		}
	}
	found := false
	ast.Inspect(fn.decl.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		switch {
		case len(ret.Results) == 0:
			// 裸 return：看具名结果变量
			if res := fn.decl.Type.Results; res != nil {
				i := 0
				for _, field := range res.List {
					for _, name := range field.Names {
						if (idx < 0 || idx == i) && t.varTainted(fn.info.Defs[name], callee) {
							found = true
						}
						i++
					}
				}
			}
		case len(ret.Results) == 1 && idx > 0:
			// return g() 转交多个返回值
			found = t.tainted(ret.Results[0], fn.info, idx, callee)
		default:
			i := idx
			if i < 0 {
				i = 0
			}
			if i < len(ret.Results) {
				found = t.tainted(ret.Results[i], fn.info, -1, callee)
			}
		}
		return true
	})
	return found
}

// isI18nError 判断类型是不是 i18n.Error（含指针）。
func isI18nError(t types.Type) bool {
	if t == nil {
		return false
	}
	if p, ok := t.Underlying().(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := t.(*types.Named)
	return ok && n.Obj().Name() == "Error" && objPkgPath(n.Obj()) == i18nImport
}

// checkArgsValue 检查 Args 字段的值：切片字面量逐个元素查，其余（变量、append(…) 等）整体查，append 的实参逐个查。
func checkArgsValue(check func(ast.Expr, string), v ast.Expr, what string) {
	switch x := ast.Unparen(v).(type) {
	case *ast.CompositeLit:
		for _, el := range x.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				check(kv.Value, what)
				continue
			}
			check(el, what)
		}
		return
	case *ast.CallExpr:
		if id, ok := ast.Unparen(x.Fun).(*ast.Ident); ok && id.Name == "append" {
			for i, a := range x.Args {
				if i == 0 {
					checkArgsValue(check, a, what)
					continue
				}
				check(a, what)
			}
			return
		}
	}
	check(v, what)
}

type argHit struct {
	site string // 文件 · 函数
	pos  string
	what string
}

func argScanTargets() map[string]bool {
	return map[string]bool{"deckd": true, "deckstore": true, "agentdeck": true, "lease": true, "viewstate": true}
}

func scanArgChinese(pkgs []checkedPkg, fset *token.FileSet) []argHit {
	return append(scanArgs(pkgs, fset, false), scanArgs(pkgs, fset, true)...)
}

// scanArgs 扫一遍。errText 为真时来源换成 x.Error()，命中的登记键后面加「 · .Error()」。
func scanArgs(pkgs []checkedPkg, fset *token.FileSet, errText bool) []argHit {
	w := newArgWorld(pkgs)
	var hits []argHit
	targets := argScanTargets()
	for _, cp := range pkgs {
		if !targets[filepath.Base(cp.dir)] {
			continue
		}
		info := cp.info
		for _, pf := range cp.files {
			base := filepath.Base(pf.path)
			walkStack(pf.file, func(n ast.Node, stack []ast.Node) {
				add := func(pos token.Pos, what string) {
					site := base + " · " + ownerFuncLabel(stack)
					if errText {
						site += errSuffix
					}
					hits = append(hits, argHit{site: site, pos: fset.Position(pos).String(), what: what})
				}
				check := func(e ast.Expr, what string) {
					t := &taint{w: w, seen: map[string]bool{}, errText: errText}
					if t.tainted(e, info, -1, nil) {
						add(e.Pos(), what)
					}
				}
				switch v := n.(type) {
				case *ast.CompositeLit:
					// 直接造 i18n.Error{Key: …, Args: []any{…}}：参数和键都不能是本地中文
					if !isI18nError(info.TypeOf(v)) {
						return
					}
					for _, el := range v.Elts {
						kv, ok := el.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						id, ok := kv.Key.(*ast.Ident)
						if !ok {
							continue
						}
						switch id.Name {
						case "Args":
							checkArgsValue(check, kv.Value, "i18n.Error 的 Args 字段")
						case "Key":
							check(kv.Value, "i18n.Error 的 Key 字段")
						}
					}
					return
				case *ast.CallExpr:
					obj := calleeObj(info, v)
					if objPkgPath(obj) == i18nImport && (obj.Name() == "E" || obj.Name() == "Errorf" || obj.Name() == "ListOf") {
						for i, a := range v.Args {
							if i == 0 {
								continue
							}
							check(a, "i18n."+obj.Name()+" 的参数")
						}
						return
					}
					// 本模块函数里名叫 note / reason 的形参。.Error() 来源不查这里：
					// 会话状态、备注的中文兜底本来就是 i18n.E(...).Error()，英文走同一条通知上的码。
					if errText {
						return
					}
					if fn := w.funcs[obj]; fn != nil && fn.decl.Type.Params != nil {
						i := 0
						for _, field := range fn.decl.Type.Params.List {
							for _, name := range field.Names {
								if argSinkNames[strings.ToLower(name.Name)] && i < len(v.Args) {
									check(v.Args[i], "形参 "+name.Name+"（"+obj.Name()+"）")
								}
								i++
							}
							if len(field.Names) == 0 {
								i++
							}
						}
					}
				case *ast.KeyValueExpr:
					if errText {
						return
					}
					switch k := v.Key.(type) {
					case *ast.Ident:
						if argSinkNames[strings.ToLower(k.Name)] {
							check(v.Value, "字段 "+k.Name)
						}
					case *ast.BasicLit:
						if s, err := strconv.Unquote(k.Value); err == nil && argSinkNames[s] {
							check(v.Value, "map 键 "+s)
						}
					}
				case *ast.AssignStmt:
					// 对 i18n.Error 的 Key / Args 字段赋值：e.Args = …、e.Args[i] = …、e.Key = …（两种来源都查）
					if (v.Tok == token.ASSIGN || v.Tok == token.ADD_ASSIGN) && len(v.Lhs) == len(v.Rhs) {
						for i, l := range v.Lhs {
							lhs := ast.Unparen(l)
							if ix, ok := lhs.(*ast.IndexExpr); ok {
								lhs = ast.Unparen(ix.X)
								if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "Args" && isI18nError(info.TypeOf(sel.X)) {
									check(v.Rhs[i], "i18n.Error 的 Args 元素赋值")
								}
								continue
							}
							if sel, ok := lhs.(*ast.SelectorExpr); ok && isI18nError(info.TypeOf(sel.X)) {
								switch sel.Sel.Name {
								case "Args":
									checkArgsValue(check, v.Rhs[i], "i18n.Error 的 Args 字段赋值")
								case "Key":
									check(v.Rhs[i], "i18n.Error 的 Key 字段赋值")
								}
							}
						}
					}
					if errText {
						return
					}
					if v.Tok != token.ASSIGN && v.Tok != token.ADD_ASSIGN || len(v.Lhs) != len(v.Rhs) {
						return
					}
					for i, l := range v.Lhs {
						switch lv := ast.Unparen(l).(type) {
						case *ast.SelectorExpr:
							if argSinkNames[strings.ToLower(lv.Sel.Name)] {
								check(v.Rhs[i], "字段 "+lv.Sel.Name)
							}
						case *ast.IndexExpr:
							if lit, ok := lv.Index.(*ast.BasicLit); ok {
								if s, err := strconv.Unquote(lit.Value); err == nil && argSinkNames[s] {
									check(v.Rhs[i], "map 键 "+s)
								}
							}
						}
					}
				}
			})
		}
	}
	return hits
}

func TestN060NoLocalChineseInArgs(t *testing.T) {
	var hits []argHit
	err := withTypeImp(func(fset *token.FileSet, imp types.Importer) error {
		pkgs, err := checkModule(fset, imp, nil)
		if err != nil {
			return err
		}
		hits = scanArgChinese(pkgs, fset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var lines []string
	for _, h := range hits {
		t.Logf("命中 %s：%s（%s）", h.pos, h.what, h.site)
		seen[h.site] = true
		if argChineseAllow[h.site] == "" {
			lines = append(lines, fmt.Sprintf("%s：中文流进 %s（%s）", h.pos, h.what, h.site))
		}
	}
	sort.Strings(lines)
	for _, l := range lines {
		t.Error(l)
	}
	for site, why := range argChineseAllow {
		if why == "" {
			t.Errorf("%s 的例外没有写理由", site)
		}
		if !seen[site] {
			t.Errorf("%s 已经没有命中，例外过期了", site)
		}
	}
}

// TestN060ArgScanFindsPlantedChinese 证明扫描器本身有牙：往源码里种几种走法，必须都被抓到。
func TestN060ArgScanFindsPlantedChinese(t *testing.T) {
	const decl = "\n\nconst plantedCN = \"本地写的中文\"\n\nfunc plantedHelper(s string) string { return \"前缀：\" + s }\n"
	cases := []struct {
		name string
		body string
	}{
		{"Literal", "func planted() { _ = i18n.E(\"err.deckd.serverrunspawn.47\", \"本地中文\") }"},
		{"Const", "func planted() { _ = i18n.E(\"err.deckd.serverrunspawn.47\", plantedCN) }"},
		{"Var", "func planted() { why := \"本地\" + \"中文\"; _ = i18n.E(\"err.deckd.serverrunspawn.47\", why) }"},
		{"Sprintf", "func planted() { _ = i18n.E(\"err.deckd.serverrunspawn.47\", fmt.Sprintf(\"本地%d\", 1)) }"},
		{"Errorf", "func planted() { err := errors.New(\"本地\"); _ = i18n.E(\"err.deckd.serverrunspawn.47\", err.Error()) }"},
		{"HelperReturn", "func planted() { _ = i18n.E(\"err.deckd.serverrunspawn.47\", plantedHelper(\"x\")) }"},
		{"CleanPassThrough", "func planted() { _ = i18n.E(\"err.deckd.serverrunspawn.47\", deckstore.Clean(plantedCN, 80)) }"},
		{"ErrorText", "func planted() { err := errors.New(\"x\"); _ = i18n.E(\"err.deckd.serverrunspawn.47\", err.Error()) }"},
		{"ErrorTextClean", "func planted() { err := errors.New(\"x\"); _ = i18n.E(\"err.deckd.serverrunspawn.47\", deckstore.Clean(err.Error(), 80)) }"},
		{"NoteField", "type plantedBox struct{ Note string }\n\nfunc planted() { _ = plantedBox{Note: plantedCN} }"},
		{"NoteMap", "func planted() { _ = map[string]any{\"note\": plantedCN} }"},
		{"NoteParam", "func plantedTake(note string) {}\n\nfunc planted() { plantedTake(plantedCN) }"},
		// 返工 3 新增的两种形状
		{"ErrorLiteralArgs", "func planted() { _ = &i18n.Error{Key: \"err.deckd.serverrunspawn.47\", Args: []any{plantedCN}} }"},
		{"ErrorLiteralKey", "func planted() { _ = i18n.Error{Key: i18n.Key(plantedCN)} }"},
		{"ErrorArgsAssign", "func planted() { e := i18n.E(\"err.deckd.serverrunspawn.47\", \"x\"); e.Args = []any{plantedCN} }"},
		{"ErrorArgsAppend", "func planted() { e := i18n.E(\"err.deckd.serverrunspawn.47\"); e.Args = append(e.Args, plantedCN) }"},
		{"ErrorArgsIndex", "func planted() { e := i18n.E(\"err.deckd.serverrunspawn.47\", \"x\"); e.Args[0] = plantedCN }"},
		{"ErrorKeyAssign", "func planted() { e := i18n.E(\"err.deckd.serverrunspawn.47\"); e.Key = i18n.Key(plantedCN) }"},
		{"LocalViaHelper", "func plantedEcho(s string) string { out := s; return out }\n\nfunc planted() { _ = i18n.E(\"err.deckd.serverrunspawn.47\", plantedEcho(\"本地中文\")) }"},
		{"LocalViaHelperChain", "func plantedEcho(s string) string { out := s; return out }\n\nfunc plantedEcho2(s string) string { mid := plantedEcho(s); return mid }\n\nfunc planted() { _ = i18n.E(\"err.deckd.serverrunspawn.47\", plantedEcho2(plantedCN)) }"},
		{"LocalViaHelperMulti", "func plantedPair(s string) (string, int) { out := s; return out, 1 }\n\nfunc planted() { v, _ := plantedPair(plantedCN); _ = i18n.E(\"err.deckd.serverrunspawn.47\", v) }"},
	}
	// 段 3（第 2 段复审 4 的建议 2）：以前每个反例都把 deckd 及依赖它的包完整装载、类型检查一遍（21 次）。
	// 现在所有反例放进同一份 agents.go 的覆盖里，只装载检查一次：每个反例的函数名与自带的辅助名各加两位序号后缀（planted_03、
	// plantedEcho_03），共用的 plantedCN、plantedHelper 在 decl 里只声明一次，不加后缀；每个反例仍要各自被抓到（站点 agents.go · planted_NN），一个都不能少。
	path := filepath.Join(moduleRoot(), "internal", "deckd", "agents.go")
	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rename := regexp.MustCompile(`\bplanted(Box|Take|Echo2|Echo|Pair)?\b`)
	var planted strings.Builder
	for i, tc := range cases {
		suffix := fmt.Sprintf("_%02d", i)
		planted.WriteString("\n" + rename.ReplaceAllStringFunc(tc.body, func(m string) string { return m + suffix }) + "\n")
	}
	src := map[string]string{path: string(orig) + decl + planted.String()}
	var hits []argHit
	err = withTypeImp(func(fset *token.FileSet, imp types.Importer) error {
		pkgs, err := checkModule(fset, imp, src)
		if err != nil {
			return err
		}
		hits = scanArgChinese(pkgs, fset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, tc := range cases {
		prefix := fmt.Sprintf("agents.go · planted_%02d", i)
		t.Run(tc.name, func(t *testing.T) {
			for _, h := range hits {
				if strings.HasPrefix(h.site, prefix) {
					return
				}
			}
			t.Fatalf("种进去的 %s 没被扫描抓到", tc.name)
		})
	}
}
