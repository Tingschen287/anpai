package i18n

import (
	"bytes"
	"strings"
	"testing"
)

// 返工 4：字节版的中和。长度不变（U+E000..E004 与 U+FFFD 都是 3 字节），后面的 JSON 解析不受影响。
func TestN060NeutralizeBytes(t *testing.T) {
	in := []byte(`{"a":"x` + RedactMark + `y","b":"[已打码] 字面"}`)
	out := NeutralizeBytes(in)
	if len(out) != len(in) {
		t.Fatalf("长度变了：%d -> %d", len(in), len(out))
	}
	if HasMark(string(out)) || strings.ContainsRune(string(out), '') {
		t.Fatalf("还有标记：%q", out)
	}
	if !strings.Contains(string(out), "[已打码] 字面") {
		t.Fatalf("字面的中文记号被改了：%q", out)
	}
	clean := []byte(`{"a":"没有标记"}`)
	if got := NeutralizeBytes(clean); !bytes.Equal(got, clean) {
		t.Fatalf("没有标记的字节不该变：%q", got)
	}
	// 别的私用区字符不动
	other := []byte("ab")
	if got := NeutralizeBytes(other); !bytes.Equal(got, other) {
		t.Fatalf("别的私用区字符被改了：%q", got)
	}
}
