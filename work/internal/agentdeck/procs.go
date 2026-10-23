package agentdeck

import (
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

// procArgv 返回一个进程的命令行（argv）。进程已经没了、读不到、或是内核线程（命令行为空）时第二个返回值为 false。
func procArgv(proc string, pid int) ([]string, bool) {
	var data []byte
	if proc == "" {
		var err error
		if data, err = nativeCmdline(pid); err != nil {
			return nil, false
		}
	} else {
		var err error
		if data, err = procCmdlineFile(proc, pid); err != nil {
			return nil, false
		}
	}
	if len(data) == 0 {
		return nil, false
	}
	return strings.Split(strings.TrimRight(i18n.Neutralize(string(data)), "\x00"), "\x00"), true
}

// procCmdlineFile 读 /proc 形状的目录里一个进程的 cmdline 文件：参数之间用 NUL 隔开。
func procCmdlineFile(proc string, pid int) ([]byte, error) {
	return os.ReadFile(filepath.Join(proc, strconv.Itoa(pid), "cmdline"))
}
