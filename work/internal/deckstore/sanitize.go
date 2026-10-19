package deckstore

import (
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 模型写的文本只当数据显示（架构稿 §3「凭据与展示安全」）：去掉终端控制序列、对疑似密钥打码、限长。
// 通知、决策摘要、子 agent 输出与汇报在落盘或显示前都要过这一道。

var (
	// CSI：ESC [ … 终止字节；OSC：ESC ] … BEL 或 ESC \；其他双字节 ESC 序列；以及 DCS/SOS/PM/APC（ESC P/X/^/_ … ESC \）。
	csiRe   = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]")
	oscRe   = regexp.MustCompile("\x1b\\][^\x07\x1b]*(\x07|\x1b\\\\)?")
	strRe   = regexp.MustCompile("\x1b[PX^_][^\x1b]*(\x1b\\\\)?")
	escRe   = regexp.MustCompile("\x1b[@-Z\\\\-_]")
	c1Re    = regexp.MustCompile("[\u0080-\u009f]")
	secrets = []*regexp.Regexp{
		regexp.MustCompile(`sk-[A-Za-z0-9_-]{8,}`),
		regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]{8,}`),
		regexp.MustCompile(`(?i)(token|api[_-]?key|secret|password|passwd)\s*[=:]\s*\S{4,}`),
		regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`xai-[A-Za-z0-9]{16,}`),
		regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	}
)

// Clean 返回可以安全显示的文本：去控制序列、打码、限长（按字符数，超出加「…」）。
// 打码记号是中文「[已打码]」，和 d36dd20 逐字一样：这条文本落盘后、给模型读、中文界面显示，都不用再翻译。
// 要作为 i18n 参数落盘的文字不走这里，走 ArgText / CleanArgs，它们存语言无关的标记。
func Clean(s string, max int) string {
	return clean(s, max, i18n.E("err.deckstore.clean.1").Error())
}

// ArgText 是 Clean 的参数版：打码处存语言无关的 i18n.RedactMark，渲染时再换成当前语言的记号。
// 凡是把文字放进 i18n.E 的参数，都用它，不用 Clean。
// 已经带中文「[已打码]」的字符串（先跑过 Clean 的）也一并换成标记，英文才不会留下中文。
func ArgText(s string, max int) string {
	return clean(strings.ReplaceAll(s, zhRedact(), i18n.RedactMark), max, i18n.RedactMark)
}

func zhRedact() string { return i18n.E("err.deckstore.clean.1").Error() }

func clean(s string, max int, mark string) string {
	s = strings.ToValidUTF8(s, "�")
	s = oscRe.ReplaceAllString(s, "")
	s = strRe.ReplaceAllString(s, "")
	s = csiRe.ReplaceAllString(s, "")
	s = escRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\x1b", "")
	s = c1Re.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	for _, re := range secrets {
		s = re.ReplaceAllString(s, mark)
	}
	return Clip(s, max)
}

// RedactLabel 是当前语言的打码记号：中文「[已打码]」，英文「[redacted]」。
// 参数里落盘的是 i18n.RedactMark，显示时 i18n.Present 换成它。
func RedactLabel() string { return i18n.T("err.deckstore.clean.1") }

// Clip 只按字符数截断，超出加「…」。不再去控制字符、不再打码。
// 译文整句用它。已经打过码的参数不要再跑一遍 Clean，免得标记被截断后对不上。
func Clip(s string, max int) string {
	if max > 0 && utf8.RuneCountInString(s) > max {
		r := []rune(s)
		return string(r[:max]) + "…"
	}
	return s
}

// CleanArgs 对参数里的字符串做和正文一样的清洗。嵌套码和列表只洗里面的文字，不拆结构，也不改码名。
func CleanArgs(args []any, max int) []any {
	if args == nil {
		return nil
	}
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = cleanArg(a, max)
	}
	return out
}

func cleanArg(a any, max int) any {
	switch v := a.(type) {
	case string:
		return ArgText(v, max)
	case i18n.Nest:
		v.Error = Clean(v.Error, max)
		v.Args = CleanArgs(v.Args, max)
		return v
	case i18n.List:
		v.Items = CleanArgs(v.Items, max)
		return v
	case *i18n.Error:
		if v == nil {
			return v
		}
		return i18n.E(v.Key, CleanArgs(v.Args, max)...)
	case map[string]any:
		if n, ok := i18n.NestFromMap(v); ok {
			return cleanArg(n, max)
		}
		if l, ok := i18n.ListFromMap(v); ok {
			return cleanArg(l, max)
		}
		return v
	default:
		if err, ok := a.(error); ok {
			if e := i18n.AsCoded(err); e != nil && e != a {
				return cleanArg(e, max)
			}
		}
		return a
	}
}

// Arg 把错误收成 i18n 参数。
// 带码的留下码，英文才能翻译。没有码的当作外部原文，返回清洗后的字符串。
// 清洗或截断改了原文时，外壳的 Error() 仍是清洗结果，所以外层中文和以前逐字一样；I18n() 仍是码。
func Arg(err error, max int) any {
	if err == nil {
		return ""
	}
	shown := Clean(err.Error(), max)
	e := i18n.AsCoded(err)
	if e == nil {
		return ArgText(err.Error(), max)
	}
	wire := scrubCoded(e, max)
	if wire.Error() == shown {
		return wire
	}
	return &keptShown{shown: shown, wire: wire}
}

// keptShown 的 Error() 是清洗、截断后的中文。码留给英文渲染。
type keptShown struct {
	shown string
	wire  *i18n.Error
}

func (k *keptShown) Error() string {
	if k == nil {
		return ""
	}
	return k.shown
}

func (k *keptShown) I18n() *i18n.Error {
	if k == nil {
		return nil
	}
	return k.wire
}

func scrubCoded(e *i18n.Error, max int) *i18n.Error {
	if e == nil || len(e.Args) == 0 {
		return e
	}
	args := make([]any, len(e.Args))
	changed := false
	for i, a := range e.Args {
		next, ch := scrubOne(a, max)
		args[i] = next
		if ch {
			changed = true
		}
	}
	if !changed {
		return e
	}
	return i18n.E(e.Key, args...)
}

func scrubOne(a any, max int) (any, bool) {
	switch v := a.(type) {
	case string:
		c := ArgText(v, max)
		if c == v {
			return v, false
		}
		return c, true
	case *i18n.Error:
		n := scrubCoded(v, max)
		return n, n != v
	case i18n.Nest:
		n := v
		n.Error = Clean(v.Error, max)
		n.Args = CleanArgs(v.Args, max)
		return n, !reflect.DeepEqual(n, v)
	case i18n.List:
		l := v
		l.Items = CleanArgs(v.Items, max)
		return l, !reflect.DeepEqual(l, v)
	case error:
		if e := i18n.AsCoded(v); e != nil {
			return scrubOne(e, max)
		}
		c := ArgText(v.Error(), max)
		return c, true
	default:
		return a, false
	}
}
