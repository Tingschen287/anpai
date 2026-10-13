package agentdeck

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// n055：deck up 自管 headless 服务前先探一次：没在听、token 不对或别的服务占着、正常，三种要分开。
func TestProbeWeb(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/menu" || r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	if err := ProbeWeb(context.Background(), "deck", addr, "good"); err != nil {
		t.Fatalf("服务在、token 对：%v", err)
	}
	if err := ProbeWeb(context.Background(), "deck", addr, "bad"); err == nil || errors.Is(err, ErrWebDown) {
		t.Fatalf("token 不对：不是「没在跑」：%v", err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	free := l.Addr().String()
	l.Close()
	if err := ProbeWeb(context.Background(), "deck", free, "good"); !errors.Is(err, ErrWebDown) {
		t.Fatalf("没在听：%v", err)
	}
}

func TestWebServeCommand(t *testing.T) {
	got := WebServeCommand("agent-deck", "deck", "127.0.0.1:8420", "/home/x/it's/agentdeck-web.token")
	want := `exec env AGENTDECK_PROFILE='deck' 'agent-deck' web --no-tui --listen '127.0.0.1:8420' --token-file '/home/x/it'\''s/agentdeck-web.token'`
	if got != want {
		t.Fatalf("\n%s\n%s", got, want)
	}
	for _, f := range []string{"--no-tui", "--listen", "--token-file"} {
		found := false
		for _, w := range WebFlags {
			found = found || w == f
		}
		if !found {
			t.Fatalf("%s 要在 WebFlags 里", f)
		}
	}
	if strings.Contains(got, "--token ") {
		t.Fatal("token 不能出现在命令行参数里")
	}
}
