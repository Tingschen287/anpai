package i18n

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

// Nest 是嵌进参数里的另一条带码错误。
// 线路上它是对象，不是字符串。外部原文即使长得像旧的码标记，也不会被当成内层码。
type Nest struct {
	Code  string `json:"code"`
	Args  []any  `json:"args,omitempty"`
	Error string `json:"error,omitempty"`
}

// List 是一组按语言拼接的参数。String 用中文分隔符，放进 Error() 后中文原文不变。
// 线路上它是对象。界面按当前语言的分隔符再拼（中文「、」或「；」，英文 ", " 或 "; "）。
type List struct {
	Sep   Key   `json:"sep"`
	Items []any `json:"items"`
}

// ListOf 按分隔符键拼一组参数。sep 用 sep.list 或 sep.semi。
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
	return fmt.Errorf(s, e.Args...).Error()
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
		if e := codedOf(err); e != nil && len(e.Key) > 0 {
			return nestOf(e)
		}
		return err.Error()
	}
	rv := reflect.ValueOf(a)
	if !rv.IsValid() {
		return ""
	}
	if rv.Kind() == reflect.String {
		return rv.String()
	}
	// 具名类型（time.Duration、档位等）按 fmt 的原文来，不拆成底层数字。
	if rv.Type().PkgPath() != "" {
		return fmt.Sprint(a)
	}
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Uint()
	case reflect.Float32, reflect.Float64:
		return rv.Float()
	}
	return fmt.Sprint(a)
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

// ListFromMap 认出按语言拼接的列表。sep 只认 sep.list 和 sep.semi。
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
