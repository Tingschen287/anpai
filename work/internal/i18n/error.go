package i18n

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

// RedactMark 是打码用的中性标记：5 个 Unicode 私用区字符，外部文本里不会自然出现，也不属于任何语言。
// 它和中文「[已打码]」一样长，所以先打码再按字符截断，截断位置和以前直接写入中文记号时相同。
//
// 文字清洗（deckstore.Clean）打码时只写这个标记。存下来的文字、线路上的文字都带着它，
// 在显示出口按语言换成记号：
//   - 给 owner 看的出口：Present，当前语言（中文「[已打码]」、英文「[redacted]」）。
//   - 给模型读的、落盘记账的、日志与 Error()：PresentZH，永远是中文「[已打码]」，和改码之前逐字一样。
//
// 外部原文里字面写着的「[已打码]」不是标记，原样保留。
// 外部原文一进来（HTTP 请求体、外部命令的输出、读进来的文件）就先过 Neutralize 去掉输入里本来就有的私用区字符，
// 外部文字伪造不出标记；之后清洗过的文字再进参数用 Tidy 语义，不再去标记，真标记才能活下来。
//
// 两条已知限制（返工 4 写明，不是遗漏）：
//   - 这次改动之前落盘的老数据里，真打码写的是中文「[已打码]」。它和外部原文里字面写的「[已打码]」长得一样，
//     无法可靠区分，所以英文界面里这些老文字仍显示中文记号，不迁移。只有新写入的数据才带中性标记。
//   - 降级到不认识中性标记的旧版本，读新写入的数据会把这 5 个私用区字符画成方块。升级后回退要先备份状态目录。
const RedactMark = "\uE000\uE001\uE002\uE003\uE004"

// MarkHeader 是请求头：界面（deckclient.NewUI）带上它，表示自己会把中性标记翻成当前语言，
// deckd 才在线路上留着标记。没有这个头的客户端（deck agent 命令、模型、旧工具）拿到的永远是中文「[已打码]」。
const (
	MarkHeader  = "X-Deck-Marks"
	MarkNeutral = "neutral"
)

// Neutralize 把文字里本来就有的标记字符换成 U+FFFD。外部文字因此伪造不出中性标记。
// 只处理标记用到的 5 个字符，别的私用区字符不动。
func Neutralize(s string) string {
	if !strings.ContainsAny(s, RedactMark) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r >= '\uE000' && r <= '\uE004' {
			return '\uFFFD'
		}
		return r
	}, s)
}

// NeutralizeBytes 对字节做 Neutralize。外部命令的输出、HTTP 响应、读进来的文件在边界上先过它：
// 标记字符在 UTF-8 里是 U+E000..U+E004（EE 80 80..84），换成 U+FFFD（EF BF BD）长度不变，所以不影响后面的解析。
func NeutralizeBytes(b []byte) []byte {
	if !bytes.Contains(b, []byte("\xee\x80")) {
		return b
	}
	return []byte(Neutralize(string(b)))
}

// HasMark 说明文字里有没有中性标记（含被截断的残缺标记）。
func HasMark(s string) bool { return strings.ContainsAny(s, RedactMark) }

// PresentZH 把中性标记换成中文「[已打码]」，不看当前语言。给模型、记账、日志、Error() 用。
func PresentZH(s string) string { return revealRedactZH(s) }

// redactKey 是打码记号的文字键。写在这里，避免 Error() 里出现非常量键。
const redactKey = Key("err.deckstore.clean.1")

// Present 把落盘标记换成当前语言的打码记号。没有标记时原样返回。
func Present(s string) string { return swapMark(s, T(redactKey)) }

// revealRedactZH 把落盘标记换成中文打码记号，不看当前语言。Error() 用它，中文原文因此和以前逐字一样。
func revealRedactZH(s string) string {
	phrase, ok := zh[redactKey]
	if !ok || phrase == "" {
		return s
	}
	return swapMark(s, phrase)
}

// swapMark 把标记换成 phrase。清洗先打码、后按字符截断，标记可能只剩前几个字符（截断处接着是「…」）。
// 残缺的标记是完整标记的前 k 个字符（1 到 4 个），换成 phrase 的前 k 个字符：
// 中文就是「[已」，和以前直接写入中文记号再截断的结果逐字一样。
// 完整标记先换掉，剩下的才按残缺处理，所以不看位置，字符串拼接之后也一样。
// 换完以后仍剩下的标记字符（不成形的，只能是外部伪造）换成 U+FFFD，不让私用区字符漏出去。
func swapMark(s, phrase string) string {
	if !strings.ContainsAny(s, RedactMark) {
		return s
	}
	s = strings.ReplaceAll(s, RedactMark, phrase)
	if !strings.ContainsAny(s, RedactMark) {
		return s
	}
	mark, ph := []rune(RedactMark), []rune(phrase)
	in, out := []rune(s), make([]rune, 0, len(s))
	for i := 0; i < len(in); {
		if in[i] != mark[0] {
			out = append(out, in[i])
			i++
			continue
		}
		k := 1
		for k < len(mark) && i+k < len(in) && in[i+k] == mark[k] {
			k++
		}
		cut := k
		if cut > len(ph) {
			cut = len(ph)
		}
		out = append(out, ph[:cut]...)
		i += k
	}
	return Neutralize(string(out))
}

// Nest 是嵌进参数里的另一条带码错误。
// 线路上它是对象，不是字符串。外部原文即使长得像旧的码标记，也不会被当成内层码。
type Nest struct {
	Code  string `json:"code"`
	Args  []any  `json:"args,omitempty"`
	Error string `json:"error,omitempty"`
}

// String 是中文原文。fmt 把嵌套码放进 Error() 时用它，中文原文因此不变。
func (n Nest) String() string { return n.Error }

// List 是一组按语言拼接的参数。String 用中文分隔符，放进 Error() 后中文原文不变。
// 线路上它是对象。界面按当前语言的分隔符再拼（中文「、」或「；」，英文 ", " 或 "; "）。
type List struct {
	Sep   Key   `json:"sep"`
	Items []any `json:"items"`
}

// ListOf 按分隔符键拼一组参数。sep 用 sep.list、sep.semi 或 sep.nl。
func ListOf(sep Key, items ...any) List {
	return List{Sep: sep, Items: append([]any(nil), items...)}
}

// String 用中文分隔符。fmt 在 Error() 里会调它。
func (l List) String() string {
	parts := make([]string, len(l.Items))
	for i, it := range l.Items {
		parts[i] = fmt.Sprint(it)
	}
	return strings.Join(parts, MustTextZH(l.Sep))
}

// Args 是线路上的参数列表。解码时用 json.Number，整数不经过 float64。
type Args []any

// UnmarshalJSON 用 json.Number 解数字，避免整数变成 float64 后 %d 打印失败，也避免超过 2^53 丢精度。
func (a *Args) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*a = nil
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	var raw []any
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	*a = raw
	return nil
}

// verbPat 与 i18n 测试里的占位符正则一致。%% 先剥掉，不算占位符。
var verbPat = regexp.MustCompile(`%[-+# 0]*[0-9]*(\.[0-9]+)?[a-zA-Z]`)

// Error 是带文字键的错误。Key 用 err.<包>.<含义>。
// Error() 永远用中文原文，所以日志和已有的 err.Error() 断言逐字不变。
// 界面要别的语言时走 Wire 取出键和参数，不把 Error() 的结果当译文。
type Error struct {
	Key  Key
	Args []any
}

// E 造一条带码错误。参数里的 error 可以再带码，Wire 会嵌进去。
func E(key Key, args ...any) *Error {
	if h := ArgHook; h != nil {
		h(key, args)
	}
	return &Error{Key: key, Args: args}
}

// Error 返回中文原文。中文表没有这条时返回键名，并在开了记录时记一笔。
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	s, ok := zh[e.Key]
	if !ok {
		noteMiss(e.Key)
		return string(e.Key)
	}
	if len(e.Args) == 0 {
		return s
	}
	return revealRedactZH(fmt.Errorf(s, safeArgs(e.Args)...).Error()) // safeArgs：不带码的 error 和兜底值的文字先中和（display_args.go）
}

// AsCoded 取出错误自己带的码。没有码就返回 nil。
// 外层没有码就沿 %w 往下找，和 Wire 的解包行为一致。
// 界面参数要嵌套码时用它，不要先调 Error() 把中文压成字符串。
func AsCoded(err error) *Error {
	for err != nil {
		if e := codedOf(err); e != nil {
			return e
		}
		err = errors.Unwrap(err)
	}
	return nil
}

// Unwrap 只解开格式串里的一个 %w，和 fmt.Errorf 一样。
// 多个 %w 时 fmt 只提供 Unwrap() []error，errors.Unwrap 得到 nil。这种情况靠 Is 和 As。
func (e *Error) Unwrap() error {
	return errors.Unwrap(e.fmtCause())
}

func (e *Error) fmtCause() error {
	if e == nil || len(e.Args) == 0 {
		return nil
	}
	s, ok := zh[e.Key]
	if !ok {
		return nil
	}
	return fmt.Errorf(s, e.Args...)
}

// Is 把多个 %w 也交给 errors.Is。一个 %w 时结果和直接拆包一样。
func (e *Error) Is(target error) bool {
	fe := e.fmtCause()
	if fe == nil {
		return false
	}
	return errors.Is(fe, target)
}

// As 把多个 %w 也交给 errors.As。
func (e *Error) As(target any) bool {
	fe := e.fmtCause()
	if fe == nil {
		return false
	}
	return errors.As(fe, target)
}

// I18n 让带码错误自己也能走 Wire。
func (e *Error) I18n() *Error { return e }

// coded 是具体错误类型自愿交出的码。具体类型仍用自己的 Error()，不额外包一层。
type coded interface{ I18n() *Error }

func codedOf(err error) *Error {
	if err == nil {
		return nil
	}
	if e, ok := err.(*Error); ok {
		return e
	}
	if c, ok := err.(coded); ok {
		return c.I18n()
	}
	return nil
}

// Wire 取最外层的码和线路参数。外层没有码就往下拆。
// 参数是字符串、数字、嵌套码对象或列表对象。普通字符串原样放行，不在里面认码。
func Wire(err error) (code string, args []any, ok bool) {
	for err != nil {
		if e := codedOf(err); e != nil && len(e.Key) > 0 {
			return string(e.Key), WireArgs(e.Args), true
		}
		err = errors.Unwrap(err)
	}
	return "", nil, false
}

// WireArgs 把参数收成线路上能放的值。
func WireArgs(args []any) []any {
	if len(args) == 0 {
		return []any{}
	}
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = wireArg(a)
	}
	return out
}

func wireArg(a any) any {
	if a == nil {
		return ""
	}
	switch v := a.(type) {
	case string:
		return v
	case *Error:
		return nestOf(v)
	case Nest:
		return Nest{Code: v.Code, Args: WireArgs(v.Args), Error: v.Error}
	case List:
		items := make([]any, len(v.Items))
		for i, it := range v.Items {
			items[i] = wireArg(it)
		}
		return List{Sep: v.Sep, Items: items}
	case json.Number:
		return numberArg(v)
	case int:
		return v
	case int8:
		return int64(v)
	case int16:
		return int64(v)
	case int32:
		return int64(v)
	case int64:
		return v
	case uint:
		return uint64(v)
	case uint8:
		return uint64(v)
	case uint16:
		return uint64(v)
	case uint32:
		return uint64(v)
	case uint64:
		return v
	case float32:
		return float64(v)
	case float64:
		return v
	}
	if err, ok := a.(error); ok {
		if e := AsCoded(err); e != nil && len(e.Key) > 0 {
			return nestOf(e)
		}
		// errors.Join 的结果：Error() 是各条用换行连起来。拆成列表，每一条各自带码，英文界面才没有中文标点残留。
		if kids := joinedErrors(err); kids != nil {
			items := make([]any, len(kids))
			for i, k := range kids {
				items[i] = wireArg(k)
			}
			return ListOf("sep.nl", items...)
		}
		return ErrText(err) // 不带码的 error 是外部原文：伪造的打码标记在这里中和（返工 2，M4）
	}
	rv := reflect.ValueOf(a)
	if !rv.IsValid() {
		return ""
	}
	if rv.Kind() == reflect.String {
		return Neutralize(rv.String()) // 具名字符串类型（档位、状态等）：不是 Clean 出来的字段，当外部原文（返工 2，M4）
	}
	// 具名类型（time.Duration、档位等）按 fmt 的原文来，不拆成底层数字。
	if rv.Type().PkgPath() != "" {
		return Neutralize(fmt.Sprint(a))
	}
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Uint()
	case reflect.Float32, reflect.Float64:
		return rv.Float()
	}
	return Neutralize(fmt.Sprint(a))
}

// joinedErrors 取 errors.Join 结果里的各条。别的多重包装（fmt.Errorf 带多个 %w）的 Error() 里有拼接文字，不当列表。
func joinedErrors(err error) []error {
	if reflect.TypeOf(err).String() != "*errors.joinError" {
		return nil
	}
	if u, ok := err.(interface{ Unwrap() []error }); ok {
		return u.Unwrap()
	}
	return nil
}

func nestOf(e *Error) Nest {
	if e == nil {
		return Nest{}
	}
	return Nest{Code: string(e.Key), Args: WireArgs(e.Args), Error: e.Error()}
}

// NestFromMap 认出线路上的嵌套码。只认 code、args、error 三个字段。
// 带 sep 的是列表，不是嵌套码。普通字符串不会进这里。
func NestFromMap(m map[string]any) (Nest, bool) {
	if m == nil {
		return Nest{}, false
	}
	if _, ok := m["sep"]; ok {
		return Nest{}, false
	}
	for k := range m {
		if k != "code" && k != "args" && k != "error" {
			return Nest{}, false
		}
	}
	code, ok := m["code"].(string)
	if !ok || code == "" {
		return Nest{}, false
	}
	var args []any
	if raw, exists := m["args"]; exists && raw != nil {
		arr, ok := raw.([]any)
		if !ok {
			return Nest{}, false
		}
		args = coerceNums(arr)
	}
	fallback, _ := m["error"].(string)
	return Nest{Code: code, Args: args, Error: fallback}, true
}

// ListFromMap 认出按语言拼接的列表。sep 只认 sep.list、sep.semi 和 sep.nl。
func ListFromMap(m map[string]any) (List, bool) {
	if m == nil {
		return List{}, false
	}
	for k := range m {
		if k != "sep" && k != "items" {
			return List{}, false
		}
	}
	sep, ok := m["sep"].(string)
	if !ok {
		return List{}, false
	}
	// 用字面量赋给 Key。非常量转换过不了 i18n 自己的检查。
	var sepKey Key
	switch sep {
	case "sep.list":
		sepKey = "sep.list"
	case "sep.semi":
		sepKey = "sep.semi"
	case "sep.nl":
		sepKey = "sep.nl"
	default:
		return List{}, false
	}
	raw, ok := m["items"].([]any)
	if !ok {
		return List{}, false
	}
	return List{Sep: sepKey, Items: coerceNums(raw)}, true
}

func numberArg(n json.Number) any {
	if i, err := n.Int64(); err == nil {
		return i
	}
	if f, err := n.Float64(); err == nil {
		return f
	}
	return n.String()
}

// ForceMiss 记一笔缺键。键在表里也记。参数个数不对时用它。
// 没开 RecordMisses 时什么都不做。
func ForceMiss(key Key) {
	if missDepth.Load() == 0 {
		return
	}
	missMu.Lock()
	if missDepth.Load() > 0 {
		missKeys = append(missKeys, key)
	}
	missMu.Unlock()
}

// Verbs 返回中文格式串里的占位符。键不在表里时 ok 为 false。
func Verbs(key Key) (verbs []string, ok bool) {
	s, ok := zh[key]
	if !ok {
		return nil, false
	}
	return verbPat.FindAllString(strings.ReplaceAll(s, "%%", ""), -1), true
}

// FormatOf 返回某语言的格式串。lang 只认 en，其它都用中文。
func FormatOf(lang string, key Key) (string, bool) {
	if lang == EN {
		if s, ok := en[key]; ok {
			return s, true
		}
	}
	s, ok := zh[key]
	return s, ok
}

// SprintfVerb 把格式串里的 %w 换成 %s，再 Sprintf。%% 不动。
// 界面翻译时内层错误已经变成字符串，不能再留 %w。
func SprintfVerb(format string, args []any) string {
	format = widenW(format)
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}

func widenW(format string) string {
	var b strings.Builder
	for i := 0; i < len(format); {
		if format[i] != '%' {
			b.WriteByte(format[i])
			i++
			continue
		}
		if i+1 < len(format) && format[i+1] == '%' {
			b.WriteString("%%")
			i += 2
			continue
		}
		j := i + 1
		for j < len(format) && !isVerbByte(format[j]) {
			j++
		}
		if j < len(format) && format[j] == 'w' {
			b.WriteString(format[i:j])
			b.WriteByte('s')
			i = j + 1
			continue
		}
		if j < len(format) {
			j++
		}
		b.WriteString(format[i:j])
		i = j
	}
	return b.String()
}

func isVerbByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func coerceNums(args []any) []any {
	if args == nil {
		return nil
	}
	out := make([]any, len(args))
	for i, a := range args {
		if n, ok := a.(json.Number); ok {
			out[i] = numberArg(n)
			continue
		}
		out[i] = a
	}
	return out
}
