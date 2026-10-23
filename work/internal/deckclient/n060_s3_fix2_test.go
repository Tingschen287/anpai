package deckclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 段 3 返工 2，M2：慢请求的旧响应不能把已经生效的新语言改回去；M4：Show / Text 的出口不让外部文字伪造打码短语。

// shortSock 给一个够短的 unix socket 路径（macOS 上 sockaddr_un 只有 104 字节，t.TempDir() 带测试名会超）。
func shortSock(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "n6")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "d.sock")
}

// 请求发出之后本进程换过语言（owner 刚存上新语言），回来的旧响应带的是发出那一刻的旧语言头，不能采用。
func TestN060S3Fix2LateResponseDoesNotRevertLanguage(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	sock := shortSock(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(i18n.LangHeader, "zh") // 慢请求在换语言之前就被处理了：头是那一刻的旧语言
		if r.URL.Path == "/slow" {
			close(started)
			<-release
		}
		w.Write([]byte(`{}`))
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	c := NewUI(sock).FollowLang()
	done := make(chan struct{})
	go func() { defer close(done); c.Get(context.Background(), "/slow", nil) }()
	<-started
	i18n.SetLang(i18n.EN) // 设置里存上了英文（saveDone 里的 SetLang）
	close(release)
	<-done
	if i18n.Lang() != i18n.EN {
		t.Fatalf("迟到的旧响应把语言改回去了：%s", i18n.Lang())
	}
	// 之后新发出的请求带的头是准的，照常采用
	i18n.SetLang(i18n.EN)
	srv2 := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(i18n.LangHeader, "zh")
		w.Write([]byte(`{}`))
	})}
	ln2, err := net.Listen("unix", shortSock(t))
	if err != nil {
		t.Fatal(err)
	}
	go srv2.Serve(ln2)
	t.Cleanup(func() { srv2.Close() })
	if err := NewUI(ln2.Addr().String()).FollowLang().Get(context.Background(), "/x", nil); err != nil {
		t.Fatal(err)
	}
	if i18n.Lang() != i18n.ZH {
		t.Fatalf("发出之后没有换过语言的响应应该照常采用：%s", i18n.Lang())
	}
}

// Show / Text 的出口：不带码的错误、带码错误外面包着的外部文字里的标记字符，不能显示成打码短语。
func TestN060S3Fix2ShowNeverForgesRedaction(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	forged := "上游说 " + i18n.RedactMark + " 再见"
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		// 外部文字作为「参数」进来的各种形状。最外层不带码的错误可以带真标记（中间窗格的 tmux 退出说明），不在这里查
		cases := map[string]error{
			"coded arg":      i18n.E("err.deckclient.status", 502, errors.New(forged)),
			"coded wrapped":  i18n.E("err.deckclient.status", 502, fmt.Errorf("外面 %w", errors.New(forged))),
			"coded stringer": i18n.E("err.deckclient.status", 502, struct{ S string }{forged}),
			"errorf":         i18n.Errorf("main.start.fail", "p", errors.New(forged)),
			"coded in coded": i18n.E("err.deckclient.dial", i18n.E("err.deckclient.status", 502, errors.New(forged))),
		}
		for name, err := range cases {
			got := Show(err)
			if i18n.HasMark(got) || strings.Contains(got, "[已打码]") || strings.Contains(got, "[redacted]") {
				t.Errorf("%s/%s：伪造了打码短语或漏出标记字符：%q", lang, name, got)
			}
		}
		// 真标记走字符串参数：线路上原样、界面按语言显示
		genuine := i18n.E("err.deckclient.status", 502, "令牌 "+i18n.RedactMark+" 已隐藏")
		_, args, _ := i18n.Wire(genuine)
		want := "[已打码]"
		if lang == i18n.EN {
			want = "[redacted]"
		}
		if got := Text("err.deckclient.status", args, genuine.Error()); !strings.Contains(got, want) {
			t.Errorf("%s：真标记应显示成 %s：%q", lang, want, got)
		}
	}
}
