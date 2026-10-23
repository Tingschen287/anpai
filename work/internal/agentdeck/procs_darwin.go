package agentdeck

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

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

// nativeCmdline 取一个进程的命令行，格式同 /proc/<pid>/cmdline：参数之间用 NUL 隔开。
// 读别的用户的进程会被拒绝（EINVAL/EPERM）：当作读不到，调用方跳过。
func nativeCmdline(pid int) ([]byte, error) {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, err
	}
	return parseProcargs2(buf)
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
// 然后是 argc 个以 NUL 结尾的参数，最后才是环境变量。只取参数，环境变量一个字节都不带出来。
//
// 难点是 argv[0] 为空（或前几个参数为空）：空参数就是一个孤零零的 NUL，和填充分不开。把所有 NUL 当填充跳过，
// 会吞掉空参数，于是 argc 个字符串多读走的是环境变量（审查 1 的反例：环境变量进了命令行）。
// 做法：填充的长度由对齐决定（见 procargsAlign），超出对齐位置的那些 NUL 才是空参数；
// 之后严格按 NUL 切，不再跳过空串；最多读 argc 个，不够就停（缓冲区被截断时也不会读进环境变量）。
func parseProcargs2(buf []byte) ([]byte, error) {
	if len(buf) < 4 {
		return nil, errors.New("procargs2 too short")
	}
	argc := int(int32(binary.LittleEndian.Uint32(buf[:4])))
	if argc < 0 || argc > maxProcArgc {
		return nil, fmt.Errorf("procargs2 argc %d out of range", argc)
	}
	// 跳过可执行文件路径
	pathEnd := bytes.IndexByte(buf[4:], 0)
	if pathEnd < 0 {
		return nil, errors.New("procargs2 has no end of the executable path")
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
	for n := empties; n < argc && len(rest) > 0; n++ {
		j := bytes.IndexByte(rest, 0)
		if j < 0 {
			return nil, errors.New("procargs2 is truncated in the middle of an argument")
		}
		out = append(out, rest[:j]...)
		out = append(out, 0)
		rest = rest[j+1:]
	}
	return out, nil
}
