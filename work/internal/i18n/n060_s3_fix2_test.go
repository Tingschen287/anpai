package i18n

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// 段 3 返工 2，M4：外部文字变成显示参数的统一出口。
// 守卫：随便造一个不带码的 error（或兜底值），文字里放原样的标记字符（解码后的转义就是这个样子），
// 经 Wire、Error()、Present、PresentZH、T、Errorf 都显示不出打码短语；字面写着的反斜杠转义文字不动；真标记（字符串参数）的往返不坏。

// forgedTexts 是伪造的文字：完整标记、残缺标记、夹在文字中间、重复、只有一个字符。
func forgedTexts() []string {
	return []string{
		RedactMark,
		"",
		"前缀 " + RedactMark + " 后缀",
		RedactMark + RedactMark,
		"",
		"cannot unmarshal !!str `" + RedactMark + "` into int",
	}
}

type fixStringer struct{ s string }

func (f fixStringer) String() string { return f.s }

type fixNamed string

type fixStruct struct{ Name string }

// forgedArgs 把一段伪造文字造成各种形状的参数：不带码的 error、被包住的、errors.Join、Stringer、具名字符串、结构体、指针、切片。
func forgedArgs(txt string) map[string]any {
	plain := errors.New(txt)
	return map[string]any{
		"plain error":     plain,
		"wrapped":         fmt.Errorf("读取 %s：%w", "a.yaml", plain),
		"wrapped w/ text": fmt.Errorf("外部说 %s", txt),
		"joined":          errors.Join(plain, errors.New("另一条")),
		"stringer":        fixStringer{txt},
		"named string":    fixNamed(txt),
		"struct":          fixStruct{txt},
		"struct pointer":  &fixStruct{txt},
		"slice":           []string{txt, "x"},
		"map":             map[string]string{"k": txt},
	}
}

func noPhrase(t *testing.T, where, s string) {
	t.Helper()
	if HasMark(s) {
		t.Errorf("%s：还带着标记字符：%q", where, s)
	}
	for _, bad := range []string{"[已打码]", "[redacted]", "[已", "[r"} {
		if strings.Contains(s, bad) {
			t.Errorf("%s：显示出了打码短语 %q：%q", where, bad, s)
		}
	}
}

func TestN060S3Fix2NoCodedErrorShowsARedactionPhrase(t *testing.T) {
	t.Cleanup(func() { SetLang(ZH) })
	for _, lang := range []string{ZH, EN} {
		SetLang(lang)
		for _, txt := range forgedTexts() {
			for name, a := range forgedArgs(txt) {
				at := fmt.Sprintf("%s/%q/%s", lang, txt, name)
				e := E("err.deckclient.status", 502, a)
				code, args, ok := Wire(e)
				if !ok || code != "err.deckclient.status" || len(args) != 2 {
					t.Fatalf("%s：Wire %v %v %v", at, code, args, ok)
				}
				noPhrase(t, at+" Wire 参数", fmt.Sprint(args))
				noPhrase(t, at+" Wire 参数 Present", Present(fmt.Sprint(args)))
				noPhrase(t, at+" Wire 参数 PresentZH", PresentZH(fmt.Sprint(args)))
				text := e.Error()
				noPhrase(t, at+" Error()", text)
				noPhrase(t, at+" Present(Error())", Present(text))
				noPhrase(t, at+" PresentZH(Error())", PresentZH(text))
				shown := T("main.start.fail", "p", a)
				noPhrase(t, at+" T", shown)
				noPhrase(t, at+" Present(T)", Present(shown))
				fe := Errorf("main.start.fail", "p", a)
				noPhrase(t, at+" Errorf", fe.Error())
				noPhrase(t, at+" Present(Errorf)", Present(fe.Error()))
			}
			// 不带码的 error 本身
			noPhrase(t, lang+" ErrText", ErrText(errors.New(txt)))
			noPhrase(t, lang+" ErrText(wrapped)", ErrText(fmt.Errorf("外面 %w", errors.New(txt))))
		}
	}
}

// 字面写着的转义文字（反斜杠 u e000）不是标记，不动；普通文字也不动。
func TestN060S3Fix2LiteralEscapeTextIsUntouched(t *testing.T) {
	bs := "\\" // 一个反斜杠；转义文字用拼的，免得源码里的 \u 被当成真字符
	lit := "路径 " + bs + "u" + "e000" + bs + "u" + "E004 与 " + bs + bs + "u" + "E000 还有 [已打码] 字面"
	e := E("err.deckclient.status", 502, errors.New(lit))
	_, args, _ := Wire(e)
	if args[1] != lit {
		t.Fatalf("Wire 参数被改了：%q", args[1])
	}
	if got := e.Error(); got != "deckd 502："+lit {
		t.Fatalf("Error() 被改了：%q", got)
	}
	if got := ErrText(errors.New(lit)); got != lit {
		t.Fatalf("ErrText 被改了：%q", got)
	}
	if got := T("main.start.fail", "p", fixStringer{lit}); got != "起「p」的主 agent 失败："+lit {
		t.Fatalf("T 被改了：%q", got)
	}
}

// 真标记的正常往返：字符串参数里的真标记原样过线路，Error() 是中文记号，英文界面显示 [redacted]。
// 字符串和 Nest、List 里的字符串都是 deckstore.Clean 的结果，不被统一中和碰到。
func TestN060S3Fix2GenuineMarkInStringArgsRoundTrips(t *testing.T) {
	t.Cleanup(func() { SetLang(ZH) })
	genuine := "令牌 " + RedactMark + " 已隐藏"
	e := E("err.deckclient.status", 502, genuine)
	_, args, _ := Wire(e)
	if args[1] != genuine {
		t.Fatalf("字符串参数里的真标记被改了：%q", args[1])
	}
	if got := e.Error(); got != "deckd 502：令牌 [已打码] 已隐藏" {
		t.Fatalf("Error() 应是中文记号：%q", got)
	}
	SetLang(EN)
	if got := Present(genuine); got != "令牌 [redacted] 已隐藏" {
		t.Fatalf("英文显示：%q", got)
	}
	// 嵌套码与列表里的真标记也不动
	nest := E("err.deckclient.status", 502, E("err.deckclient.status", 500, genuine))
	_, nargs, _ := Wire(nest)
	n, ok := nargs[1].(Nest)
	if !ok || n.Args[1] != genuine {
		t.Fatalf("嵌套码里的真标记被改了：%#v", nargs[1])
	}
	l := E("err.deckclient.status", 502, ListOf("sep.list", genuine, "x"))
	_, largs, _ := Wire(l)
	if li, ok := largs[1].(List); !ok || li.Items[0] != genuine {
		t.Fatalf("列表里的真标记被改了：%#v", largs[1])
	}
	// 带码错误里的外部错误：真标记已经被它自己的 Error() 换成中文记号，外部伪造的被中和
	inner := E("err.deckclient.status", 500, genuine)
	outer := E("err.deckclient.status", 502, fmt.Errorf("外面 %w 伪造 %s", inner, RedactMark))
	noMark := ErrText(outer)
	if HasMark(noMark) || !strings.Contains(outer.Error(), "令牌 [已打码] 已隐藏") {
		t.Fatalf("带码错误外面包着的外部文字：%q / %q", noMark, outer.Error())
	}
	if strings.Count(outer.Error(), "[已打码]") != 1 {
		t.Fatalf("外部伪造的标记不该变成记号，只该有真标记那一处：%q", outer.Error())
	}
}

// 中和不改变 errors.Is / As / Unwrap 的行为（Errorf 和 *Error 里的 %w 照常能找到原错误）。
func TestN060S3Fix2NeutralizingKeepsErrorChains(t *testing.T) {
	sentinel := errors.New("哨兵 " + RedactMark)
	e := E("err.deckclient.dial", sentinel)
	if !errors.Is(e, sentinel) {
		t.Error("*Error 的 errors.Is 找不到原错误")
	}
	fe := Errorf("err.deckclient.dial", sentinel)
	if !errors.Is(fe, sentinel) {
		t.Error("Errorf 的 errors.Is 找不到原错误")
	}
	var target *fixErr
	wrapped := fmt.Errorf("x: %w", &fixErr{})
	if !errors.As(Errorf("err.deckclient.dial", wrapped), &target) {
		t.Error("Errorf 的 errors.As 找不到原错误类型")
	}
	if HasMark(fe.Error()) || HasMark(e.Error()) {
		t.Errorf("文字里还有标记字符：%q %q", fe.Error(), e.Error())
	}
}

type fixErr struct{}

func (*fixErr) Error() string { return "fix" }

// 语言代次只在语言真的变了才加：设成同一个语言不算，不会让正常响应被误丢。
func TestN060S3Fix2LangSeqOnlyCountsRealChanges(t *testing.T) {
	SetLang(ZH)
	t.Cleanup(func() { SetLang(ZH) })
	a := LangSeq()
	SetLang(ZH)
	SetLang("fr") // 不认识的回到中文，仍是同一个语言
	if LangSeq() != a {
		t.Fatal("设成同一个语言不该加代次")
	}
	SetLang(EN)
	if LangSeq() != a+1 {
		t.Fatalf("语言变了该加一：%d -> %d", a, LangSeq())
	}
}
