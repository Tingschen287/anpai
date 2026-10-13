package agentdeck

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProc 建一棵假的 /proc：net/tcp 里一条 LISTEN 行（端口 8420、属主 uid、inode 4242），pid 100 持有它、exe 指向 exe。
func fakeProc(t *testing.T, listen bool, uid int, exe string) {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "net"), 0o755)
	tcp := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	if listen {
		tcp += fmt.Sprintf("   0: 0100007F:20E4 00000000:0000 0A 00000000:00000000 00:00000000 00000000  %d        0 4242 1 0000000000000000 100 0 0 10 0\n", uid)
	}
	tcp += "   1: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 7777 1\n" // 别的端口
	os.WriteFile(filepath.Join(root, "net", "tcp"), []byte(tcp), 0o644)
	os.WriteFile(filepath.Join(root, "net", "tcp6"), []byte("  sl  local_address\n"), 0o644)
	os.MkdirAll(filepath.Join(root, "100", "fd"), 0o755)
	os.Symlink("socket:[4242]", filepath.Join(root, "100", "fd", "5"))
	os.Symlink(exe, filepath.Join(root, "100", "exe"))
	old := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = old })
}

// n055 评审 1 B1：带 token 探测之前先认清端口上是谁；不是本用户的 agent-deck（或认不清）就不带 token。
func TestCheckWebListener(t *testing.T) {
	me := os.Getuid()
	fakeProc(t, false, me, "/home/x/.local/bin/agent-deck")
	if err := CheckWebListener("127.0.0.1:8420"); !errors.Is(err, ErrWebDown) {
		t.Fatalf("没人听：%v", err)
	}
	fakeProc(t, true, me, "/home/x/.local/bin/agent-deck")
	if err := CheckWebListener("127.0.0.1:8420"); err != nil {
		t.Fatalf("本用户的 agent-deck：%v", err)
	}
	fakeProc(t, true, me+1, "/home/x/.local/bin/agent-deck")
	if err := CheckWebListener("127.0.0.1:8420"); !errors.Is(err, ErrForeignListener) || !strings.Contains(err.Error(), "用户") {
		t.Fatalf("别的用户：%v", err)
	}
	fakeProc(t, true, me, "/usr/bin/python3")
	if err := CheckWebListener("127.0.0.1:8420"); !errors.Is(err, ErrForeignListener) || !strings.Contains(err.Error(), "python3") {
		t.Fatalf("本用户的别的程序：%v", err)
	}
	fakeProc(t, true, me, "/home/x/.local/bin/agent-deck")
	os.Remove(filepath.Join(procRoot, "100", "fd", "5")) // 找不到持有者（例如属于看不到的进程）
	if err := CheckWebListener("127.0.0.1:8420"); !errors.Is(err, ErrForeignListener) {
		t.Fatalf("找不到持有者：%v", err)
	}
	old := procRoot
	procRoot = filepath.Join(t.TempDir(), "none")
	defer func() { procRoot = old }()
	if err := CheckWebListener("127.0.0.1:8420"); !errors.Is(err, ErrForeignListener) {
		t.Fatalf("读不了监听表按不是自己的处理：%v", err)
	}
}
