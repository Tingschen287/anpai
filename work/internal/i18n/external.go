package i18n

import (
	"bytes"
	"encoding/json"
	"reflect"
)

// 外部原文解码之后的中和（n060 段 3，第 2 段复审 4 的必改 1）。
//
// 只在解码前处理字节挡不住转义：JSON 的 "\ue000" 和 YAML 双引号字符串的 "\ue000"，在字节里没有私用区字符，
// 解码才把它们变成真的标记字符。所以外部 JSON、YAML 在解码之后的字符串边界再中和一次：
// NeutralizeValue 走遍字符串、map 的键和值、嵌套值、接口里的值，对 json.RawMessage 按 JSON 转义规则处理。
// 只用在外部原文解码出来的值上；deck 自己写的、带真标记的值不能过它。

var rawMessageType = reflect.TypeOf(json.RawMessage(nil))

// NeutralizeValue 把 v 指向的值里所有字符串里的标记字符换成 U+FFFD。v 必须是指针，否则什么也不做。
// 覆盖：字符串字段、嵌套结构、指针、切片、数组、map 的键和值、接口里的值、json.RawMessage（含 JSON 转义写法）。
// 未导出的字段改不了，原样留着；外部解码器也不会往里写。
func NeutralizeValue(v any) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return
	}
	neutralizeWalk(rv.Elem())
}

func neutralizeWalk(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() {
			if s := v.String(); HasMark(s) {
				v.SetString(Neutralize(s))
			}
		}
	case reflect.Pointer:
		if !v.IsNil() {
			neutralizeWalk(v.Elem())
		}
	case reflect.Interface:
		if v.IsNil() || !v.CanSet() {
			return
		}
		e := v.Elem()
		cp := reflect.New(e.Type()).Elem()
		cp.Set(e)
		neutralizeWalk(cp)
		v.Set(cp)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			neutralizeWalk(v.Field(i))
		}
	case reflect.Slice:
		if v.Type() == rawMessageType {
			if v.CanSet() && !v.IsNil() {
				v.SetBytes(NeutralizeJSON(v.Bytes()))
			}
			return
		}
		for i := 0; i < v.Len(); i++ {
			neutralizeWalk(v.Index(i))
		}
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			neutralizeWalk(v.Index(i))
		}
	case reflect.Map:
		if v.IsNil() || !v.CanSet() {
			return
		}
		for _, k := range v.MapKeys() {
			val := v.MapIndex(k)
			cp := reflect.New(val.Type()).Elem()
			cp.Set(val)
			neutralizeWalk(cp)
			nk := k
			if k.Kind() == reflect.String && HasMark(k.String()) {
				nk = reflect.New(k.Type()).Elem()
				nk.SetString(Neutralize(k.String()))
				v.SetMapIndex(k, reflect.Value{})
			}
			v.SetMapIndex(nk, cp)
		}
	}
}

// NeutralizeJSON 对一段 JSON 文本做 Neutralize：字节里的标记字符换成 U+FFFD，
// 字符串里的 \ue000..\ue004 转义写法换成 \uFFFD。json.RawMessage 在被第二次解码之前要先过它。
// 转义按 JSON 的规则逐个认：反斜杠总是和后一个字符成对（\\ 之后的 uE000 是普通文字，不动）。
// 输入不是合法 JSON 也不会出错，只是照样替换能认出来的转义。
func NeutralizeJSON(b []byte) []byte {
	b = NeutralizeBytes(b)
	if !bytes.Contains(b, []byte{'\\'}) {
		return b
	}
	var out []byte // 有替换时才复制
	for i := 0; i < len(b); {
		if b[i] != '\\' || i+1 >= len(b) {
			if out != nil {
				out = append(out, b[i])
			}
			i++
			continue
		}
		if b[i+1] == 'u' && i+6 <= len(b) && isMarkEscape(b[i+2:i+6]) {
			if out == nil {
				out = append(make([]byte, 0, len(b)), b[:i]...)
			}
			out = append(out, '\\', 'u', 'F', 'F', 'F', 'D')
			i += 6
			continue
		}
		if out != nil {
			out = append(out, b[i], b[i+1])
		}
		i += 2
	}
	if out == nil {
		return b
	}
	return out
}

// isMarkEscape 说明四位十六进制数是不是 E000..E004（大小写都认）。
func isMarkEscape(h []byte) bool {
	if len(h) != 4 || (h[0] != 'e' && h[0] != 'E') || h[1] != '0' || h[2] != '0' {
		return false
	}
	return h[3] >= '0' && h[3] <= '4'
}

// UnmarshalExternal 解码外部来的 JSON（上游命令的输出、HTTP 响应、事件、缓存文件），并在解码之后中和：
// 先 NeutralizeJSON 处理字节，再对解出来的值走一遍 NeutralizeValue。v 必须是指针。
func UnmarshalExternal(data []byte, v any) error {
	if err := json.Unmarshal(NeutralizeJSON(data), v); err != nil {
		return err
	}
	NeutralizeValue(v)
	return nil
}

// DecodeExternal 同 UnmarshalExternal，但像 json.Decoder 一样只解第一个 JSON 值、允许后面还有内容。
func DecodeExternal(data []byte, v any) error {
	if err := json.NewDecoder(bytes.NewReader(NeutralizeJSON(data))).Decode(v); err != nil {
		return err
	}
	NeutralizeValue(v)
	return nil
}
