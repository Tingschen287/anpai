package agentdeck

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"slices"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// macOS 的本平台进程表：sysctl。
const nativeProcTableName = "sysctl kern.proc.all"

// nativePIDs 列出全部进程（kern.proc.all）。
func nativePIDs() ([]int, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, len(procs))
	for i := range procs {
		if pid := int(procs[i].Proc.P_pid); pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// nativeChildren 建父进程 → 子进程表。读不到进程表时返回空表（和 Linux 版读不到 /proc 时一样）。
func nativeChildren() map[int][]int {
	out := map[int][]int{}
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return out
	}
	for i := range procs {
		pid, ppid := int(procs[i].Proc.P_pid), int(procs[i].Eproc.Ppid)
		if pid > 0 {
			out[ppid] = append(out[ppid], pid)
		}
	}
	return out
}

// sysctlProcargs 读一个进程的 kern.procargs2 原始数据。做成变量是为了让测试注入截断、畸形的数据。
var sysctlProcargs = func(pid int) ([]byte, error) { return unix.SysctlRaw("kern.procargs2", pid) }

// initialArgs 是进程启动时的 os.Args。运行时自检拿它和内核给出的自己的命令行比对；
// 在 init 里留一份，是因为 main 可能会改 os.Args（切掉子命令）。
var initialArgs = slices.Clone(os.Args)

// nativeCmdline 取一个进程的命令行，格式同 /proc/<pid>/cmdline：参数之间用 NUL 隔开。
//
// 两类失败要分开（调用方的处理不同）：
//   - 读不到（进程已退出或无权读：ESRCH、EINVAL、EPERM，见 procUnreadable）：原样返回系统错误，调用方当作没有这个进程，跳过；
//   - 确认不了（缓冲区截断、读不足 argc 个参数、版式不对、运行时自检没过，以及 ENOMEM、EIO 等其他一切读错误）：
//     返回 errProcArgsUnconfirmable（带 n060 错误码），CheckSessionArgs 据此拒绝（fail closed），不会拿半截命令行去核对。
func nativeCmdline(pid int) ([]byte, error) {
	procargsSelf.once.Do(func() { procargsSelf.err = selfCheckHook() })
	if procargsSelf.err != nil {
		return nil, i18n.E("err.agentdeck.procargs.selffail", errProcArgsUnconfirmable, errProcArgsSelfCheck, procargsSelf.err)
	}
	return readCmdline(pid)
}

// procUnreadable 说明一个 sysctl 错误是不是「这个进程已经不在，或这个调用方无权读它」。只有这三种算读不到，跳过那个进程：
//   - ESRCH：没有这个进程；
//   - EINVAL：kern.procargs2 对不存在、刚退出（僵尸）或读不了参数区的 pid 返回 EINVAL。SDK 的 sysctl(3) 手册里 EINVAL 还可能是
//     mib 数组或长度非法，但这里 mib 固定、缓冲按先查到的大小分配（x/sys 的 SysctlRaw），那两种不会因目标进程而出现；
//   - EPERM：没有权限读这个进程（别的用户的进程）。
//
// ENOMEM（SDK 手册：数据比缓冲大时复制放得下的部分并返回 ENOMEM；也可能是锁内存超限；x/sys 的 SysctlRaw 先查大小再读，
// 两次之间参数区变大，第二次就会 ENOMEM 并丢掉 buf）、EIO 等其他一切错误，都不能说明「进程不在」，归为确认不了。
// 手册没有逐条写出 kern.procargs2 的 errno（XNU 源码不在本机），所以分类宁可严：认不出来的错误一律当确认不了，
// 代价只是多拒绝一次，不会放过一个读错了的进程。
func procUnreadable(err error) bool {
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.EPERM)
}

// readCmdline 读并解析一次。参数区在 exec 进行中或被目标进程改写时可能一时不完整：遇到确认不了的结果重读几次（总共最多 3 次读），再放弃。
//
// 「确认不了」是黏性的：一旦发现过（缓冲区截断、解析不对、ENOMEM/EIO 等读错误），后面再遇到任何非成功的结果——包括
// ESRCH、EINVAL、EPERM——都保持确认不了，不会被后来的错误覆盖成「读不到」而跳过；只有之后某次完整读成功才通过。
// 还没发现过确认不了时，第一次读到 ESRCH、EINVAL、EPERM 就直接返回（进程不在或无权读，不重读）。
func readCmdline(pid int) ([]byte, error) {
	var unconfirmed error
	for try := 0; try < 3; try++ {
		if try > 0 {
			time.Sleep(5 * time.Millisecond)
		}
		buf, err := sysctlProcargs(pid)
		if err != nil {
			if unconfirmed == nil {
				if procUnreadable(err) {
					return nil, err
				}
				unconfirmed = i18n.E("err.agentdeck.procargs.readfail", errProcArgsUnconfirmable, err)
			}
			continue
		}
		out, err := parseProcargs2(buf)
		if err == nil {
			return out, nil
		}
		if unconfirmed == nil {
			unconfirmed = err
		}
	}
	return nil, unconfirmed
}

// 运行时自检（启动样本自检）：第一次读命令行之前，先读自己进程的 procargs2，解析出的命令行必须和启动时的 os.Args 逐字一致。
// 它只验证「这个进程自己的这一份样本」按本文件的规则解得对，不证明别的路径长度、空 argv[0] 或别的 macOS 版本的版式也解得对
// （自检通过时，空 argv[0] 之类需要对齐才解得对的版式并没有被覆盖；那部分靠 procs_darwin_test.go 里本机的多路径真实子进程测试）。
// 不一致（解析规则和这台机器的内核版式对不上）就让 nativeCmdline 一律报确认不了，调用方按 fail closed 处理。
var procargsSelf struct {
	once sync.Once
	err  error
}

var selfCheckHook = selfCheckProcargs

func selfCheckProcargs() error {
	buf, err := sysctlProcargs(os.Getpid())
	if err != nil {
		return i18n.E("err.agentdeck.procargs.self.read", err)
	}
	got, err := parseProcargs2(buf)
	if err != nil {
		return i18n.E("err.agentdeck.procargs.self.parse", err)
	}
	var want []byte
	for _, a := range initialArgs {
		want = append(append(want, a...), 0)
	}
	if !bytes.Equal(got, want) {
		return i18n.E("err.agentdeck.procargs.self.mismatch", string(got), string(want))
	}
	return nil
}

// maxProcArgc 是 argc 的上限：内核给整个参数加环境变量区的上限是 1 MiB（NCARGS），每个参数至少占一个指针 8 字节加一个 NUL，
// 所以真实的 argc 到不了 1<<17。超过就当数据不可信。
const maxProcArgc = 1 << 17

// procargs2 里字符串区的对齐：内核让字符串区从 8 字节对齐的地址开始，而缓冲区的前 4 个字节是 argc，
// 所以第一个参数（argv[0]）的偏移 ≡ 4 (mod 8)，可执行文件路径的结尾 NUL 与它之间补若干个 NUL（0～7 个）。
const (
	procargsAlign = 8
	procargsBase  = 4
)

// parseProcargs2 解 kern.procargs2 的返回：int32 argc，可执行文件路径（NUL 结尾），若干 NUL 填充，
// 然后是 argc 个以 NUL 结尾的参数，最后才是环境变量。
//
// 保证（和 Linux 的 /proc/<pid>/cmdline 一致，也只到这里为止）：
//   - 读满 argc 个参数才返回成功；缓冲区被截断、不足 argc 个、argc 不合理、路径没有结尾，一律返回 errProcArgsUnconfirmable
//     （带 n060 错误码的各个原因，见 err_n067.go），不返回半截 argv；
//   - 成功返回时只含 argc 个参数，环境变量区不会被整段读进来。
//
// 不保证：目标进程改写自己参数区末尾的 NUL（setproctitle 一类，或恶意改写）时，最后一个参数可能连上紧跟其后的环境变量。
// 这一点 Linux 一样：/proc/<pid>/cmdline 在同样情况下，内核（get_mm_cmdline）把它当成 setproctitle，继续读进环境变量区，
// 直到遇到 NUL。两个平台都做不到「目标进程篡改自己内存时，也保证读不到环境变量」。
// 威胁模型防的是配置或启动参数里的绕过，不防会改写自身内存的进程；被连成一串的绕过参数由 argallow.go 的
// checkGluedTail 认出来（只查最后一个 argv 元素，两个平台相同）。
//
// 版式的依据：本机 SDK 头文件（MacOSX.sdk/usr/include/sys/sysctl.h）只有 KERN_PROCARGS2 这个编号（还包在 __APPLE_API_UNSTABLE 里），
// sys/syslimits.h 有 ARG_MAX = 1 MiB；man page 里没有这块的版式，XNU 源码不在本机。所以版式是实测得来的：
// 本机（x86_64、macOS 15.8.1）、真实内核给出的 argc、目标没有改写过参数字符串时：真实子进程、可执行文件路径 0～17 个字符
// （把 8 种填充都走一遍）、空 argv[0]、空参数在中间，见 procs_darwin_test.go。
// 外加上面的启动样本自检：它只核对本进程自己的一份样本，不能证明别的 macOS 版本或别的路径长度的版式也对；换平台版本要重新实测。
// 解析器不去猜字节上分不出来的畸形输入（例如合成数据谎报 argc、pad=0 加空 argv[0]）：那些不是本机真实内核在目标没改写参数字符串时会给出的版式。
//
// 难点是 argv[0] 为空（或前几个参数为空）：空参数就是一个孤零零的 NUL，和填充分不开。把所有 NUL 当填充跳过，
// 会吞掉空参数，于是 argc 个字符串多读走的是环境变量（审查 1 的反例）。
// 做法：填充的长度由对齐决定（见 procargsAlign），超出对齐位置的那些 NUL 才是空参数；
// 之后严格按 NUL 切，不再跳过空串。
func parseProcargs2(buf []byte) ([]byte, error) {
	if len(buf) < 4 {
		return nil, i18n.E("err.agentdeck.procargs.tooshort", errProcArgsUnconfirmable)
	}
	argc := int(int32(binary.LittleEndian.Uint32(buf[:4])))
	if argc < 0 || argc > maxProcArgc {
		return nil, i18n.E("err.agentdeck.procargs.argcrange", errProcArgsUnconfirmable, argc)
	}
	// 跳过可执行文件路径
	pathEnd := bytes.IndexByte(buf[4:], 0)
	if pathEnd < 0 {
		return nil, i18n.E("err.agentdeck.procargs.nopathend", errProcArgsUnconfirmable)
	}
	pathEnd += 4 // 路径结尾 NUL 的偏移
	// 路径之后的这一串 NUL：对齐的填充，加上 argv[0] 起头那些空参数
	runEnd := pathEnd + 1
	for runEnd < len(buf) && buf[runEnd] == 0 {
		runEnd++
	}
	aligned := pathEnd + 1
	for aligned%procargsAlign != procargsBase {
		aligned++
	}
	start, empties := runEnd, 0
	if runEnd > aligned {
		start, empties = aligned, runEnd-aligned
	}
	empties = min(empties, argc)
	out := bytes.Repeat([]byte{0}, empties)
	rest := buf[start+empties:]
	for n := empties; n < argc; n++ {
		j := bytes.IndexByte(rest, 0)
		if j < 0 {
			return nil, i18n.E("err.agentdeck.procargs.shortargs", errProcArgsUnconfirmable, n, argc)
		}
		out = append(out, rest[:j]...)
		out = append(out, 0)
		rest = rest[j+1:]
	}
	return out, nil
}
