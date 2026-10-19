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

// TestStringToKeyConversionsStayInTable 证明：i18n 生产代码里，
// 显式把值转成 Key 时，要么是常量而且在表里，要么就失败。
// 非常量转换能造出任意键，这里不允许。
// 表字面量里的无类型常量不是这种转换，不在这里记。
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
		Types: make(map[ast.Expr]types.TypeAndValue),
		Uses:  make(map[*ast.Ident]types.Object),
	}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	pkg, err := conf.Check("github.com/Tingschen287/terminal-deck/work/internal/i18n", fset, files, info)
	if err != nil {
		t.Fatal(err)
	}
	nConv := 0
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 || call.Ellipsis.IsValid() {
				return true
			}
			var id *ast.Ident
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				id = fun
			case *ast.SelectorExpr:
				id = fun.Sel
			default:
				return true
			}
			tn, ok := info.Uses[id].(*types.TypeName)
			if !ok || !isOurKey(tn.Type(), pkg) {
				return true
			}
			nConv++
			tv := info.Types[call]
			pos := fset.Position(call.Pos())
			if tv.Value == nil {
				t.Errorf("%s：非常量转成 Key，可能造出表外的键", pos)
				return true
			}
			if tv.Value.Kind() != constant.String {
				t.Errorf("%s：Key 常量不是字符串", pos)
				return true
			}
			k := Key(constant.StringVal(tv.Value))
			if _, ok := zh[k]; !ok {
				t.Errorf("%s：转换出的键 %q 不在表里", pos, k)
			}
			return true
		})
	}
	t.Logf("i18n 生产代码里显式转成 Key 的地方：%d", nConv)
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
