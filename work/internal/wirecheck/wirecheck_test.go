package wirecheck

import (
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

func hits(kind, doc string) []Violation {
	return Capture(func() { Doc(kind, []byte(doc)) })
}

// 兜底中文：带码的对象，error / text / note / summary / title 不查；没有码就查。
func TestFallbackTextIsExemptOnlyWithACode(t *testing.T) {
	cases := []struct {
		name, doc string
		want      int
	}{
		{"有码的 error", `{"error":"中文兜底","code":"err.x","args":[]}`, 0},
		{"无码的 error", `{"error":"本地写的中文"}`, 1},
		{"有码的通知", `{"text":"中文兜底","code":"err.x","args":["a"]}`, 0},
		{"无码的通知", `{"text":"本地写的中文"}`, 1},
		{"note 看 note_code", `{"note":"中文","note_code":"err.x"}`, 0},
		{"note 有 code 但没有 note_code", `{"note":"中文","code":"err.x"}`, 1},
		{"标题看 title_code", `{"title":"主 agent · a","title_code":"err.x","title_args":["a"]}`, 0},
		{"无码的标题", `{"title":"主 agent · a"}`, 1},
		{"数组里的对象", `[{"summary":"中文","summary_code":"err.x"},{"summary":"中文"}]`, 1},
		{"没有中文", `{"error":"plain","title":"t"}`, 0},
		{"中文标点也算", `{"error":"a：b"}`, 1},
	}
	for _, c := range cases {
		if got := hits("GET /v1/x OK", c.doc); len(got) != c.want {
			t.Errorf("%s：命中 %d，想要 %d：%+v", c.name, len(got), c.want, got)
		}
	}
}

// 参数：嵌套码看它的 args 不看 error；列表看 items；字符串含中文就是命中，除非登记为外部原文。
func TestArgsRules(t *testing.T) {
	if got := hits("GET /v1/x OK", `{"error":"x","code":"err.x","args":["本地写的中文"]}`); len(got) != 1 || got[0].Path != "$.args[]" {
		t.Fatalf("参数里的中文字符串：%+v", got)
	}
	nested := `{"error":"x","code":"err.x","args":[{"code":"err.y","error":"内层中文兜底","args":["ok",3]}]}`
	if got := hits("GET /v1/x OK", nested); len(got) != 0 {
		t.Fatalf("嵌套码的 error 是兜底：%+v", got)
	}
	nestedBad := `{"error":"x","code":"err.x","args":[{"code":"err.y","error":"内层中文兜底","args":["内层的本地中文"]}]}`
	if got := hits("GET /v1/x OK", nestedBad); len(got) != 1 || got[0].Path != "$.args[].args[]" {
		t.Fatalf("嵌套码的参数要递归看：%+v", got)
	}
	list := `{"error":"x","code":"err.x","args":[{"sep":"sep.nl","items":["a",{"code":"err.y","error":"兜底"}]}]}`
	if got := hits("GET /v1/x OK", list); len(got) != 0 {
		t.Fatalf("列表里英文和嵌套码都放行：%+v", got)
	}
	External("上游自己的话 ABC")
	if got := hits("GET /v1/x OK", `{"error":"x","code":"err.x","args":["说：上游自己的话 ABC"]}`); len(got) != 0 {
		t.Fatalf("登记过的外部原文（参数里含有它）放行：%+v", got)
	}
	if got := hits("GET /v1/x OK", `{"error":"上游自己的话 ABC"}`); len(got) != 0 {
		t.Fatalf("文本字段恰好等于登记的外部原文放行：%+v", got)
	}
	if got := hits("GET /v1/x OK", `{"error":"说：上游自己的话 ABC"}`); len(got) != 1 {
		t.Fatalf("文本字段只认整句相等，多了本地的话就命中：%+v", got)
	}
}

// 例外按「方法 路径 · 字段路径」匹配，用到就记一次；方法和路径都要对上。
func TestExceptionsMatchMethodPathAndField(t *testing.T) {
	old := Exceptions
	t.Cleanup(func() { Exceptions = old })
	Exceptions = []Exception{{Pkg: "x", Kind: "GET /v1/reports/wait", Path: "$.reports[].text", Func: "f", Why: "w"},
		{Pkg: "x", Kind: "*", Path: "$.session.note", Func: "g", Why: "w"},
		{Pkg: "x", Kind: "POST /v1/decisions/*/answer", Path: "$.result", Func: "h", Why: "w"}}
	cases := []struct {
		kind, doc string
		want      int
	}{
		{"GET /v1/reports/wait OK", `{"reports":[{"text":"中文"}]}`, 0},
		{"POST /v1/reports/wait OK", `{"reports":[{"text":"中文"}]}`, 1},
		{"GET /v1/reports OK", `{"reports":[{"text":"中文"}]}`, 1},
		{"GET /v1/reports/wait OK", `{"reports":[{"other":"中文"}]}`, 1},
		{"GET /v1/sessions OK", `{"session":{"note":"中文"}}`, 0},
		{"POST /v1/decisions/dec-1/answer OK", `{"result":"中文"}`, 0},
		{"POST /v1/decisions/dec-1/cancel OK", `{"result":"中文"}`, 1},
	}
	for _, c := range cases {
		if got := hits(c.kind, c.doc); len(got) != c.want {
			t.Errorf("%s %s：命中 %d，想要 %d：%+v", c.kind, c.doc, len(got), c.want, got)
		}
	}
	if Snapshot().AllowUsed["GET /v1/reports/wait · $.reports[].text · f"] == 0 {
		t.Fatal("例外被用到应记一次")
	}
}

// SSE 事件：取每个 data: 行里的 JSON 来查。
func TestServerSentEventsAreParsed(t *testing.T) {
	ev := "id: abc:3\nevent: notification\ndata: {\"text\":\"本地写的中文\"}\n\n"
	if got := hits("GET /v1/events OK", ev); len(got) != 1 || got[0].Path != "$.text" {
		t.Fatalf("SSE 事件里的中文：%+v", got)
	}
	if got := hits("GET /v1/events OK", ": keepalive\n\n"); len(got) != 0 {
		t.Fatalf("keepalive 不查：%+v", got)
	}
	if got := hits("GET /v1/events OK", "id: x\nevent: resync\ndata: {}\n\n"); len(got) != 0 {
		t.Fatalf("resync 不查：%+v", got)
	}
}

// 请求体里客户端自己写的中文是外部原文，登记后响应里原样回显放行。
func TestRequestBodyBecomesExternal(t *testing.T) {
	requestBody([]byte(`{"title":"客户端写的标题 Q1","options":["选项甲乙"],"n":3}`))
	if got := hits("POST /v1/x OK", `{"session":{"title":"客户端写的标题 Q1"},"options":["选项甲乙"]}`); len(got) != 0 {
		t.Fatalf("请求体里的文字回显放行：%+v", got)
	}
}

// 构造带码错误的参数：本地中文字符串、无码的中文错误命中；英文、嵌套码、Join 的英文条目放行。
func TestArgsOfCodedErrors(t *testing.T) {
	if got := Capture(func() { Args("err.x", []any{"本地写的中文"}) }); len(got) != 1 {
		t.Fatalf("字符串：%+v", got)
	}
	if got := Capture(func() { Args("err.x", []any{"plain", 3, i18n.E("err.deckd.serverevents.1")}) }); len(got) != 0 {
		t.Fatalf("英文和嵌套码放行：%+v", got)
	}
	if got := Capture(func() { Args("err.x", []any{strings.Repeat("ok", 3)}) }); len(got) != 0 {
		t.Fatalf("纯英文放行：%+v", got)
	}
}

func TestHasHanMatchesTheStageOneDefinition(t *testing.T) {
	for _, s := range []string{"中", "，", "（", "：", "ＡＢ"} {
		if !HasHan(s) {
			t.Errorf("%q 应算中文", s)
		}
	}
	for _, s := range []string{"abc", "…", "·", "—", "é", "[redacted]"} {
		if HasHan(s) {
			t.Errorf("%q 不该算中文", s)
		}
	}
}
