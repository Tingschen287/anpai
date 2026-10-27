package agentdeck

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/wirecheck"
)

// 段 3 必改 1（第 2 段复审 4）：转义写法的伪造标记。
// JSON 里的 "\ue000…" 在字节里没有私用区字符，只在解码之后才变成标记字符；只在解码前处理字节挡不住。
// 下面逐个「已中和」入口喂带转义的输入，断言解码之后的值不含标记字符（wirecheck.MarksIn 走遍导出字段、map、RawMessage）。

const escaped = `\ue000\ue001\ue002\ue003\ue004`

type auditR5Transport func(*http.Request) (*http.Response, error)

func (f auditR5Transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Codex 复审 4 的反例，名字沿用。
func TestAuditN060R5EscapedUpstreamJSON(t *testing.T) {
	c, _ := NewClient("unused", "review", "review.invalid", "")
	c.httpClient = &http.Client{Transport: auditR5Transport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"error":"external ` + escaped + ` text"}`)), Header: make(http.Header)}, nil
	})}
	var out struct {
		Error string `json:"error"`
	}
	if err := c.get(context.Background(), "/review", &out); err != nil {
		t.Fatal(err)
	}
	if i18n.HasMark(out.Error) {
		t.Errorf("转义写法的伪造标记绕过了入口中和：%q", out.Error)
	}
	if !strings.Contains(out.Error, "�") {
		t.Errorf("伪造的标记应被换成 U+FFFD：%q", out.Error)
	}
}

// 嵌套结构、map 的键和值、接口里的值、json.RawMessage：get 解出来的整棵值里都不能有标记。
func TestN060S3EscapedHTTPNestedValues(t *testing.T) {
	c, _ := NewClient("unused", "review", "review.invalid", "")
	body := `{"a":{"b":["x ` + escaped + `",{"c":"y ` + escaped + `"}]},"k` + escaped + `":"v","raw":{"inner":"z ` + escaped + `","\ue000key":1}}`
	c.httpClient = &http.Client{Transport: auditR5Transport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	var viaMap map[string]any
	if err := c.get(context.Background(), "/x", &viaMap); err != nil {
		t.Fatal(err)
	}
	if hits := wirecheck.MarksIn(viaMap); len(hits) > 0 {
		t.Errorf("map[string]any 里还有标记：%v", hits)
	}
	var viaStruct struct {
		A   map[string]any  `json:"a"`
		Raw json.RawMessage `json:"raw"`
	}
	if err := c.get(context.Background(), "/x", &viaStruct); err != nil {
		t.Fatal(err)
	}
	if hits := wirecheck.MarksIn(viaStruct); len(hits) > 0 {
		t.Errorf("结构体（含 RawMessage）里还有标记：%v", hits)
	}
	// RawMessage 之后会被再解码一次：再解码出来也不能有标记
	var again map[string]any
	if err := json.Unmarshal(viaStruct.Raw, &again); err != nil {
		t.Fatal(err)
	}
	if hits := wirecheck.MarksIn(again); len(hits) > 0 {
		t.Errorf("RawMessage 再解码出来的值里还有标记：%v", hits)
	}
}

func fakeBin(t *testing.T, out string) *Client {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fake-agent-deck")
	script := "#!/bin/sh\ncat <<'JSON'\n" + out + "\nJSON\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(bin, "deck", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// 上游子命令的 stdout 是 JSON：list、launch、inbox export、inbox writer-status、send 回执，各自解码之后的值都不能有标记。
func TestN060S3EscapedExecJSON(t *testing.T) {
	ctx := context.Background()
	t.Run("list", func(t *testing.T) {
		c := fakeBin(t, `[{"id":"s1","title":"t `+escaped+`","tool":"claude","group":"g`+escaped+`","path":"/p"}]`)
		got, err := c.ListSessions(ctx)
		if err != nil || len(got) != 1 {
			t.Fatalf("%v %v", got, err)
		}
		if hits := wirecheck.MarksIn(got); len(hits) > 0 {
			t.Errorf("list 的值里还有标记：%v", hits)
		}
	})
	t.Run("launch", func(t *testing.T) {
		c := fakeBin(t, `{"success":false,"error":"bad `+escaped+` news","code":"E`+escaped+`","title":"t`+escaped+`"}`)
		_, err := c.Launch(ctx, LaunchRequest{ProjectPath: "/tmp/p", Tool: "claude", Model: "claude-opus-5-5", NoWait: true})
		if err == nil {
			t.Fatal("应该失败")
		}
		for _, s := range wireStrings(err) {
			if i18n.HasMark(s) {
				t.Errorf("launch 的错误参数里还有标记：%q", s)
			}
		}
		if i18n.HasMark(err.Error()) {
			t.Errorf("launch 的 Error() 里还有标记：%q", err.Error())
		}
	})
	t.Run("inbox-export", func(t *testing.T) {
		c := fakeBin(t, `[{"child_session_id":"c1","child_title":"t `+escaped+`","done_summary":"s `+escaped+`","dead_letter_reason":"r`+escaped+`"}]`)
		got, err := c.InboxExport(ctx)
		if err != nil || len(got) != 1 {
			t.Fatalf("%v %v", got, err)
		}
		if hits := wirecheck.MarksIn(got); len(hits) > 0 {
			t.Errorf("inbox export 的值里还有标记：%v", hits)
		}
	})
	t.Run("inbox-writer-status", func(t *testing.T) {
		c := fakeBin(t, `{"running":true,"detail":"d `+escaped+`"}`)
		got, err := c.InboxWriterStatus(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if hits := wirecheck.MarksIn(got); len(hits) > 0 {
			t.Errorf("writer-status 的值里还有标记：%v", hits)
		}
	})
	t.Run("send-receipt", func(t *testing.T) {
		err := sendResult("s1", []byte(`{"delivery":"unv`+escaped+`","submitted":false,"error":"e `+escaped+`"}`), nil, false)
		if err == nil {
			t.Fatal("应该是未确认")
		}
		for _, s := range wireStrings(err) {
			if i18n.HasMark(s) {
				t.Errorf("send 回执的错误参数里还有标记：%q", s)
			}
		}
		if i18n.HasMark(err.Error()) {
			t.Errorf("send 回执的 Error() 里还有标记：%q", err.Error())
		}
	})
}

// 上游事件流（SSE）的每一行：data 里的 JSON 转义也要中和。
func TestN060S3EscapedEventStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: menu\ndata: {\"profile\":\"p"+escaped+"\",\"items\":[{\"index\":1,\"type\":\"session\",\"path\":\"/x"+escaped+"\"}]}\n\n")
	}))
	defer srv.Close()
	c, _ := NewClient("unused", "deck", strings.TrimPrefix(srv.URL, "http://"), "")
	var got []MenuEvent
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = c.WatchMenu(ctx, func(ev MenuEvent) { got = append(got, ev); cancel() })
	if len(got) == 0 {
		t.Fatal("没收到事件")
	}
	if hits := wirecheck.MarksIn(got); len(hits) > 0 {
		t.Errorf("事件里还有标记：%v", hits)
	}
}
