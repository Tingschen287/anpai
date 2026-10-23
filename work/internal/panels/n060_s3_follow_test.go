package panels

import (
	"net"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/testsock"
)

// 段 3：面板进程（deck panel 通知、活动、决策、进度、额度）每 2 秒向 deckd 取一次数；响应头带的语言一变，
// 这一次取数画出来的就是新语言，不用重启。已有的通知（带码）整条跟着换。

func TestN060S3PanelsFollowDeckdLanguageOnTheNextFetch(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	sock := testsock.Path(t, "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var lang atomic.Value
	lang.Store("zh")
	notes := `[{"seq":1,"at":"2026-10-20T10:00:00Z","kind":"spawn","text":"「worker」的第一条 brief 已送出，但上游没证实提交，请在中间窗格看一眼","code":"err.deckd.serverrunspawn.26","args":["worker"]},` +
		`{"seq":2,"at":"2026-10-20T10:01:00Z","kind":"view","text":"会话不在打开集合里","code":"err.viewstate.notopen"}]`
	sessions := `[{"key":"k1","project":"demo","role":"main","title":"主 agent · demo","title_code":"err.deckd.serverpostmain.4","title_args":["demo"],"agent":"claude","status":"running"}]`
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(i18n.LangHeader, lang.Load().(string))
		switch r.URL.Path {
		case "/v1/notifications":
			w.Write([]byte(notes))
		case "/v1/sessions":
			w.Write([]byte(sessions))
		default:
			http.NotFound(w, r)
		}
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })

	i18n.SetLang(i18n.ZH)
	c := deckclient.NewUI(sock).FollowLang()
	fetch := func(name string) string {
		t.Helper()
		m := &model{name: name, c: c, width: 80, height: 20}
		msg, ok := m.fetch()().(dataMsg)
		if !ok || msg.err != nil {
			t.Fatalf("%s 取数：%+v", name, msg)
		}
		return msg.view
	}
	for _, name := range []string{"notifications", "activity"} {
		zh1 := fetch(name)
		lang.Store("en")
		en := fetch(name) // 同一个进程、同一个客户端：这一次响应头是英文，画出来的就是英文
		if i18n.Lang() != i18n.EN {
			t.Fatalf("%s：取数之后本进程应是英文", name)
		}
		if en == zh1 {
			t.Fatalf("%s：英文应与中文不同：%q", name, en)
		}
		if r, bad := firstHan(en); bad {
			t.Errorf("%s：英文画面里有汉字 %q：%q", name, r, en)
		}
		lang.Store("zh")
		if zh2 := fetch(name); zh2 != zh1 {
			t.Fatalf("%s：切回中文后与切换前不一致：\n%q\n%q", name, zh1, zh2)
		}
	}
}

func firstHan(s string) (rune, bool) {
	for _, r := range s {
		if (r >= 0x4e00 && r <= 0x9fff) || (r >= 0x3000 && r <= 0x303f) || (r >= 0xff00 && r <= 0xffef) {
			return r, true
		}
	}
	return 0, false
}
