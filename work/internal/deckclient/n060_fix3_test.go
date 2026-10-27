package deckclient

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// NewUI 的请求带 i18n.MarkHeader，New 的不带；WithTimeout 的副本保留这个性质。
func TestN060NewUISendsTheMarkHeader(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("不能监听 unix socket：%v", err)
	}
	var mu sync.Mutex
	var got []string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Header.Get(i18n.MarkHeader))
		mu.Unlock()
		w.Write([]byte(`{}`))
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	var out map[string]any
	for _, c := range []*Client{New(sock), NewUI(sock), NewUI(sock).WithTimeout(5e9), New(sock).WithTimeout(5e9)} {
		if err := c.Get(context.Background(), "/v1/x", &out); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"", i18n.MarkNeutral, i18n.MarkNeutral, ""}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 个客户端的请求头：%q，想要 %q", i, got[i], want[i])
		}
	}
}

// Show、Text 的返回值里中性标记换成当前语言的记号：中文「[已打码]」，英文「[redacted]」。
// 兜底文字、嵌套码的兜底、无码的 HTTP 错误都一样；字面的 [已打码] 不动。
func TestN060ShowAndTextTranslateTheNeutralMark(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	mark := i18n.RedactMark
	for _, c := range []struct{ lang, want string }{{i18n.ZH, "[已打码]"}, {i18n.EN, "[redacted]"}} {
		i18n.SetLang(c.lang)
		// 无码的 HTTP 错误：英文也翻前缀
		got := Show(&Error{Status: 502, Message: "upstream " + mark + " literal [已打码]"})
		wantPrefix := "deckd 502：upstream "
		if c.lang == i18n.EN {
			wantPrefix = "deckd 502: upstream "
		}
		if got != wantPrefix+c.want+" literal [已打码]" {
			t.Fatalf("%s 无码 HTTP 错误：%q", c.lang, got)
		}
		// 码认得但参数个数对不上：回到兜底文字，兜底里的标记也要换
		if got := Text("err.deckd.serverrunspawn.47", nil, "fallback "+mark); got != "fallback "+c.want {
			t.Fatalf("%s 兜底：%q", c.lang, got)
		}
		// 不认识的码
		if got := Text("err.nope", nil, "fb "+mark); got != "fb "+c.want {
			t.Fatalf("%s 不认识的码：%q", c.lang, got)
		}
		if c.lang != i18n.EN {
			continue // 中文界面显示兜底（Error() 的中文原文）；下面是英文按码翻译的路径
		}
		// 参数里的字符串带标记
		if got := Text("err.deckd.serverrunspawn.47", []any{"a " + mark}, "fb"); !strings.Contains(got, "a "+c.want) || strings.ContainsAny(got, mark) {
			t.Fatalf("%s 参数：%q", c.lang, got)
		}
		// 嵌套码找不到译文时用它的兜底，兜底里的标记同样要换
		n := i18n.Nest{Code: "err.nope", Error: "nest " + mark}
		if got := Text("err.deckd.serverrunspawn.47", []any{n}, "fb"); !strings.Contains(got, "nest "+c.want) || strings.ContainsAny(got, mark) {
			t.Fatalf("%s 嵌套兜底：%q", c.lang, got)
		}
	}
}
