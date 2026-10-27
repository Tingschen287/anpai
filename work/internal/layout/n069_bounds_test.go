package layout

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestN069LockCtxWaitStopsAtDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "layout.lock")
	unlock, err := Lock(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	u, err := LockCtx(ctx, path, 10*time.Second)
	if u != nil {
		u()
	}
	t.Logf("elapsed=%v err=%v", time.Since(start), err)
	if err == nil || time.Since(start) > time.Second {
		t.Fatal("lock wait discarded deadline")
	}
}

func TestN069DetachedMuxHasPerCommandDeadline(t *testing.T) {
	d := t.TempDir()
	// exec avoids a descendant retaining stdout after the fake tmux is killed.
	if err := os.WriteFile(filepath.Join(d, "tmux"), []byte("#!/bin/sh\nexec /bin/sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", d+":"+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := TmuxMux{Socket: filepath.Join(d, "unused"), Ctx: ctx}
	start := time.Now()
	_, err := m.Detached().Panes()
	took := time.Since(start)
	t.Logf("elapsed=%v err=%v", took, err)
	if err == nil || took < 4*time.Second || took > 7*time.Second {
		t.Fatal("detached command should run despite parent cancellation, then stop at muxTimeout")
	}
}

// 步骤自己的时限（复审 2 的 R2-2）：父 ctx 已取消，Step 的 Mux 仍照常开始命令（不随取消），到了步骤时限就停，
// 比每条命令的 5 秒上限更短；Detached 则是只受每条命令 5 秒上限。
func TestN069StepMuxHasItsOwnShortDeadline(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "tmux"), []byte("#!/bin/sh\nexec /bin/sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", d+":"+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := TmuxMux{Socket: filepath.Join(d, "unused"), Ctx: ctx}
	sm, done := m.Step(700 * time.Millisecond)
	defer done()
	start := time.Now()
	_, err := sm.Panes()
	took := time.Since(start)
	t.Logf("elapsed=%v err=%v", took, err)
	if err == nil || took < 500*time.Millisecond || took > 3*time.Second {
		t.Fatal("步骤 Mux 应该不随父取消地开始命令，并在步骤时限（0.7 秒）停下")
	}
	// 步骤 Mux 的 Detached（收尾恢复用）不再受步骤时限截断
	if e := sm.(interface{ Err() error }).Err(); e == nil {
		t.Fatal("前提：步骤时限已过")
	}
	dm := sm.(interface{ Detached() Mux }).Detached()
	if e := dm.(interface{ Err() error }).Err(); e != nil {
		t.Fatalf("收尾用的 Mux 不该带着已过期的步骤时限：%v", e)
	}
}
