package i18n

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// 打码标记：完整的换成记号；被截断只剩前几个字符的，换成记号的前几个字符，不管它在字符串的哪个位置
// （清洗之后字符串还会被拼接）。中文逐字等于以前直接写「[已打码]」再截断的结果。
func TestN060PresentHandlesTruncatedMarksAnywhere(t *testing.T) {
	SetLang(ZH)
	t.Cleanup(func() { SetLang(ZH) })
	mk := []rune(RedactMark)
	for k := 1; k <= 4; k++ {
		part := string(mk[:k])
		in := "前" + part + "…（后）"
		zh := "前" + string([]rune("[已打码]")[:k]) + "…（后）"
		if got := PresentZH(in); got != zh {
			t.Fatalf("k=%d 中文：%q，想要 %q", k, got, zh)
		}
		if got := Present(in); got != zh {
			t.Fatalf("k=%d 中文界面：%q", k, got)
		}
	}
	// 完整标记、残缺标记混在一起
	in := RedactMark + "-" + string(mk[:2]) + "…" + RedactMark
	if got := PresentZH(in); got != "[已打码]-[已…[已打码]" {
		t.Fatalf("混合：%q", got)
	}
	SetLang(EN)
	if got := Present(RedactMark + " x"); got != "[redacted] x" {
		t.Fatalf("英文：%q", got)
	}
	if got := Present(string(mk[:3]) + "…"); got != "[re…" {
		t.Fatalf("英文残缺：%q", got)
	}
	// 没有标记的文字原样，包括字面的 [已打码]
	for _, s := range []string{"", "plain", "literal [已打码] end", "unicode 私有区  "} {
		if Present(s) != s || PresentZH(s) != s {
			t.Fatalf("没有标记的文字不该变：%q", s)
		}
	}
	// 不成形的标记字符（外部伪造）不漏出去
	if got := Present("abc"); strings.ContainsAny(got, RedactMark) {
		t.Fatalf("不成形的标记不该漏出：%q", got)
	}
}

// Neutralize 只动标记用到的 5 个字符，别的私用区字符不动。
func TestN060NeutralizeOnlyTouchesMarkChars(t *testing.T) {
	if got := Neutralize("x" + RedactMark + "yz"); got != "x�����yz" {
		t.Fatalf("%q", got)
	}
	if Neutralize("plain") != "plain" || !HasMark("a") || HasMark("a") {
		t.Fatal("HasMark / Neutralize")
	}
}

// Error() 永远是中文：标记换成中文记号，包括放在嵌套码、列表里的字符串参数。
func TestN060ErrorTextRevealsMarkAsChinese(t *testing.T) {
	SetLang(EN)
	t.Cleanup(func() { SetLang(ZH) })
	e := E("err.deckd.serverrunspawn.47", "token "+RedactMark+" end")
	if got := e.Error(); strings.ContainsAny(got, RedactMark) || !strings.Contains(got, "token [已打码] end") {
		t.Fatalf("Error() 应是中文记号：%q", got)
	}
	nest := E("err.deckd.serverrunspawn.47", Nest{Code: "x", Error: "内层 " + RedactMark})
	if got := nest.Error(); !strings.Contains(got, "内层 ") {
		t.Fatalf("Nest 的 String 应是中文原文：%q", got)
	}
}

// AsCoded 沿 %w 往下找，和 Wire 一致；Wire 与 WireArgs 对包装的处理相同。
func TestN060AsCodedUnwrapsChain(t *testing.T) {
	inner := E("err.agentdeck.clientlaunch.4")
	if AsCoded(fmt.Errorf("%w", inner)) != inner || AsCoded(fmt.Errorf("a: %w", fmt.Errorf("b: %w", inner))) != inner {
		t.Fatal("AsCoded 应取到被包装的码")
	}
	if AsCoded(errors.New("x")) != nil || AsCoded(nil) != nil {
		t.Fatal("没有码就是 nil")
	}
	wrapped := fmt.Errorf("%w", inner)
	c1, _, ok1 := Wire(wrapped)
	n, isNest := WireArgs([]any{wrapped})[0].(Nest)
	if !ok1 || !isNest || n.Code != c1 {
		t.Fatalf("Wire 与 WireArgs 应取同一个码：%q %+v", c1, n)
	}
}

// 换行列表：errors.Join 的结果拆成每条各自带码的列表，Error() 原文不变。
func TestN060ListSepKeys(t *testing.T) {
	for _, k := range []Key{"sep.list", "sep.semi", "sep.nl"} {
		if _, ok := TextZH(k); !ok {
			t.Fatalf("中文缺 %s", k)
		}
		if _, ok := TextEN(k); !ok {
			t.Fatalf("英文缺 %s", k)
		}
	}
	l := ListOf("sep.nl", "a", "b")
	if l.String() != "a\nb" {
		t.Fatalf("%q", l.String())
	}
	joined := errors.Join(E("err.agentdeck.clientlaunch.4"), errors.New("plain"))
	arg := WireArgs([]any{joined})[0]
	lst, ok := arg.(List)
	if !ok || lst.Sep != "sep.nl" || len(lst.Items) != 2 {
		t.Fatalf("errors.Join 应拆成两项的列表：%#v", arg)
	}
	if _, ok := lst.Items[0].(Nest); !ok || lst.Items[1] != "plain" {
		t.Fatalf("有码的一条嵌套、无码的当外部原文：%#v", lst.Items)
	}
	if lst.String() != joined.Error() {
		t.Fatalf("中文原文应和 errors.Join 逐字一样：%q / %q", lst.String(), joined.Error())
	}
	if m := map[string]any{"sep": "sep.nl", "items": []any{"x"}}; func() bool { _, ok := ListFromMap(m); return !ok }() {
		t.Fatal("线路上的 sep.nl 列表应能认出来")
	}
	// 带多个 %w 的 fmt.Errorf 不是列表：Error() 里有拼接文字，拆开会丢
	multi := fmt.Errorf("a %w b %w", errors.New("x"), errors.New("y"))
	if _, ok := WireArgs([]any{multi})[0].(string); !ok {
		t.Fatal("fmt.Errorf 的多重包装应当字符串")
	}
}
