package i18n

import (
	"encoding/json"
	"strings"
	"testing"
)

// 段 3 必改 1：解码之后的中和。转义写法用 "\\" + "ue000" 拼，避免源码里直接出现 \uXXXX。

var bs = string(rune(92)) // 反斜杠

func esc(hex string) string { return bs + "u" + hex }

func escMark() string { return esc("e000") + esc("e001") + esc("e002") + esc("e003") + esc("e004") }

func TestN060NeutralizeJSONHandlesEscapesBySyntax(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"转义写法":     {`{"a":"x` + escMark() + `y"}`, `{"a":"x` + strings.Repeat(esc("FFFD"), 5) + `y"}`},
		"大写十六进制":   {`"` + bs + "uE003" + `"`, `"` + esc("FFFD") + `"`},
		"字面字节":     {"\"" + RedactMark + "\"", "\"�����\""},
		"转义的反斜杠不动": {`"` + bs + bs + "ue000" + `"`, `"` + bs + bs + "ue000" + `"`},
		"三个反斜杠":    {`"` + bs + bs + bs + "ue000" + `"`, `"` + bs + bs + esc("FFFD") + `"`},
		"其他私用区不动":  {`"` + esc("e005") + esc("e0ff") + `"`, `"` + esc("e005") + esc("e0ff") + `"`},
		"普通转义不动":   {`"` + bs + "n" + bs + `"` + esc("0041") + `"`, `"` + bs + "n" + bs + `"` + esc("0041") + `"`},
		"没有反斜杠":    {`{"a":1}`, `{"a":1}`},
		"结尾的反斜杠":   {`abc` + bs, `abc` + bs},
		"不完整的转义":   {`"` + bs + "ue00", `"` + bs + "ue00"},
	}
	for name, c := range cases {
		if got := string(NeutralizeJSON([]byte(c.in))); got != c.want {
			t.Errorf("%s：\n  in   %q\n  got  %q\n  want %q", name, c.in, got, c.want)
		}
	}
}

func TestN060UnmarshalExternalNeutralizesDecodedStrings(t *testing.T) {
	doc := `{"s":"a` + escMark() + `b","list":["` + esc("e001") + `",{"k":"` + esc("e002") + `"}],"m":{"key` + esc("e003") + `":"v"},"raw":{"in":"` + esc("e004") + `"},"n":3}`
	type T struct {
		S    string            `json:"s"`
		List []any             `json:"list"`
		M    map[string]string `json:"m"`
		Raw  json.RawMessage   `json:"raw"`
		P    *struct {
			X string `json:"x"`
		} `json:"p"`
		N int `json:"n"`
	}
	var v T
	if err := UnmarshalExternal([]byte(doc), &v); err != nil {
		t.Fatal(err)
	}
	if HasMark(v.S) || !strings.Contains(v.S, "�") {
		t.Errorf("S：%q", v.S)
	}
	var again map[string]any
	if err := json.Unmarshal(v.Raw, &again); err != nil || HasMark(again["in"].(string)) {
		t.Errorf("RawMessage 再解码仍有标记：%v %v", again, err)
	}
	b, _ := json.Marshal(v)
	if HasMark(string(b)) || strings.Contains(strings.ToLower(string(b)), bs+"ue00") {
		t.Errorf("整棵值里还有标记或转义：%s", b)
	}
	if v.N != 3 {
		t.Errorf("普通字段被改了：%+v", v)
	}
	// 解码错误照常返回
	if err := UnmarshalExternal([]byte(`{`), &v); err == nil {
		t.Error("坏 JSON 应报错")
	}
	// DecodeExternal 允许后面还有内容，与 json.Decoder 一样
	var s struct {
		A string `json:"a"`
	}
	if err := DecodeExternal([]byte(`{"a":"x`+esc("e000")+`"} trailing`), &s); err != nil || HasMark(s.A) {
		t.Errorf("DecodeExternal：%q %v", s.A, err)
	}
}

func TestN060NeutralizeValueWalksEveryShape(t *testing.T) {
	m := RedactMark
	type inner struct{ S string }
	type outer struct {
		Str   string
		Ptr   *string
		Any   any
		Arr   [2]string
		Slice []inner
		Map   map[string]any
		Keys  map[string]int
		hid   string
		Raw   json.RawMessage
	}
	ps := "p" + m
	v := outer{Str: "a" + m, Ptr: &ps, Any: "i" + m, Arr: [2]string{"x" + m, "y"}, Slice: []inner{{"s" + m}},
		Map: map[string]any{"k": []any{"d" + m, map[string]any{"n" + m: "v"}}}, Keys: map[string]int{"key" + m: 1},
		hid: "h" + m, Raw: json.RawMessage(`{"a":"` + esc("e000") + `"}`)}
	NeutralizeValue(&v)
	b, _ := json.Marshal(v)
	if HasMark(string(b)) || strings.Contains(string(b), bs+"ue000") {
		t.Errorf("走完之后还有标记：%s", b)
	}
	if !HasMark(v.hid) {
		t.Error("未导出的字段改不了，应原样留着（外部解码器也写不进去）")
	}
	// 不是指针、nil：什么也不做，不 panic
	NeutralizeValue(v)
	NeutralizeValue(nil)
	var np *outer
	NeutralizeValue(np)
}
