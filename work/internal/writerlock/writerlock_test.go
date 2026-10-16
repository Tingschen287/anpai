package writerlock

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// 同一 profile 第二个写入者取不到锁，并说清楚是谁占着；释放后可再取。
func TestSecondWriterForSameProfileIsRefused(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	a, err := Acquire("deck", "deckd")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Acquire("deck", "work")
	var held *ErrHeld
	if !errors.As(err, &held) || held.By == nil || held.By.Program != "deckd" {
		t.Fatalf("第二个写入者应被拒并点名 deckd：%v", err)
	}
	if !strings.Contains(err.Error(), "只读") {
		t.Errorf("拒绝要说明本进程只读：%v", err)
	}
	a.Release()
	b, err := Acquire("deck", "work")
	if err != nil {
		t.Fatalf("释放后应能再取：%v", err)
	}
	b.Release()
}

// 不同 profile 互不影响：允许 deck 与旧 work 用不同 profile 同时运行。
func TestDifferentProfilesDoNotConflict(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	a, err := Acquire("deck", "deckd")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()
	b, err := Acquire("_workdeck", "work")
	if err != nil {
		t.Fatalf("不同 profile 不该互斥：%v", err)
	}
	b.Release()
}

// 锁跨进程有效：子进程持有时，本进程取不到（flock 是进程间的，不是进程内 mutex）。
func TestLockIsExclusiveAcrossProcesses(t *testing.T) {
	if os.Getenv("WRITERLOCK_CHILD") == "1" {
		if _, err := Acquire("deck", "child"); err != nil {
			os.Exit(3)
		}
		os.Stdout.WriteString("held\n")
		buf := make([]byte, 1)
		os.Stdin.Read(buf) // 等父进程测完
		os.Exit(0)
	}
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	cmd := exec.Command(os.Args[0], "-test.run", "^TestLockIsExclusiveAcrossProcesses$")
	cmd.Env = append(os.Environ(), "WRITERLOCK_CHILD=1", "XDG_STATE_HOME="+dir)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := out.Read(buf); err != nil || string(buf[:4]) != "held" {
		t.Fatalf("子进程没取到锁：%q %v", buf, err)
	}
	_, err := Acquire("deck", "parent")
	var held *ErrHeld
	if !errors.As(err, &held) || held.By == nil || held.By.Program != "child" {
		t.Errorf("子进程持有时父进程应取不到并点名 child：%v", err)
	}
	in.Close()
	cmd.Wait()
}

// profile 名不能逃出锁目录。
func TestUnsafeProfileNameIsRejected(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, p := range []string{"../x", "a/b", "", strings.Repeat("a", 65)} {
		if _, err := Acquire(p, "x"); err == nil {
			t.Errorf("profile %q 应被拒", p)
		}
	}
}
