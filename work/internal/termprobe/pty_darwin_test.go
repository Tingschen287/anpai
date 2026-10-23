package termprobe

import (
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// openPty 开一对 pty（主端、从端），不引新依赖。macOS 没有 TIOCSPTLCK、TIOCGPTN：
// 用 TIOCPTYGRANT（grantpt）、TIOCPTYUNLK（unlockpt）、TIOCPTYGNAME（ptsname）三个 ioctl 完成同样的事。
func openPty() (*os.File, *os.File, error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	fd := m.Fd()
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, fd, unix.TIOCPTYGRANT, 0); e != 0 {
		m.Close()
		return nil, nil, e
	}
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, fd, unix.TIOCPTYUNLK, 0); e != 0 {
		m.Close()
		return nil, nil, e
	}
	var name [128]byte
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, fd, unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); e != 0 {
		m.Close()
		return nil, nil, e
	}
	n := 0
	for n < len(name) && name[n] != 0 {
		n++
	}
	s, err := os.OpenFile(string(name[:n]), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		return nil, nil, err
	}
	return m, s, nil
}
