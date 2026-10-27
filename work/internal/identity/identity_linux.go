package identity

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// PeerPID 取 unix socket 连接对端进程的 pid。
func PeerPID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *syscall.Ucred
	var serr error
	if err := raw.Control(func(fd uintptr) {
		cred, serr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if serr != nil {
		return 0, serr
	}
	return int(cred.Pid), nil
}

// soPeerPIDFD 是 SO_PEERPIDFD（Linux 6.5 起；asm-generic 下为 77）。标准库 syscall 没有这个常量。
const soPeerPIDFD = 77

// PeerPIDFD 取连接对端进程的 pidfd。内核在 connect 时记下对端的进程结构，所以 pidfd 指向的一定是建立连接的那个进程，
// 不会因为 pid 被复用而指错。用完要 Close（syscall.Close）。
func PeerPIDFD(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return -1, err
	}
	fd, serr := -1, error(nil)
	if err := raw.Control(func(s uintptr) {
		fd, serr = syscall.GetsockoptInt(int(s), syscall.SOL_SOCKET, soPeerPIDFD)
	}); err != nil {
		return -1, err
	}
	if errors.Is(serr, syscall.ENOPROTOOPT) {
		return -1, ErrNoPIDFD
	}
	if serr != nil {
		return -1, serr
	}
	return fd, nil
}

// PIDFDHolds 返回一个检查：pidfd 指向的进程仍然持有 pid。进程被回收后 /proc/self/fdinfo 里的 Pid 变为 -1
// （僵尸状态下 pid 仍被占着、不会被复用），所以「Pid 等于 pid」就说明此刻 /proc/<pid> 是这个进程。
func PIDFDHolds(fd, pid int) func() error {
	return func() error {
		data, err := os.ReadFile(fmt.Sprintf("/proc/self/fdinfo/%d", fd))
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(data), "\n") {
			if v, ok := strings.CutPrefix(line, "Pid:"); ok {
				got, err := strconv.Atoi(strings.TrimSpace(v))
				if err != nil {
					return err
				}
				if got != pid {
					return fmt.Errorf("pidfd 现在指向 pid %d（-1 表示已退出）", got)
				}
				return nil
			}
		}
		return errors.New("pidfd 的 fdinfo 里没有 Pid")
	}
}

// PeerLifetime 把连接绑定到对端进程本身：返回的 same 在对端进程仍持有 pid 时返回 nil，release 用完要调。
// Linux 用 SO_PEERPIDFD 的 pidfd（PeerPIDFD、PIDFDHolds）；内核不支持时返回 ErrNoPIDFD，调用方据此不授予任何角色。
func PeerLifetime(c *net.UnixConn, pid int) (same func() error, release func(), err error) {
	fd, err := PeerPIDFD(c)
	if err != nil {
		return nil, func() {}, err
	}
	return PIDFDHolds(fd, pid), func() { syscall.Close(fd) }, nil
}

// nativeStat 读 /proc/<pid>/stat：父进程与出生时间（开机后的时钟滴答数）。
func nativeStat(pid int) (int, uint64, error) { return statOf("/proc", pid) }
