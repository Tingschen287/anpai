package main

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckd"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/layout"
)

// 段 3：界面语言在各进程里的接线。

func envMap(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// deck up 起 deckd 时把自己（owner 的 shell）算出的系统语言传进去；deckd 的后台窗口环境可能不同。
func TestN060S3DeckdIsStartedWithTheSystemLangOfDeckUp(t *testing.T) {
	spec := layout.Spec{Deck: "/opt/deck", LayoutSocket: "/tmp/l.sock", AgentSocket: "/tmp/a.sock"}
	for _, c := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"LANG": "zh_CN.UTF-8"}, "zh"},
		{map[string]string{"LANG": "en_US.UTF-8"}, "en"},
		{map[string]string{"LC_ALL": "C.UTF-8", "LANG": "zh_CN.UTF-8"}, "en"},
		{map[string]string{}, "en"},
	} {
		cmd := deckdCommand(spec, "", envMap(c.env))
		if !strings.Contains(cmd, " -system-lang "+c.want) || !strings.Contains(cmd, "daemon") {
			t.Errorf("环境 %v：命令 %q 应带 -system-lang %s", c.env, cmd, c.want)
		}
	}
	if cmd := deckdCommand(spec, "127.0.0.1:8420", envMap(nil)); !strings.Contains(cmd, "-upstream") {
		t.Errorf("上游地址要保留：%q", cmd)
	}
}

// deckd 用的系统语言：deck up 传来的优先；手动 deck daemon 没传，按 deckd 自己的环境变量。
func TestN060S3DaemonSystemLangPrecedence(t *testing.T) {
	zhEnv, enEnv := envMap(map[string]string{"LANG": "zh_CN.UTF-8"}), envMap(map[string]string{"LANG": "en_US.UTF-8"})
	cases := []struct {
		flag string
		env  func(string) string
		want string
	}{
		{"en", zhEnv, "en"}, {"zh", enEnv, "zh"}, {"", zhEnv, "zh"}, {"", enEnv, "en"}, {"fr", zhEnv, "zh"}, {"", envMap(nil), "en"},
	}
	for _, c := range cases {
		if got := daemonSystemLang(c.flag, c.env); got != c.want {
			t.Errorf("flag=%q → %q，应为 %q", c.flag, got, c.want)
		}
	}
}

func fakeDeckd(t *testing.T, dirs deckstore.Dirs, lang string) *atomic.Value {
	t.Helper()
	if err := os.MkdirAll(dirs.State, 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", deckd.SocketPath(dirs))
	if err != nil {
		t.Fatal(err)
	}
	var cur atomic.Value
	cur.Store(lang)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(i18n.LangHeader, cur.Load().(string))
		w.Write([]byte(`{"lang":"` + cur.Load().(string) + `"}`))
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return &cur
}

func shortDirs(t *testing.T) deckstore.Dirs {
	t.Helper()
	root, err := os.MkdirTemp("", "dk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	return deckstore.Dirs{Config: filepath.Join(root, "c"), State: filepath.Join(root, "s")}
}

// 界面进程（sidebar、tabs、panel、settings、center、idle）启动时：先按自己的环境变量猜，再向 deckd 对一次；以 deckd 为准。
func TestN060S3UIClientTakesTheLanguageFromDeckdAtStartup(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	t.Setenv("LC_ALL", "zh_CN.UTF-8")
	// 没有 deckd：按环境变量猜
	dirs := shortDirs(t)
	uiClient(dirs)
	if i18n.Lang() != i18n.ZH {
		t.Fatalf("没有 deckd，环境是中文：%s", i18n.Lang())
	}
	t.Setenv("LC_ALL", "en_US.UTF-8")
	uiClient(dirs)
	if i18n.Lang() != i18n.EN {
		t.Fatalf("没有 deckd，环境是英文：%s", i18n.Lang())
	}
	// 有 deckd：以 deckd 为准，不管自己的环境
	cur := fakeDeckd(t, dirs, "zh")
	c := uiClient(dirs)
	if i18n.Lang() != i18n.ZH {
		t.Fatalf("deckd 说中文，环境是英文：%s", i18n.Lang())
	}
	// 之后 deckd 改了，客户端下一次请求就跟着换（不重启）
	cur.Store("en")
	var out map[string]any
	if err := c.Get(t.Context(), "/v1/health", &out); err != nil {
		t.Fatal(err)
	}
	if i18n.Lang() != i18n.EN {
		t.Fatalf("下一次请求应跟到英文：%s", i18n.Lang())
	}
	// 空闲页、停住的提示靠 syncLang 对语言：变了返回 true
	cur.Store("zh")
	if !syncLang(c) || i18n.Lang() != i18n.ZH {
		t.Fatalf("syncLang 应报告语言变了：%s", i18n.Lang())
	}
	if syncLang(c) {
		t.Fatal("没变就不该报告变了")
	}
	// deck agent 命令那类客户端不跟
	i18n.SetLang(i18n.ZH)
	cur.Store("en")
	if err := deckclient.New(deckd.SocketPath(dirs)).Get(t.Context(), "/v1/health", &out); err != nil {
		t.Fatal(err)
	}
	if i18n.Lang() != i18n.ZH {
		t.Fatal("命令行客户端永远是中文，不跟界面语言")
	}
}

// 停住的整屏文字按此刻的界面语言拼；原因是函数时，语言变了重新拼一次就是新语言（layout.CenterLoop.PauseLive）。
func TestN060S3PauseScreenFollowsTheLanguage(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	i18n.SetLang(i18n.ZH)
	zh := pauseScreen("原因", "/tmp/agent.sock")
	i18n.SetLang(i18n.EN)
	en := pauseScreen("reason", "/tmp/agent.sock")
	if zh == en || !strings.Contains(zh, "回车重试") || !strings.Contains(en, "press Enter") {
		t.Fatalf("两种语言的停住画面：\n%q\n%q", zh, en)
	}
	for _, r := range en {
		if (r >= 0x4e00 && r <= 0x9fff) || (r >= 0x3000 && r <= 0x303f) || (r >= 0xff00 && r <= 0xffef) {
			t.Fatalf("英文停住画面里有汉字 %q：%q", r, en)
		}
	}
	i18n.SetLang(i18n.ZH)
	if again := pauseScreen("原因", "/tmp/agent.sock"); again != zh {
		t.Fatalf("切回中文后与切换前不一致：\n%q\n%q", zh, again)
	}
}
