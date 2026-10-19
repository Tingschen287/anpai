package i18n

import (
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"unicode"
)

func cjk(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF) {
			return true
		}
	}
	return false
}

var verbRe = regexp.MustCompile(`%[-+# 0]*[0-9]*(\.[0-9]+)?[a-zA-Z]`)

func verbs(s string) []string {
	return verbRe.FindAllString(strings.ReplaceAll(s, "%%", ""), -1)
}

func TestDefaultIsChinese(t *testing.T) {
	SetLang(ZH)
	if Lang() != ZH || T("title") != "设置" || T("main.uses", "x") != "起来时用 x" {
		t.Fatalf("默认中文：%s %q", Lang(), T("title"))
	}
	SetLang(EN)
	t.Cleanup(func() { SetLang(ZH) })
	if Lang() != EN || T("title") == "设置" || cjk(T("title")) || T("brand.name") != "Anpai" {
		t.Fatalf("英文：%s title %q brand %q", Lang(), T("title"), T("brand.name"))
	}
	SetLang("nope")
	if Lang() != ZH || T("title") != "设置" {
		t.Fatal("不认识的语言应回到中文")
	}
}

func TestMissingTranslationFallsBackToChinese(t *testing.T) {
	const key = "test.only.zh"
	zh[key] = "只有中文"
	t.Cleanup(func() {
		delete(zh, key)
		SetLang(ZH)
	})
	SetLang(EN)
	if got := T(key); got != "只有中文" {
		t.Fatalf("缺英文应回退中文，得到 %q", got)
	}
	if got := T("没有这个键"); got != "没有这个键" {
		t.Fatalf("两种都没有应返回键名，得到 %q", got)
	}
}

func TestRecordMissesOnlyWhenAsked(t *testing.T) {
	SetLang(ZH)
	t.Cleanup(func() { SetLang(ZH) })
	if got := T("没有这个键"); got != "没有这个键" {
		t.Fatalf("关掉记录时仍应返回键名，得到 %q", got)
	}
	stop := RecordMisses()
	_ = T("没有这个键")
	_ = Format("也没有")
	_ = Errorf("还是没有")
	miss := stop()
	if len(miss) != 3 || miss[0] != "没有这个键" || miss[1] != "也没有" || miss[2] != "还是没有" {
		t.Fatalf("缺键记录：%v", miss)
	}
	if again := stop(); again != nil {
		t.Fatalf("stop 应只生效一次，得到 %v", again)
	}
	if got := T("关掉以后"); got != "关掉以后" {
		t.Fatalf("关掉以后仍应返回键名，得到 %q", got)
	}
}

// 中英键集合一致，占位符的个数和种类一致，英文里没有中文（含中文标点）。
func TestTablesMatch(t *testing.T) {
	if len(zh) != len(en) {
		t.Fatalf("zh %d 条，en %d 条", len(zh), len(en))
	}
	for k, zv := range zh {
		ev, ok := en[k]
		if !ok {
			t.Errorf("英文没有 %q", k)
			continue
		}
		if vz, ve := verbs(zv), verbs(ev); strings.Join(vz, ",") != strings.Join(ve, ",") {
			t.Errorf("%s 占位符不一致：中文 %v，英文 %v", k, vz, ve)
		}
		if cjk(ev) {
			t.Errorf("英文 %s 含中文：%q", k, ev)
		}
		if zv == "" || ev == "" {
			t.Errorf("%s 有空译文", k)
		}
	}
	for k := range en {
		if _, ok := zh[k]; !ok {
			t.Errorf("中文没有 %q", k)
		}
	}
}

// TestStringToKeyConversionsStayInTable 证明：i18n 生产代码里造不出表外的 Key。
// 显式转换（含括号、以及 *Key 这类含有 Key 的目标）要么是常量而且在表里，要么就失败。
// 非常量的 +、+=、结果为 Key 的切片、类型实参里含有 Key 的泛型实例化，也不允许。
// 表字面量里的无类型常量不是显式转换，不在这里记。
func TestStringToKeyConversionsStayInTable(t *testing.T) {
	srcDir := filepath.Dir(currentFile(t))
	fset := token.NewFileSet()
	var files []*ast.File
	ents, err := os.ReadDir(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		n := e.Name()
		if !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			names = append(names, n)
		}
	}
	if len(names) < 3 {
		t.Fatalf("非测试文件只有 %d 个", len(names))
	}
	for _, n := range names {
		f, err := parser.ParseFile(fset, filepath.Join(srcDir, n), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	info := &types.Info{
		Types:     make(map[ast.Expr]types.TypeAndValue),
		Uses:      make(map[*ast.Ident]types.Object),
		Instances: make(map[*ast.Ident]types.Instance),
	}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	pkg, err := conf.Check("github.com/Tingschen287/terminal-deck/work/internal/i18n", fset, files, info)
	if err != nil {
		t.Fatal(err)
	}
	nConv := 0
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.AssignStmt:
				if n.Tok != token.ADD_ASSIGN {
					return true
				}
				for _, lhs := range n.Lhs {
					if tv, ok := info.Types[lhs]; ok && isOurKey(tv.Type, pkg) {
						t.Errorf("%s：复合赋值 += 的左边是 Key", fset.Position(lhs.Pos()))
					}
				}
			case *ast.BinaryExpr:
				if n.Op != token.ADD {
					return true
				}
				if tv, ok := info.Types[n]; ok && isOurKey(tv.Type, pkg) && tv.Value == nil {
					t.Errorf("%s：二元表达式 + 的结果是 Key", fset.Position(n.Pos()))
				}
			case *ast.SliceExpr:
				if tv, ok := info.Types[n]; ok && isOurKey(tv.Type, pkg) {
					t.Errorf("%s：切片表达式的结果是 Key", fset.Position(n.Pos()))
				}
			case *ast.CallExpr:
				if len(n.Args) != 1 || n.Ellipsis.IsValid() {
					return true
				}
				fun := ast.Unparen(n.Fun)
				tv, ok := info.Types[fun]
				if !ok || !tv.IsType() {
					tv, ok = info.Types[n.Fun]
				}
				if !ok || !tv.IsType() || tv.Type == nil || !mentionsOurKey(tv.Type, pkg) {
					return true
				}
				nConv++
				pos := fset.Position(n.Pos())
				callTV := info.Types[n]
				if callTV.Value == nil {
					t.Errorf("%s：非常量转成含有 Key 的类型，可能造出表外的键", pos)
					return true
				}
				if !isOurKey(tv.Type, pkg) {
					t.Errorf("%s：常量转成了含有 Key、但不是 Key 的类型", pos)
					return true
				}
				if callTV.Value.Kind() != constant.String {
					t.Errorf("%s：Key 常量不是字符串", pos)
					return true
				}
				k := Key(constant.StringVal(callTV.Value))
				if _, ok := zh[k]; !ok {
					t.Errorf("%s：转换出的键 %q 不在表里", pos, k)
				}
			}
			return true
		})
	}
	for id, inst := range info.Instances {
		if inst.TypeArgs == nil {
			continue
		}
		for i := 0; i < inst.TypeArgs.Len(); i++ {
			if mentionsOurKey(inst.TypeArgs.At(i), pkg) {
				t.Errorf("%s：泛型实例化的类型实参含有 Key", fset.Position(id.Pos()))
				break
			}
		}
	}
	t.Logf("i18n 生产代码里显式转成含有 Key 的类型的地方：%d", nConv)
}

func mentionsOurKey(t types.Type, pkg *types.Package) bool {
	return mentionOurKey(t, pkg, map[types.Type]bool{})
}

func mentionOurKey(t types.Type, pkg *types.Package, seen map[types.Type]bool) bool {
	if t == nil {
		return false
	}
	t = types.Unalias(t)
	if seen[t] {
		return false
	}
	seen[t] = true
	if isOurKey(t, pkg) {
		return true
	}
	switch u := t.Underlying().(type) {
	case *types.Pointer:
		return mentionOurKey(u.Elem(), pkg, seen)
	case *types.Slice:
		return mentionOurKey(u.Elem(), pkg, seen)
	case *types.Array:
		return mentionOurKey(u.Elem(), pkg, seen)
	case *types.Map:
		return mentionOurKey(u.Key(), pkg, seen) || mentionOurKey(u.Elem(), pkg, seen)
	case *types.Chan:
		return mentionOurKey(u.Elem(), pkg, seen)
	case *types.Signature:
		if u.Recv() != nil && mentionOurKey(u.Recv().Type(), pkg, seen) {
			return true
		}
		return mentionOurTuple(u.Params(), pkg, seen) || mentionOurTuple(u.Results(), pkg, seen)
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			if mentionOurKey(u.Field(i).Type(), pkg, seen) {
				return true
			}
		}
	case *types.Union:
		for i := 0; i < u.Len(); i++ {
			if mentionOurKey(u.Term(i).Type(), pkg, seen) {
				return true
			}
		}
	}
	return false
}

func mentionOurTuple(t *types.Tuple, pkg *types.Package, seen map[types.Type]bool) bool {
	if t == nil {
		return false
	}
	for i := 0; i < t.Len(); i++ {
		if mentionOurKey(t.At(i).Type(), pkg, seen) {
			return true
		}
	}
	return false
}

func isOurKey(t types.Type, pkg *types.Package) bool {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Name() == "Key" && obj.Pkg() == pkg
}

func currentFile(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(1)
	if !ok {
		t.Fatal("caller")
	}
	return file
}
