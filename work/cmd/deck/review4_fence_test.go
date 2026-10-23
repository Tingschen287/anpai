package main

import (
	"context"
	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/testsock"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestReview4FenceDoesNotAcceptUnconfirmedJSON(t *testing.T) {
	shortFence(t)
	d := testsock.Dir(t)
	sock := filepath.Join(d, "deckd.sock")
	ln, e := net.Listen("unix", sock)
	if e != nil {
		t.Fatal(e)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"fenced":false}`)) })}
	go srv.Serve(ln)
	defer srv.Close()
	go func() {
		close(entered)
		fence(deckclient.New(sock), filepath.Join(d, "center.log"), func(string) {})
		close(done)
	}()
	<-entered
	select {
	case <-done:
		t.Error("fenced=false 被当成确认，栅栏放行")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
}
func TestReview4FenceDoesNotPassDuringDeckdShutdown(t *testing.T) {
	shortFence(t)
	d := testsock.Dir(t)
	sock := filepath.Join(d, "deckd.sock")
	ln, e := net.Listen("unix", sock)
	if e != nil {
		t.Fatal(e)
	}
	active, release, requestDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(active); <-release; w.Write([]byte(`{"ok":true}`)) })}
	go srv.Serve(ln)
	c := deckclient.New(sock)
	go func() {
		c.Do(context.Background(), "POST", "/v1/view", map[string]string{"op": "open"}, nil)
		close(requestDone)
	}()
	<-active
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	shutdownDone := make(chan struct{})
	go func() { srv.Shutdown(ctx); close(shutdownDone) }()
	// Shutdown 首先关监听、移除 Unix socket，但旧 handler 仍在执行。
	deadline := time.Now().Add(time.Second)
	for {
		conn, err := net.DialTimeout("unix", sock, 50*time.Millisecond)
		if err != nil {
			break
		}
		conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("监听未关闭")
		}
		time.Sleep(time.Millisecond)
	}
	passed := make(chan struct{})
	go func() { fence(c, filepath.Join(d, "center.log"), func(string) {}); close(passed) }()
	select {
	case <-passed:
		t.Error("旧视图 handler 仍未完成，栅栏已放行新 client")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-requestDone
	<-shutdownDone
}
