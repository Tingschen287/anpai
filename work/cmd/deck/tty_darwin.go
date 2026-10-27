package main

import (
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// stdinTTY 是标准输入所在终端的设备路径（/dev/ttysNNN），取不到返回空。tmux 用它认客户端（refresh-client -t）。
// macOS 没有 /proc/self/fd，也没有 ttyname 的纯 Go 版：按标准输入的设备号在 /dev 下找同一个字符设备。
func stdinTTY() string { return ttyPathOf(0) }

// ttyPathOf 是 fd 所在终端的设备路径。
func ttyPathOf(fd int) string {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFCHR {
		return ""
	}
	for _, pattern := range []string{"/dev/ttys[0-9]*", "/dev/tty[a-z0-9]*"} {
		names, _ := filepath.Glob(pattern)
		for _, n := range names {
			fi, err := os.Stat(n)
			if err != nil {
				continue
			}
			if s, ok := fi.Sys().(*syscall.Stat_t); ok && s.Rdev == st.Rdev {
				return n
			}
		}
	}
	return ""
}
