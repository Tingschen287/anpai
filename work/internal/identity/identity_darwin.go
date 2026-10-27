package identity

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// macOS 没有 pidfd，但 socket 本身带着对端的 audit token：LOCAL_PEERTOKEN 取到的是「建立这条连接的那个进程」在 connect 时
// 的 audit token（8 个 uint32：auid、euid、egid、ruid、rgid、pid、asid、pidversion）。其中的 pidversion 是进程的代际号，
// 同一个 pid 被另一个进程复用后，代际号就变了；系统调用 proc_info（PROC_PIDUNIQIDENTIFIERINFO）能读到某个 pid 此刻的代际号。
// 所以对端身份由下面几件事合起来判定，任何一件取不到都按失败处理（fail closed）：
//
//   - LOCAL_PEERCRED（xucred）：对端的 uid 必须等于本进程的有效 uid。Linux 上这件事由 socket 所在目录的 0700 权限保证，
//     macOS 上多核对一次，不多花什么。
//   - LOCAL_PEERTOKEN：取连接的 audit token，其中的 pid、euid 就是对端的 pid、有效 uid（再与 LOCAL_PEERPID 互相核对）。
//   - 生命周期绑定（PeerLifetime）：把 token 里的 pidversion 当作这条连接的身份记下；绑定当时就要核对 pid 此刻的代际号等于它
//     （对端在 connect 之后、accept 之前退出并被复用，这里就发现了），之后每次核对都重读，不等就拒绝。
//     绑定对象是连接本身，不是「accept 之后才记下的出生时间」，所以没有 accept 前的空窗，不靠 pid 回绕慢来保证。
//
// 一个已知的取舍：进程 exec 之后 pidversion 也会变（macOS 把它当作进程映像的代际）。一条连接建立之后对端再 exec，
// 这条连接会被拒绝。deck 的客户端都是 connect 之后不再 exec 的短命进程；拒绝是 fail closed 的方向。

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

// auditToken 是 audit_token_t 的 8 个字段（<mach/message.h>、<bsm/libbsm.h>）。
type auditToken struct {
	AUID, EUID, EGID, RUID, RGID uint32
	PID                          uint32
	ASID                         uint32
	PIDVersion                   uint32
}

const (
	localPeerToken = unix.LOCAL_PEERTOKEN
	auditTokenSize = 32
)

// peerTokenOf 取连接的 audit token（getsockopt(SOL_LOCAL, LOCAL_PEERTOKEN)）；测试里换成别的实现来模拟取不到。
// 只认正好 32 字节的返回，其他一律是错误。
var peerTokenOf = func(c *net.UnixConn) (auditToken, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return auditToken{}, err
	}
	var tok [8]uint32
	n := uint32(auditTokenSize)
	var serr error
	if err := raw.Control(func(fd uintptr) {
		_, _, errno := syscall.Syscall6(syscall.SYS_GETSOCKOPT, fd, unix.SOL_LOCAL, localPeerToken,
			uintptr(unsafe.Pointer(&tok[0])), uintptr(unsafe.Pointer(&n)), 0)
		if errno != 0 {
			serr = errno
		}
	}); err != nil {
		return auditToken{}, err
	}
	if serr != nil {
		return auditToken{}, serr
	}
	if n != auditTokenSize {
		return auditToken{}, fmt.Errorf("LOCAL_PEERTOKEN 返回 %d 字节，期望 %d", n, auditTokenSize)
	}
	return auditToken{tok[0], tok[1], tok[2], tok[3], tok[4], tok[5], tok[6], tok[7]}, nil
}

const (
	procInfoCallPIDInfo      = 2  // PROC_INFO_CALL_PIDINFO
	procPIDUniqIdentifier    = 17 // PROC_PIDUNIQIDENTIFIERINFO
	procUniqIdentifierSize   = 56 // sizeof(struct proc_uniqidentifierinfo)
	procUniqIdentifierIDVers = 32 // p_idversion 在其中的偏移
)

// pidVersionOf 读 pid 此刻的代际号（proc_info PROC_PIDUNIQIDENTIFIERINFO 的 p_idversion）；进程不在或读不到就是错误。
// 测试里换成别的实现来模拟 pid 被复用。
var pidVersionOf = func(pid int) (uint32, error) {
	if pid <= 0 {
		return 0, fmt.Errorf("pid %d 不合法", pid)
	}
	var buf [procUniqIdentifierSize]byte
	r, _, errno := syscall.Syscall6(unix.SYS_PROC_INFO, procInfoCallPIDInfo, uintptr(pid), procPIDUniqIdentifier, 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if errno != 0 {
		return 0, errno
	}
	if r != procUniqIdentifierSize {
		return 0, fmt.Errorf("proc_info 返回 %d 字节，期望 %d", r, procUniqIdentifierSize)
	}
	return binary.LittleEndian.Uint32(buf[procUniqIdentifierIDVers:]), nil
}

// tokenOfPeer 取对端 token 并核对：uid 是本用户（LOCAL_PEERCRED 与 token 里的 euid 都要对）、pid 合法且与 LOCAL_PEERPID 一致。
func tokenOfPeer(c *net.UnixConn) (auditToken, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return auditToken{}, err
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
		return auditToken{}, err
	}
	if serr != nil {
		return auditToken{}, serr
	}
	tok, err := peerTokenOf(c)
	if err != nil {
		return auditToken{}, fmt.Errorf("取不到连接的 audit token：%w", err)
	}
	if tok.EUID != uint32(os.Geteuid()) {
		return auditToken{}, fmt.Errorf("对端 token 里的 euid %d 不是本用户 %d", tok.EUID, os.Geteuid())
	}
	if tok.PID == 0 || int64(tok.PID) > math.MaxInt32 {
		return auditToken{}, fmt.Errorf("对端 token 里的 pid %d 不合法", tok.PID)
	}
	if pid <= 0 || uint32(pid) != tok.PID {
		return auditToken{}, fmt.Errorf("对端 pid 对不上（LOCAL_PEERPID %d，token %d）", pid, tok.PID)
	}
	return tok, nil
}

// PeerPID 取 unix socket 连接对端进程的 pid，同时核对对端 uid 就是本用户。取不到或对不上都返回错误。
func PeerPID(c *net.UnixConn) (int, error) {
	tok, err := tokenOfPeer(c)
	if err != nil {
		return 0, err
	}
	return int(tok.PID), nil
}

// PeerLifetime 把连接绑定到对端进程本身（见文件头的说明）。pid 是 PeerPID 的结果。
// 取不到 token、token 里的 pid 不是 pid、或 pid 此刻的代际号已不是 token 里的那个，都返回错误，不返回 ErrNoPIDFD：
// 这不是「系统不支持」，而是「这条连接不可信」。
func PeerLifetime(c *net.UnixConn, pid int) (same func() error, release func(), err error) {
	tok, err := tokenOfPeer(c)
	if err != nil {
		return nil, func() {}, err
	}
	if int(tok.PID) != pid {
		return nil, func() {}, fmt.Errorf("对端 pid 在核对期间由 %d 变成 %d", pid, tok.PID)
	}
	bound := tok.PIDVersion
	check := func() error {
		cur, err := pidVersionOf(pid)
		if err != nil {
			return fmt.Errorf("对端进程 %d 已不在：%w", pid, err)
		}
		if cur != bound {
			return fmt.Errorf("pid %d 现在是另一个进程（代际号 %d → %d）", pid, bound, cur)
		}
		return nil
	}
	// 绑定当时就核对一次：对端在 connect 之后、现在之前已退出并被复用，这里就拒绝
	if err := check(); err != nil {
		return nil, func() {}, err
	}
	return check, func() {}, nil
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
