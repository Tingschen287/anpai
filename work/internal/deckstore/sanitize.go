package deckstore

import (
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"regexp"
	"strings"
	"unicode/utf8"
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
func Clean(s string, max int) string {
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
		s = re.ReplaceAllString(s, i18n.E("err.deckstore.clean.1").Error())
	}
	return Clip(s, max)
}

// Clip 只按字符数截断，超出加「…」。不再去控制字符、不再打码。
// 译文整句用它。已经打过码的参数不要再跑一遍 Clean，免得英文句子里再出现中文打码标记。
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
		return Clean(v, max)
	case i18n.Nest:
		v.Error = Clean(v.Error, max)
		v.Args = CleanArgs(v.Args, max)
		return v
	case i18n.List:
		v.Items = CleanArgs(v.Items, max)
		return v
	case map[string]any:
		if n, ok := i18n.NestFromMap(v); ok {
			return cleanArg(n, max)
		}
		if l, ok := i18n.ListFromMap(v); ok {
			return cleanArg(l, max)
		}
		return v
	default:
		return a
	}
}
