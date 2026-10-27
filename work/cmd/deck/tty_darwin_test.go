package main

import (
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// macOS 上按设备号找终端路径：一对 pty 的从端，应认出它的 /dev/ttysNNN；不是终端的文件认不出。
func TestTTYPathOfFindsThePtySlave(t *testing.T) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skip("开不了 pty：", err)
	}
	defer m.Close()
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, m.Fd(), unix.TIOCPTYGRANT, 0); e != 0 {
		t.Skip(e)
	}
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, m.Fd(), unix.TIOCPTYUNLK, 0); e != 0 {
		t.Skip(e)
	}
	var name [128]byte
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, m.Fd(), unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); e != 0 {
		t.Skip(e)
	}
	n := 0
	for name[n] != 0 {
		n++
	}
	s, err := os.OpenFile(string(name[:n]), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skip(err)
	}
	defer s.Close()
	if got := ttyPathOf(int(s.Fd())); got != string(name[:n]) {
		t.Fatalf("从端 %s 被认成 %q", name[:n], got)
	}
	f, _ := os.CreateTemp(t.TempDir(), "x")
	defer f.Close()
	if got := ttyPathOf(int(f.Fd())); got != "" {
		t.Fatalf("普通文件不该有终端路径：%q", got)
	}
}
