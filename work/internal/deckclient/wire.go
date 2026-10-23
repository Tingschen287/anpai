package deckclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// Show 是界面上显示的一句错误。
// 中文用 err.Error() 的原文。英文在码对得上时用译文。
// HTTP 错误的「deckd 状态码：」前缀在英文里一并翻译。
// 文字里的中性打码标记按当前语言换成记号。
func Show(err error) string { return i18n.Present(show(err)) }

func show(err error) string {
	if err == nil {
		return ""
	}
	var de *Error
	if errors.As(err, &de) && de != nil {
		msg := de.Message
		if de.Code != "" {
			msg = Text(de.Code, de.Args, de.Message)
		}
		// 没有码的 HTTP 错误（上游原文）在英文里也翻前缀：「deckd 502: …」，不留中文冒号
		if i18n.Lang() == i18n.EN {
			return i18n.T("err.deckclient.status", de.Status, msg)
		}
		return de.Error()
	}
	// 注意：这里不中和 err.Error()。最外层不带码的错误可以带着真标记——例如中间窗格的 tmux 退出说明，
	// 测试里（layout.TestN060CenterPauseReasonTranslatesTheMark）就用不带码的错误装一个 Clean 过的文字，这里要把它翻成当前语言的记号。
	// 外部文字变成「参数」的出口（Wire、(*Error).Error()、T、Errorf）在 i18n 里统一中和了，进不了这里（返工 2，M4，见 i18n/display_args.go）
	if code, args, ok := i18n.Wire(err); ok {
		return Text(code, args, err.Error())
	}
	return err.Error()
}

// Text 按当前语言翻译线路上的错误码。
// 码必须先在 Keys() 里，而且占位符个数和 args 一致，才翻译。
// 对不上就返回 fallback（原来的 error 文字），不显示键名，并记一次缺键。
// 中文界面也走这次校验，但显示仍用 fallback，保证和改码之前逐字一样。
// 这是全模块唯一一处把非常量字符串转成 i18n.Key 的地方。
// 返回前把文字里的中性打码标记（兜底文字里可能有）换成当前语言的记号。
func Text(code string, args []any, fallback string) string {
	rendered, ok := renderCoded(code, args, 0)
	if !ok || i18n.Lang() != i18n.EN {
		return i18n.Present(fallback)
	}
	return rendered
}

func renderCoded(code string, args []any, depth int) (string, bool) {
	if code == "" || depth > 8 {
		return "", false
	}
	known := false
	for _, k := range i18n.Keys() {
		if string(k) == code {
			known = true
			break
		}
	}
	key := i18n.Key(code)
	if !known {
		i18n.ForceMiss(key)
		return "", false
	}
	verbs, ok := i18n.Verbs(key)
	if !ok || len(verbs) != len(args) {
		i18n.ForceMiss(key)
		return "", false
	}
	if i18n.Lang() == i18n.EN {
		if _, enOK := i18n.TextEN(key); !enOK {
			i18n.ForceMiss(key)
			return "", false
		}
	}
	format, ok := i18n.FormatOf(i18n.Lang(), key)
	if !ok {
		i18n.ForceMiss(key)
		return "", false
	}
	return i18n.SprintfVerb(format, expandArgs(args, depth)), true
}

func expandArgs(args []any, depth int) []any {
	if len(args) == 0 {
		return nil
	}
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = expandArg(a, depth)
	}
	return out
}

func expandArg(a any, depth int) any {
	switch v := a.(type) {
	case json.Number:
		return numberForVerb(v)
	case float64:
		// 旧解码可能把整数变成 float64。只有恰好落在 int64 里的才还原，2.5 仍是小数。
		if i, ok := exactInt64(v); ok {
			return i
		}
		return v
	case string:
		// 普通字符串不是嵌套码。外部原文即使长得像码，也原样显示。
		// 落盘的打码标记在这里换成当前语言的记号。
		return i18n.Present(v)
	case i18n.Nest:
		return renderNest(v, depth)
	case i18n.List:
		return joinList(v, depth)
	case map[string]any:
		if l, ok := i18n.ListFromMap(v); ok {
			return joinList(l, depth)
		}
		if n, ok := i18n.NestFromMap(v); ok {
			return renderNest(n, depth)
		}
		return v
	default:
		return a
	}
}

func numberForVerb(v json.Number) any {
	if i, err := v.Int64(); err == nil {
		return i
	}
	if f, err := v.Float64(); err == nil {
		return f
	}
	return v.String()
}

func exactInt64(f float64) (int64, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) || f > math.MaxInt64 || f < math.MinInt64 {
		return 0, false
	}
	i := int64(f)
	return i, float64(i) == f
}

func renderNest(n i18n.Nest, depth int) any {
	if s, ok := renderCoded(n.Code, n.Args, depth+1); ok {
		return s
	}
	if n.Error != "" {
		return i18n.Present(n.Error)
	}
	return n.Code
}

func joinList(l i18n.List, depth int) string {
	parts := make([]string, len(l.Items))
	for i, it := range l.Items {
		parts[i] = fmt.Sprint(expandArg(it, depth))
	}
	return strings.Join(parts, i18n.T(l.Sep))
}
