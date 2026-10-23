package termprobe

import "golang.org/x/sys/unix"

// freadFlag 是 <sys/fcntl.h> 的 FREAD：TIOCFLUSH 的参数，表示清输入队列（x/sys/unix 没有导出它）。
const freadFlag = 0x0001

// flushInput 丢掉终端输入缓冲里还没读的内容（macOS：TIOCFLUSH，参数是要清的方向，FREAD 即输入）。
func flushInput(fd int) { unix.IoctlSetPointerInt(fd, unix.TIOCFLUSH, freadFlag) }
