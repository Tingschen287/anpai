package deckd

import (
	"context"
	"errors"
	"fmt"
	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/identity"
	"github.com/Tingschen287/terminal-deck/work/internal/lease"
	"github.com/Tingschen287/terminal-deck/work/internal/viewstate"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestReview4FailedRestoreMustNotReleaseWritableSession(t *testing.T) {
	realTmux, e := exec.LookPath("tmux")
	if e != nil {
		t.Fatal(e)
	}
	w := newCenterWorld(t, func(a string) string { return "sh -c 'while true; do " + attachIdle(a) + "; done'" })
	l, e := lease.Open(filepath.Join(t.TempDir(), "leases.json"))
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	m := viewstate.New(w.center, l, func(id string) (viewstate.Target, bool) {
		return viewstate.Target{Session: "sub", Tmux: "agentdeck_sub", Role: "sub"}, true
	}, nil, nil)
	if e = m.Open("sub"); e != nil {
		t.Fatal(e)
	}
	old, e := w.center.client()
	if e != nil {
		t.Fatal(e)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	orig := w.center.Ancestry
	var calls atomic.Int32
	w.center.Ancestry = func(pid int) ([]identity.Proc, error) {
		chain, err := orig(pid)
		if pid == old.PID && calls.Add(1) == 4 {
			close(entered)
			<-release
		}
		return chain, err
	}
	done := make(chan error, 1)
	go func() { done <- m.Takeover("sub") }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("未停在放开输入之前")
	}
	if out, err := exec.Command(realTmux, "-S", w.agent, "detach-client", "-t", old.TTY).CombinedOutput(); err != nil {
		close(release)
		t.Fatalf("%v %s", err, out)
	}
	var next agentdeck.TmuxClient
	deadline := time.Now().Add(5 * time.Second)
	for {
		next, e = w.center.find(0)
		if e == nil && next.PID != old.PID {
			break
		}
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("新 client 未出现")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if next.TTY != old.TTY {
		close(release)
		t.Fatal("tty 不相同")
	}
	d := t.TempDir()
	counter := filepath.Join(d, "counter")
	wrapper := filepath.Join(d, "tmux")
	script := fmt.Sprintf("#!/usr/bin/python3\nimport sys,os\na=sys.argv[1:]\np=%q\nif 'switch-client' in a:\n n=int(open(p).read()) if os.path.exists(p) else 0\n open(p,'w').write(str(n+1))\n if n>0: sys.exit(1)\nos.execv(%q,[%q]+a)\n", counter, realTmux, realTmux)
	if e = os.WriteFile(wrapper, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", d+":/usr/bin:/bin")
	close(release)
	err := <-done
	if !errors.Is(err, viewstate.ErrStaleClient) {
		t.Fatalf("expected stale: %v", err)
	}
	st, e := agentdeck.ClientState(context.Background(), w.agent, next.TTY)
	if e != nil {
		t.Fatal(e)
	}
	ls, _ := l.Get("sub")
	t.Logf("client=%+v lease=%s state=%+v err=%v", st, ls.Holder, m.State(), err)
	if st.Session == "agentdeck_sub" && !st.ReadOnly && ls.Holder == lease.HolderWorkbench {
		t.Error("新 client 仍在子会话可写，却已释放 lease、恢复自动派送")
	}
}
func TestReview4SuccessfulRestoreControl(t *testing.T) {
	realTmux, e := exec.LookPath("tmux")
	if e != nil {
		t.Fatal(e)
	}
	w := newCenterWorld(t, func(a string) string { return "sh -c 'while true; do " + attachIdle(a) + "; done'" })
	l, e := lease.Open(filepath.Join(t.TempDir(), "leases.json"))
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	m := viewstate.New(w.center, l, func(id string) (viewstate.Target, bool) {
		return viewstate.Target{Session: "sub", Tmux: "agentdeck_sub", Role: "sub"}, true
	}, nil, nil)
	if e = m.Open("sub"); e != nil {
		t.Fatal(e)
	}
	old, e := w.center.client()
	if e != nil {
		t.Fatal(e)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	orig := w.center.Ancestry
	var calls atomic.Int32
	w.center.Ancestry = func(pid int) ([]identity.Proc, error) {
		chain, err := orig(pid)
		if pid == old.PID && calls.Add(1) == 4 {
			close(entered)
			<-release
		}
		return chain, err
	}
	done := make(chan error, 1)
	go func() { done <- m.Takeover("sub") }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("未停在放开输入之前")
	}
	if out, err := exec.Command(realTmux, "-S", w.agent, "detach-client", "-t", old.TTY).CombinedOutput(); err != nil {
		close(release)
		t.Fatalf("%v %s", err, out)
	}
	var next agentdeck.TmuxClient
	deadline := time.Now().Add(5 * time.Second)
	for {
		next, e = w.center.find(0)
		if e == nil && next.PID != old.PID {
			break
		}
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("新 client 未出现")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if next.TTY != old.TTY {
		close(release)
		t.Fatal("tty 不相同")
	}
	d := t.TempDir()
	counter := filepath.Join(d, "counter")
	wrapper := filepath.Join(d, "tmux")
	script := fmt.Sprintf("#!/usr/bin/python3\nimport sys,os\na=sys.argv[1:]\np=%q\nif 'switch-client' in a:\n n=int(open(p).read()) if os.path.exists(p) else 0\n open(p,'w').write(str(n+1))\n if False: sys.exit(1)\nos.execv(%q,[%q]+a)\n", counter, realTmux, realTmux)
	if e = os.WriteFile(wrapper, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", d+":/usr/bin:/bin")
	close(release)
	err := <-done
	if !errors.Is(err, viewstate.ErrStaleClient) {
		t.Fatalf("expected stale: %v", err)
	}
	st, e := agentdeck.ClientState(context.Background(), w.agent, next.TTY)
	if e != nil {
		t.Fatal(e)
	}
	ls, _ := l.Get("sub")
	t.Logf("client=%+v lease=%s state=%+v err=%v", st, ls.Holder, m.State(), err)
	if st.Session != viewstate.Idle || !st.ReadOnly || ls.Holder != lease.HolderWorkbench {
		t.Error("正常还原对照应为 idle、只读、lease 已交还")
	}
}
