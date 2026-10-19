package deckd

import (
	"context"
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
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	c := deckclient.New(sock).WithTimeout(5 * time.Second)
	ctx := context.Background()

	type want struct {
		path string
		zh   string
		en   string
		miss bool
		hide string
	}
	cases := []want{
		{path: "/args", zh: inner.Error(), en: "no decision d1"},
		{path: "/wrap", zh: wrapped.Error(), en: "no decision d1"},
		{path: "/raw", zh: external.Error(), en: "KEEP-RAW"},
		{path: "/version", zh: version.Error(), en: "version 2"},
		{path: "/unknown", zh: "原始报错", en: "原始报错", miss: true, hide: "err.no.such"},
		{path: "/badargs", zh: "没有决定 d1", en: "没有决定 d1", miss: true, hide: "err.deckstore.storeanswer.1"},
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
		i18n.SetLang(i18n.EN)
		stop := i18n.RecordMisses()
		en := deckclient.Show(err)
		miss := stop()
		if !strings.Contains(en, tc.en) {
			t.Errorf("%s 英文应含 %q，得到 %q", tc.path, tc.en, en)
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
}
