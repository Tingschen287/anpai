package identity

import (
	"errors"
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// macOS 没有 pidfd。对端身份由三件事合起来判定，任何一件取不到都按失败处理（fail closed）：
//
//   - LOCAL_PEERCRED（xucred）：对端的 uid 必须等于本进程的有效 uid。Linux 上这件事由 socket 所在目录的 0700 权限保证，
//     macOS 上多核对一次，不多花什么。
//   - LOCAL_PEERPID：对端 pid，和 Linux 的 SO_PEERCRED 里的 pid 同义（建立连接的那个进程）。
//   - 生命周期绑定（PeerLifetime）：记下对端 pid 此刻的出生时间（sysctl kern.proc.pid 的 p_starttime，微秒），
//     之后每次核对都重读，出生时间变了或进程没了就不成立。pid 被复用后，新进程的出生时间不会相同。
//     这比 pidfd 弱一档：pidfd 由内核在 connect 时钉住进程，这里是在 accept 之后才记下出生时间，
//     中间那一小段（毫秒级）里若对端退出且 pid 被复用，会记下新进程。macOS 的 pid 顺序分配、回绕后才复用，
//     这一段在实际中不可达；不能接受这个残余风险的部署应改用 Linux 6.5 及以上。

// xucredVersion 是 struct xucred 的版本号（XUCRED_VERSION）。
const xucredVersion = 0

// checkXucred 核对 LOCAL_PEERCRED 取回的凭据：版本认识、uid 等于 want。
func checkXucred(cred *unix.Xucred, want uint32) error {
	if cred == nil {
		return errors.New("取不到对端凭据")
	}
	if cred.Version != xucredVersion {
		return fmt.Errorf("对端凭据版本 %d 不认识（期望 %d）", cred.Version, xucredVersion)
	}
	if cred.Uid != want {
		return fmt.Errorf("对端 uid %d 不是本用户 %d", cred.Uid, want)
	}
	return nil
}

// PeerPID 取 unix socket 连接对端进程的 pid，同时核对对端 uid 就是本用户。取不到或对不上都返回错误。
func PeerPID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pid int
	var serr error
	if err := raw.Control(func(fd uintptr) {
		var cred *unix.Xucred
		cred, serr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if serr != nil {
			return
		}
		if serr = checkXucred(cred, uint32(os.Geteuid())); serr != nil {
			return
		}
		pid, serr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil {
		return 0, err
	}
	if serr != nil {
		return 0, serr
	}
	if pid <= 0 {
		return 0, fmt.Errorf("对端 pid %d 不合法", pid)
	}
	return pid, nil
}

// startTimeOf 读 pid 的出生时间；测试里换成会失败的版本，证明取不到时拒绝。
var startTimeOf = func(pid int) (uint64, error) {
	_, start, err := nativeStat(pid)
	return start, err
}

// PeerLifetime 把连接绑定到对端进程本身（见文件头的说明）。pid 是 PeerPID 的结果。
// 取不到出生时间、或再问一次对端 pid 对不上，都返回错误，不返回 ErrNoPIDFD：这不是「系统不支持」，而是「这条连接不可信」。
func PeerLifetime(c *net.UnixConn, pid int) (same func() error, release func(), err error) {
	born, err := startTimeOf(pid)
	if err != nil {
		return nil, func() {}, fmt.Errorf("读不到对端进程 %d 的出生时间：%w", pid, err)
	}
	again, err := PeerPID(c)
	if err != nil {
		return nil, func() {}, err
	}
	if again != pid {
		return nil, func() {}, fmt.Errorf("对端 pid 在核对期间由 %d 变成 %d", pid, again)
	}
	same = func() error {
		cur, err := startTimeOf(pid)
		if err != nil {
			return fmt.Errorf("对端进程 %d 已不在：%w", pid, err)
		}
		if cur != born {
			return fmt.Errorf("pid %d 现在是另一个进程（出生时间 %d → %d）", pid, born, cur)
		}
		return nil
	}
	return same, func() {}, nil
}

// nativeStat 读 sysctl kern.proc.pid：父进程与出生时间（微秒）。
func nativeStat(pid int) (int, uint64, error) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, 0, err
	}
	if int(k.Proc.P_pid) != pid {
		return 0, 0, fmt.Errorf("进程 %d 不存在", pid)
	}
	t := k.Proc.P_starttime
	return int(k.Eproc.Ppid), uint64(t.Sec)*1_000_000 + uint64(t.Usec), nil
}
