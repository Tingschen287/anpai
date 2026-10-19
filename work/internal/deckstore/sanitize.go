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

// Clean 清洗外部来的文字：去控制序列、打码、限长（按字符数，超出加「…」）。
// 打码只写中性标记 i18n.RedactMark，不写任何语言的记号；输入里本来就有的标记字符先被 Neutralize 去掉，伪造不出来。
// 清洗过的文字落盘、上线路、进参数都带着标记，显示出口再翻译：
//   - 给 owner 看：Display（或 i18n.Present），按当前语言。
//   - 给模型读、落盘记账、日志、Error()：i18n.PresentZH，中文「[已打码]」，和改码之前逐字一样。
//
// 外部原文里字面写着的「[已打码]」不是标记，原样保留。
func Clean(s string, max int) string { return clean(i18n.Neutralize(s), max) }

// ArgText 是放进 i18n.E 参数的文字的清洗：规则和 Clean 一样，打码存中性标记，外部原文里字面的「[已打码]」不被改写。
// 参数常常是已经清洗过的文字（Arg 的结果、存下来的汇报），所以和 Tidy 一样不去掉里面的标记，免得二次清洗把标记抹掉。
// 还没清洗过的外部文字走 Clean 或 Arg。
func ArgText(s string, max int) string { return Tidy(s, max) }

// Tidy 是对已经清洗过的文字再清洗一遍：规则和 Clean 一样，但不去掉里面的标记。
// 显示出口、再次限长时用它；还没清洗过的外部文字用 Clean。
func Tidy(s string, max int) string { return clean(s, max) }

// Display 是给 owner 看的出口：Tidy 之后把标记换成当前语言的记号。
// 界面（deckui、panels、layout、cmd/deck）显示文字前都走它，不直接调 Clean。
func Display(s string, max int) string { return i18n.Present(Tidy(s, max)) }

func clean(s string, max int) string {
	mark := i18n.RedactMark
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
// 存下来的文字里是 i18n.RedactMark，显示时 i18n.Present 换成它。
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
		return Tidy(v, max)
	case i18n.Nest:
		v.Error = Tidy(v.Error, max)
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
	// Error() 永远是中文：清洗出来的标记换成中文记号，外壳的 Error() 才和改码之前逐字一样。
	shown := i18n.PresentZH(Clean(err.Error(), max))
	e := i18n.AsCoded(err)
	if e == nil {
		return Clean(err.Error(), max) // 没有码的是外部原文：第一次清洗，顺带去掉伪造的标记
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
		n.Error = Tidy(v.Error, max)
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
		c := Clean(v.Error(), max)
		return c, true
	default:
		return a, false
	}
}
