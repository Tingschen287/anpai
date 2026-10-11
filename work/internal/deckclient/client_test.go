package deckclient

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 已连上、等答复超时：不能说成「连不上 deckd」，操作可能还在进行（n051 端到端实测）。
func TestTimeoutIsNotUnreachable(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(time.Second) })}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	err = New(sock).WithTimeout(100*time.Millisecond).Do(context.Background(), "POST", "/v1/agents", map[string]string{}, nil)
	if err == nil || !strings.Contains(err.Error(), "仍在进行") || strings.Contains(err.Error(), "连不上") {
		t.Fatalf("超时应提示操作可能仍在进行：%v", err)
	}
	err = New(filepath.Join(t.TempDir(), "none.sock")).Get(context.Background(), "/v1/health", nil)
	if err == nil || !strings.Contains(err.Error(), "连不上") {
		t.Fatalf("没有 deckd 应说连不上：%v", err)
	}
}
