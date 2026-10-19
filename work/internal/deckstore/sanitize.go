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
	if max > 0 && utf8.RuneCountInString(s) > max {
		r := []rune(s)
		s = string(r[:max]) + "…"
	}
	return s
}
