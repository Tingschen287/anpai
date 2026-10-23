package agentdeck

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"slices"
	"sync"
	"time"

	"golang.org/x/sys/unix"
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
//   - 读不到（进程已退出、别的用户的进程被拒绝 EINVAL/EPERM）：原样返回系统错误，调用方当作没有这个进程，跳过；
//   - 读到了但确认不了（缓冲区截断、读不足 argc 个参数、版式不对、运行时自检没过）：返回 errProcArgsUnconfirmable，
//     CheckSessionArgs 据此拒绝（fail closed），不会拿半截命令行去核对。
func nativeCmdline(pid int) ([]byte, error) {
	procargsSelf.once.Do(func() { procargsSelf.err = selfCheckHook() })
	if procargsSelf.err != nil {
		return nil, fmt.Errorf("%w: %w: %v", errProcArgsUnconfirmable, errProcArgsSelfCheck, procargsSelf.err)
	}
	return readCmdline(pid)
}

// readCmdline 读并解析一次。参数区在 exec 进行中或被目标进程改写时可能一时不完整：遇到确认不了的错误重读几次，再放弃。
func readCmdline(pid int) ([]byte, error) {
	var lastErr error
	for try := 0; try < 3; try++ {
		if try > 0 {
			time.Sleep(5 * time.Millisecond)
		}
		buf, err := sysctlProcargs(pid)
		if err != nil {
			return nil, err
		}
		out, err := parseProcargs2(buf)
		if err == nil {
			return out, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// 运行时自检：第一次读命令行之前，先读自己进程的 procargs2，解析出的命令行必须和启动时的 os.Args 逐字一致。
// 不一致（解析规则和这台机器的内核版式对不上）就让 nativeCmdline 一律报确认不了，调用方按 fail closed 处理。
var procargsSelf struct {
	once sync.Once
	err  error
}

var selfCheckHook = selfCheckProcargs

func selfCheckProcargs() error {
	buf, err := sysctlProcargs(os.Getpid())
	if err != nil {
		return fmt.Errorf("cannot read own procargs2: %w", err)
	}
	got, err := parseProcargs2(buf)
	if err != nil {
		return fmt.Errorf("own procargs2: %w", err)
	}
	var want []byte
	for _, a := range initialArgs {
		want = append(append(want, a...), 0)
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("own procargs2 parsed as %q, but os.Args is %q", got, want)
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
//   - 读满 argc 个参数才返回成功；缓冲区被截断、不足 argc 个、argc 不合理、路径没有结尾，一律返回 errProcArgsUnconfirmable，
//     不返回半截 argv；
//   - 成功返回时只含 argc 个参数，环境变量区不会被整段读进来。
//
// 不保证：目标进程改写自己参数区末尾的 NUL（setproctitle 一类，或恶意改写）时，最后一个参数可能连上紧跟其后的环境变量。
// 这一点 Linux 一样：/proc/<pid>/cmdline 在同样情况下，内核（get_mm_cmdline）把它当成 setproctitle，继续读进环境变量区，
// 直到遇到 NUL。两个平台都做不到「目标进程篡改自己内存时，也保证读不到环境变量」。
// 威胁模型防的是配置或启动参数里的绕过，不防会改写自身内存的进程；被连成一串的绕过参数由 argallow.go 的
// checkBypassFlagsGlued 认出来。
//
// 版式的依据：本机 SDK 头文件（MacOSX.sdk/usr/include/sys/sysctl.h）只有 KERN_PROCARGS2 这个编号（还包在 __APPLE_API_UNSTABLE 里），
// sys/syslimits.h 有 ARG_MAX = 1 MiB；man page 里没有这块的版式，XNU 源码不在本机。所以版式是实测得来的：
// 真实子进程、可执行文件路径 0～17 个字符（把 8 种填充都走一遍）、空 argv[0]、空参数在中间，见 procs_darwin_test.go；
// 外加上面的运行时自检，防止别的 macOS 版本的版式和这里不同。
//
// 难点是 argv[0] 为空（或前几个参数为空）：空参数就是一个孤零零的 NUL，和填充分不开。把所有 NUL 当填充跳过，
// 会吞掉空参数，于是 argc 个字符串多读走的是环境变量（审查 1 的反例）。
// 做法：填充的长度由对齐决定（见 procargsAlign），超出对齐位置的那些 NUL 才是空参数；
// 之后严格按 NUL 切，不再跳过空串。
func parseProcargs2(buf []byte) ([]byte, error) {
	if len(buf) < 4 {
		return nil, fmt.Errorf("%w: procargs2 too short", errProcArgsUnconfirmable)
	}
	argc := int(int32(binary.LittleEndian.Uint32(buf[:4])))
	if argc < 0 || argc > maxProcArgc {
		return nil, fmt.Errorf("%w: procargs2 argc %d out of range", errProcArgsUnconfirmable, argc)
	}
	// 跳过可执行文件路径
	pathEnd := bytes.IndexByte(buf[4:], 0)
	if pathEnd < 0 {
		return nil, fmt.Errorf("%w: procargs2 has no end of the executable path", errProcArgsUnconfirmable)
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
			return nil, fmt.Errorf("%w: procargs2 holds only %d of %d arguments (truncated, or argc too large)", errProcArgsUnconfirmable, n, argc)
		}
		out = append(out, rest[:j]...)
		out = append(out, 0)
		rest = rest[j+1:]
	}
	return out, nil
}
