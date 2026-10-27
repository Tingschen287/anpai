package deckd

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// TestN060ErrorRoundTrip：deckd 用 writeErr 写出错误，界面经 JSON 读回来，中英各显示一次。
// 不连真的 deckd，只用本测试自己的 unix socket。
func TestN060ErrorRoundTrip(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })

	inner := i18n.E("err.deckstore.storeanswer.1", "d1")
	wrapped := i18n.E("err.deckd.tmuxcenterfind.3", inner, 7)
	external := i18n.E("err.deckd.serverrunspawn.47", "KEEP-RAW")
	version := &deckstore.ErrVersion{Want: 2, Have: 9}

	sock := filepath.Join(os.TempDir(), "n060rt.sock")
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(sock) })

	mux := http.NewServeMux()
	mux.HandleFunc("/args", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusBadRequest, inner)
	})
	mux.HandleFunc("/wrap", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusConflict, wrapped)
	})
	mux.HandleFunc("/raw", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusBadGateway, external)
	})
	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusConflict, version)
	})
	mux.HandleFunc("/unknown", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "原始报错", Code: "err.no.such"})
	})
	mux.HandleFunc("/badargs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "没有决定 d1", Code: "err.deckstore.storeanswer.1"})
	})
	mux.HandleFunc("/num", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusBadRequest, i18n.E("err.agentdeck.clientget.3", "up", 2))
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusBadRequest, i18n.E("err.agentdeck.clientget.3", "up", int64(9007199254740993)))
	})
	mux.HandleFunc("/oknum", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"note_code": "err.deckd.serverrunspawn.25",
			"note_args": []any{2, 3},
		})
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	c := deckclient.New(sock).WithTimeout(5 * time.Second)
	ctx := context.Background()
	var numErr error

	type want struct {
		path  string
		zh    string
		en    string
		zhAll string
		enAll string
		miss  bool
		hide  string
	}
	cases := []want{
		{path: "/args", zh: inner.Error(), en: "no decision d1",
			zhAll: "deckd 400：没有决定 d1", enAll: "deckd 400: no decision d1"},
		{path: "/wrap", zh: wrapped.Error(), en: "no decision d1",
			zhAll: "deckd 409：没有决定 d1（这次操作绑定的 client 7 已不在中间）",
			enAll: "deckd 409: no decision d1 (client 7 bound to this operation is no longer in the center)"},
		{path: "/raw", zh: external.Error(), en: "KEEP-RAW"},
		{path: "/version", zh: version.Error(), en: "version 2"},
		{path: "/unknown", zh: "原始报错", en: "原始报错", miss: true, hide: "err.no.such"},
		{path: "/badargs", zh: "没有决定 d1", en: "没有决定 d1", miss: true, hide: "err.deckstore.storeanswer.1"},
		{path: "/num", zh: "HTTP 2", en: "HTTP 2",
			zhAll: "deckd 400：upstream agent-deck up 返回 HTTP 2",
			enAll: "deckd 400: upstream agent-deck up returned HTTP 2"},
		{path: "/big", zh: "9007199254740993", en: "9007199254740993",
			zhAll: "deckd 400：upstream agent-deck up 返回 HTTP 9007199254740993",
			enAll: "deckd 400: upstream agent-deck up returned HTTP 9007199254740993"},
	}
	for _, tc := range cases {
		err := c.Do(ctx, http.MethodGet, tc.path, nil, nil)
		if err == nil {
			t.Fatalf("%s 应返回错误", tc.path)
		}
		i18n.SetLang(i18n.ZH)
		zh := deckclient.Show(err)
		if !strings.Contains(zh, tc.zh) {
			t.Errorf("%s 中文应含 %q，得到 %q", tc.path, tc.zh, zh)
		}
		if tc.zhAll != "" && zh != tc.zhAll {
			t.Errorf("%s 中文整句应为 %q，得到 %q", tc.path, tc.zhAll, zh)
		}
		i18n.SetLang(i18n.EN)
		stop := i18n.RecordMisses()
		en := deckclient.Show(err)
		miss := stop()
		if strings.Contains(zh, "%!d") || strings.Contains(en, "%!d") || strings.Contains(en, "float64") {
			t.Errorf("%s 把整数印坏了：中文 %q 英文 %q", tc.path, zh, en)
		}
		if !strings.Contains(en, tc.en) {
			t.Errorf("%s 英文应含 %q，得到 %q", tc.path, tc.en, en)
		}
		if tc.enAll != "" && en != tc.enAll {
			t.Errorf("%s 英文整句应为 %q，得到 %q", tc.path, tc.enAll, en)
		}
		if tc.path == "/num" {
			numErr = err
		}
		if tc.path == "/wrap" && !strings.Contains(en, "no longer in the center") {
			t.Errorf("%s 英文应同时含外层译文，得到 %q", tc.path, en)
		}
		if tc.path == "/raw" && !strings.Contains(en, "cannot send the brief") {
			t.Errorf("%s 英文应翻译外层，得到 %q", tc.path, en)
		}
		if tc.path == "/version" && !strings.Contains(en, "version 9") {
			t.Errorf("%s 英文应含现在的版本，得到 %q", tc.path, en)
		}
		if tc.hide != "" && (strings.Contains(zh, tc.hide) || strings.Contains(en, tc.hide)) {
			t.Errorf("%s 不应把键名显示出来：中文 %q 英文 %q", tc.path, zh, en)
		}
		if tc.miss && len(miss) == 0 {
			t.Errorf("%s 应记一次缺键", tc.path)
		}
		if !tc.miss && len(miss) != 0 {
			t.Errorf("%s 不应记缺键：%q", tc.path, miss)
		}
	}

	// 成功响应里的数字也留成 json.Number，再按 %d 印。
	var okBody struct {
		NoteCode string `json:"note_code"`
		NoteArgs []any  `json:"note_args"`
	}
	if err := c.Do(ctx, http.MethodGet, "/oknum", nil, &okBody); err != nil {
		t.Fatal(err)
	}
	if _, ok := okBody.NoteArgs[0].(json.Number); !ok {
		t.Fatalf("成功响应的整数应是 json.Number，实际 %T", okBody.NoteArgs[0])
	}
	i18n.SetLang(i18n.EN)
	okEN := deckclient.Text(okBody.NoteCode, okBody.NoteArgs, "")
	const okWant = "the new main agent took over 2 sub-agents and 3 reports that the old main agent left waiting to be sent"
	if okEN != okWant {
		t.Fatalf("成功响应英文整句应为 %q，得到 %q", okWant, okEN)
	}

	// deckd 的 JSON 再落盘、重读，%d 仍是整数。
	if numErr == nil {
		t.Fatal("没有拿到 /num 的错误")
	}
	var de *deckclient.Error
	if !errors.As(numErr, &de) {
		t.Fatalf("应是 deckclient.Error：%T", numErr)
	}
	num, ok := de.Args[1].(json.Number)
	if !ok {
		t.Fatalf("错误参数里的整数应是 json.Number，实际 %T", de.Args[1])
	}
	n, err := num.Int64()
	if err != nil || n != 2 {
		t.Fatalf("json.Number 应还原成 2：%v %v", n, err)
	}
	root := t.TempDir()
	st, err := deckstore.Open(deckstore.Dirs{Config: filepath.Join(root, "cfg"), State: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	// 落盘用还原后的整数。Error() 不认 json.Number，中文原文要和 deckd 写出时一样。
	if _, err := st.NotifyCoded("", "n", i18n.E(i18n.Key(de.Code), de.Args[0], n), "deckd"); err != nil {
		t.Fatal(err)
	}
	st2, err := deckstore.Open(deckstore.Dirs{Config: filepath.Join(root, "cfg"), State: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	ns := st2.Notifications(0, "")
	if len(ns) != 1 {
		t.Fatalf("重读应有 1 条通知，实际 %d", len(ns))
	}
	if _, ok := ns[0].Args[1].(json.Number); !ok {
		t.Fatalf("重读后的整数应是 json.Number，实际 %T", ns[0].Args[1])
	}
	i18n.SetLang(i18n.ZH)
	zh := deckclient.Text(ns[0].Code, []any(ns[0].Args), ns[0].Text)
	i18n.SetLang(i18n.EN)
	en := deckclient.Text(ns[0].Code, []any(ns[0].Args), ns[0].Text)
	if zh != "upstream agent-deck up 返回 HTTP 2" || en != "upstream agent-deck up returned HTTP 2" {
		t.Fatalf("落盘重读后中英整句不对：%q / %q", zh, en)
	}
	if strings.Contains(en, "%!d") || strings.Contains(zh, "%!d") {
		t.Fatalf("落盘重读把整数印坏了：%q / %q", zh, en)
	}
}
