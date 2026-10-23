package agentdeck

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 进程表的读取（n067）。
//
// 校验启动参数（CheckSessionArgs）和找 notify-daemon（NotifyDaemonRunning）都要「列出进程、读命令行、建父子关系」。
// Linux 读 /proc；macOS 没有 /proc，读 sysctl（kern.proc.all 列进程与父进程，kern.procargs2 取命令行，procs_darwin.go）。
// proc 参数为空表示用本平台的进程表，非空表示读那个 /proc 形状的目录（测试里造假目录用，两个平台都能跑）。
//
// 命令行是外部原文：进来这里就过一遍 Neutralize（返工 4）。

// errProcArgsUnconfirmable：读到了进程的命令行数据，但确认不了它是完整可信的（macOS 上：缓冲区截断、读不足 argc 个参数、
// 版式不对、运行时自检没过）。和「读不到」（进程已退出、没权限）是两回事：后者跳过，前者让 CheckSessionArgs 拒绝，
// 不拿半截命令行去核对。Linux 的 /proc/<pid>/cmdline 不产生这个错误。
var errProcArgsUnconfirmable = errors.New("process arguments cannot be confirmed")

// errProcArgsSelfCheck：确认不了的原因是运行时自检没过，对每个进程都一样（macOS，procs_darwin.go）。
var errProcArgsSelfCheck = errors.New("procargs2 self check failed")

// procTableName 是进程表的名字，写进错误里给人看。
func procTableName(proc string) string {
	if proc == "" {
		return nativeProcTableName
	}
	return proc
}

// procPIDs 列出进程表里的全部 pid。
func procPIDs(proc string) ([]int, error) {
	if proc == "" {
		return nativePIDs()
	}
	entries, err := os.ReadDir(proc)
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// procArgv 返回一个进程的命令行（argv）。进程已经没了、读不到、命令行确认不了、或是内核线程（命令行为空）时第二个返回值为 false。
// 需要区分「读不到」和「确认不了」的调用方（CheckSessionArgs）用 procArgvChecked。
func procArgv(proc string, pid int) ([]string, bool) {
	argv, ok, _ := procArgvChecked(proc, pid)
	return argv, ok
}

// procArgvChecked 同 procArgv，多一个错误：只在命令行数据读到了但确认不了时非 nil（errors.Is errProcArgsUnconfirmable），
// 此时调用方不能当作「没有这个进程」。
func procArgvChecked(proc string, pid int) ([]string, bool, error) {
	var data []byte
	var err error
	if proc == "" {
		data, err = nativeCmdline(pid)
	} else {
		data, err = procCmdlineFile(proc, pid)
	}
	if err != nil {
		if errors.Is(err, errProcArgsUnconfirmable) {
			return nil, false, err
		}
		return nil, false, nil
	}
	if len(data) == 0 {
		return nil, false, nil
	}
	return strings.Split(strings.TrimRight(i18n.Neutralize(string(data)), "\x00"), "\x00"), true, nil
}

// procCmdlineFile 读 /proc 形状的目录里一个进程的 cmdline 文件：参数之间用 NUL 隔开。
func procCmdlineFile(proc string, pid int) ([]byte, error) {
	return os.ReadFile(filepath.Join(proc, strconv.Itoa(pid), "cmdline"))
}
