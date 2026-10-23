package main

import (
	"os"
	"strconv"
)

// stdinTTY 是标准输入所在终端的设备路径（/dev/pts/N），取不到返回空。tmux 用它认客户端（refresh-client -t）。
func stdinTTY() string { return ttyPathOf(0) }

// ttyPathOf 是 fd 所在终端的设备路径。
func ttyPathOf(fd int) string {
	tty, _ := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
	return tty
}
