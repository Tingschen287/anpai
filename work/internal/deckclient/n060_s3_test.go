package deckclient

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/testsock"
)

// 段 3：界面进程用 FollowLang 的客户端，deckd 每个响应带的 X-Deck-Lang 就是本进程的界面语言。
// 不跟的客户端（deck agent 命令、模型用的）永远不被它改动。

type fakeLangServer struct {
	sock   string
	lang   atomic.Value // 响应头里放什么
	status atomic.Int32
}

func newFakeLangServer(t *testing.T) *fakeLangServer {
	t.Helper()
	f := &fakeLangServer{sock: testsock.Path(t, "d.sock")}
	f.lang.Store("zh")
	f.status.Store(200)
	l, err := net.Listen("unix", f.sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := f.lang.Load().(string); v != "-" {
			w.Header().Set(i18n.LangHeader, v)
		}
		w.WriteHeader(int(f.status.Load()))
		w.Write([]byte(`{"lang":"x","error":"boom"}`))
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return f
}

func TestN060S3FollowLangSwitchesProcessLanguage(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	f := newFakeLangServer(t)
	ctx := context.Background()
	var out map[string]any

	// 不跟的客户端：不动语言，即使 deckd 说是英文
	i18n.SetLang(i18n.ZH)
	f.lang.Store("en")
	for name, c := range map[string]*Client{"New": New(f.sock), "NewUI": NewUI(f.sock)} {
		if err := c.Get(ctx, "/v1/x", &out); err != nil {
			t.Fatal(err)
		}
		if i18n.Lang() != i18n.ZH {
			t.Fatalf("%s 不跟 deckd 的语言，却被改成了 %s", name, i18n.Lang())
		}
	}
	// 跟的客户端：响应头是什么就是什么
	c := NewUI(f.sock).FollowLang()
	if err := c.Get(ctx, "/v1/x", &out); err != nil {
		t.Fatal(err)
	}
	if i18n.Lang() != i18n.EN {
		t.Fatalf("应跟到英文：%s", i18n.Lang())
	}
	f.lang.Store("zh")
	if err := c.Get(ctx, "/v1/x", &out); err != nil {
		t.Fatal(err)
	}
	if i18n.Lang() != i18n.ZH {
		t.Fatalf("应跟回中文：%s", i18n.Lang())
	}
	// 不认识的值、没有这个头：忽略，语言不变
	f.lang.Store("fr")
	c.Get(ctx, "/v1/x", &out)
	f.lang.Store("-")
	c.Get(ctx, "/v1/x", &out)
	if i18n.Lang() != i18n.ZH {
		t.Fatalf("不认识的头或没有头应忽略：%s", i18n.Lang())
	}
	// 出错的响应也带语言：照样跟
	f.lang.Store("en")
	f.status.Store(500)
	if err := c.Get(ctx, "/v1/x", &out); err == nil {
		t.Fatal("500 应是错误")
	}
	if i18n.Lang() != i18n.EN {
		t.Fatalf("出错的响应也该跟：%s", i18n.Lang())
	}
	// 换超时的副本仍然跟
	f.status.Store(200)
	f.lang.Store("zh")
	if err := c.WithTimeout(time.Second).Get(ctx, "/v1/x", &out); err != nil {
		t.Fatal(err)
	}
	if i18n.Lang() != i18n.ZH {
		t.Fatalf("WithTimeout 的副本应保留跟随：%s", i18n.Lang())
	}
}

func TestN060S3SyncLang(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	f := newFakeLangServer(t)
	f.lang.Store("en")
	i18n.SetLang(i18n.ZH)
	got, err := NewUI(f.sock).SyncLang(context.Background())
	if err != nil || got != i18n.EN || i18n.Lang() != i18n.EN {
		t.Fatalf("SyncLang：%q %v lang=%s", got, err, i18n.Lang())
	}
	// 连不上：返回错误，语言不变
	got, err = NewUI(testsock.Path(t, "none.sock")).SyncLang(context.Background())
	if err == nil || got != i18n.EN || i18n.Lang() != i18n.EN {
		t.Fatalf("连不上应报错且语言不变：%q %v lang=%s", got, err, i18n.Lang())
	}
}
