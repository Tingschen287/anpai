package agentdeck

import (
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

// 算不出来的就得老实说算不出来，不许猜。
func TestRuntimeBuiltStringsAreNotGuessed(t *testing.T) {
	const src = `package p

import "fmt"

var a = fmt.Sprintf("send-%s", "keys")`

	fset, path := fixture(t, src)
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := constStringDecl(f, "a"); ok {
		t.Errorf("Sprintf 的结果要到运行时才知道，却算出了 %q", got)
	}
}
