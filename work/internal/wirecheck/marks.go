package wirecheck

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 解码后的值里有没有中性打码标记（n060 段 3，第 2 段复审 4 的建议 1 / 必改 1 的行为测试）。
// 入口清单的守卫只证明文件登记过；这里给每个「已中和」入口的行为测试用：喂一份带 \ue000 转义的输入，
// 解码之后用 MarksIn 查，必须是空的。

var markEscapeRe = regexp.MustCompile(`(?i)\\u[eE]00[0-4]`)

// MarksIn 走遍 v（指针、结构体的导出字段、切片、数组、map 的键和值、接口、json.RawMessage），
// 返回含标记字符的字符串所在的位置。RawMessage 既按文本查（含 \ue000 转义写法），也解码后查。
func MarksIn(v any) []string {
	var hits []string
	walkMarks(reflect.ValueOf(v), "$", &hits, 0)
	sort.Strings(hits)
	return hits
}

var rawType = reflect.TypeOf(json.RawMessage(nil))

func walkMarks(v reflect.Value, path string, hits *[]string, depth int) {
	if !v.IsValid() || depth > 40 {
		return
	}
	switch v.Kind() {
	case reflect.String:
		if s := v.String(); i18n.HasMark(s) {
			*hits = append(*hits, fmt.Sprintf("%s=%q", path, s))
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			walkMarks(v.Elem(), path, hits, depth+1)
		}
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			if t.Field(i).IsExported() {
				walkMarks(v.Field(i), path+"."+t.Field(i).Name, hits, depth+1)
			}
		}
	case reflect.Slice:
		if v.Type() == rawType {
			raw := v.Bytes()
			if i18n.HasMark(string(raw)) || markEscapeRe.Match(raw) {
				*hits = append(*hits, fmt.Sprintf("%s(raw)=%q", path, raw))
			}
			var any1 any
			if json.Unmarshal(raw, &any1) == nil {
				walkMarks(reflect.ValueOf(any1), path+"(decoded)", hits, depth+1)
			}
			return
		}
		for i := 0; i < v.Len(); i++ {
			walkMarks(v.Index(i), fmt.Sprintf("%s[%d]", path, i), hits, depth+1)
		}
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walkMarks(v.Index(i), fmt.Sprintf("%s[%d]", path, i), hits, depth+1)
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			if k.Kind() == reflect.String && i18n.HasMark(k.String()) {
				*hits = append(*hits, fmt.Sprintf("%s{key}=%q", path, k.String()))
			}
			walkMarks(v.MapIndex(k), fmt.Sprintf("%s[%v]", path, k), hits, depth+1)
		}
	}
}
