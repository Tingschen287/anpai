package deckd

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// n049 评审 4（n051 承接）：「deckd 没在运行」以单实例锁为准。

func TestProbeLockStates(t *testing.T) {
	d := t.TempDir()
	lock := filepath.Join(d, "deckd.lock")
	if st, err := ProbeLock(lock); err != nil || st != LockMissing {
		t.Fatalf("锁文件不存在应是 LockMissing：%v %v", st, err)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatal("探测不应创建锁文件")
	}
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if st, err := ProbeLock(lock); err != nil || st != LockFree {
		t.Fatalf("没人持有应是 LockFree：%v %v", st, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	if st, err := ProbeLock(lock); err != nil || st != LockHeld {
		t.Fatalf("被持有应是 LockHeld：%v %v", st, err)
	}
	if LockPathForSocket(filepath.Join(d, "deckd.sock")) != lock {
		t.Fatal("锁文件应与 socket 同目录")
	}
}

// 启动时抢锁稍等重试：别的进程（deck center 的探测）只持锁一瞬，deckd 不该因此起不来；真被占着就报错。
func TestFlockRetryToleratesBriefHolder(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "deckd.lock")
	holder, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(lock, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := flockRetry(f, 100*time.Millisecond); err == nil {
		t.Fatal("一直被占着应报错")
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		syscall.Flock(int(holder.Fd()), syscall.LOCK_UN)
	}()
	if err := flockRetry(f, 2*time.Second); err != nil {
		t.Fatalf("对方放开后应拿到：%v", err)
	}
}
