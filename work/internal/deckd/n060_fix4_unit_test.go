package deckd

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 返工 4 第 1 条的单元测试：直接测入口里新加的两个函数（对旧代码编不过，所以单放一个文件）。

func TestN060R4NeutralRequestLeavesCleanRequestsAlone(t *testing.T) {
	r, _ := http.NewRequest("GET", "/v1/progress?project=alpha", nil)
	if neutralRequest(r) != r {
		t.Fatal("没有伪造标记的请求不该被复制")
	}
	r2, _ := http.NewRequest("GET", "/v1/progress?project=a"+url.QueryEscape(i18n.RedactMark), nil)
	got := neutralRequest(r2)
	if got == r2 || i18n.HasMark(got.URL.Query().Get("project")) || !strings.HasPrefix(got.URL.Query().Get("project"), "a") {
		t.Fatalf("查询串没有被中和：%q", got.URL.RawQuery)
	}
}

// 请求体里嵌套的值（map、切片、指针、RawMessage、键）都要走到。
func TestN060R4NeutralizeValueWalksNestedValues(t *testing.T) {
	m := i18n.RedactMark
	type inner struct {
		S   string
		Ptr *string
	}
	s := "p" + m
	v := struct {
		A   string
		L   []string
		M   map[string]any
		I   inner
		Raw json.RawMessage
		Any any
	}{
		A: "a" + m, L: []string{"l" + m}, M: map[string]any{"k" + m: []any{"v" + m, map[string]any{"z": "z" + m}}},
		I: inner{S: "i" + m, Ptr: &s}, Raw: json.RawMessage(`{"r":"` + m + `"}`), Any: "any" + m,
	}
	neutralizeValue(&v)
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "") || strings.Contains(string(b), ``) {
		t.Fatalf("还有标记：%s", b)
	}
	if v.A != "a�����" {
		t.Fatalf("A=%q", v.A)
	}
}
