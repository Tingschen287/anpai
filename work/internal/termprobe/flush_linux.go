package termprobe

import "golang.org/x/sys/unix"

// flushInput 丢掉终端输入缓冲里还没读的内容（Linux：TCFLSH + TCIFLUSH）。
func flushInput(fd int) { unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH) }
