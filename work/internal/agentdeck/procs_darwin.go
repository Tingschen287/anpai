package agentdeck

import (
	"bytes"
	"encoding/binary"
	"errors"

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

// parseProcargs2 解 kern.procargs2 的返回：int32 argc，可执行文件路径（NUL 结尾），若干 NUL 填充，
// 然后是 argc 个以 NUL 结尾的参数，最后才是环境变量。只取参数。
func parseProcargs2(buf []byte) ([]byte, error) {
	if len(buf) < 4 {
		return nil, errors.New("procargs2 too short")
	}
	argc := int(int32(binary.LittleEndian.Uint32(buf[:4])))
	rest := buf[4:]
	// 跳过可执行文件路径
	i := bytes.IndexByte(rest, 0)
	if i < 0 {
		return nil, errors.New("procargs2 has no end of the executable path")
	}
	rest = rest[i:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	var out []byte
	for n := 0; n < argc && len(rest) > 0; n++ {
		j := bytes.IndexByte(rest, 0)
		if j < 0 {
			j = len(rest)
		}
		out = append(out, rest[:j]...)
		out = append(out, 0)
		if j >= len(rest) {
			break
		}
		rest = rest[j+1:]
	}
	return out, nil
}
