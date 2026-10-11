package agentdeck

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CheckSessionNoBypass 按实际在跑的命令行再核一次（n051 真模型端到端实测：上游可能凭自己的配置给 CLI 加跳过权限的参数，
// deck 交出去的启动参数再干净也看不到）。取 tmux 会话里每个窗格的进程及其全部子孙，读 /proc/<pid>/cmdline，
// 任何一个带绕过参数就报错；会话里没有窗格（核对不了）也报错。
func CheckSessionNoBypass(ctx context.Context, socket, tmuxSession string) error {
	if tmuxSession == "" {
		return fmt.Errorf("不知道会话在哪个 tmux 会话里，核对不了它的命令行")
	}
	panes, err := ListTmuxPanes(ctx, socket)
	if err != nil {
		return err
	}
	var queue []int
	for _, p := range panes {
		if p.Session == tmuxSession && p.PanePID > 0 {
			queue = append(queue, p.PanePID)
		}
	}
	if len(queue) == 0 {
		return fmt.Errorf("tmux 会话 %s 里没有在跑的窗格，核对不了它的命令行", tmuxSession)
	}
	children := procChildren("/proc")
	seen := map[int]bool{}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		if err := cmdlineNoBypass("/proc", pid); err != nil {
			return fmt.Errorf("tmux 会话 %s 的进程 %d %w", tmuxSession, pid, err)
		}
		queue = append(queue, children[pid]...)
	}
	return nil
}

// TmuxSessionAlive 看 socket 对应的 tmux server 上还有没有这个会话的窗格（server 没在跑算没有）。
func TmuxSessionAlive(ctx context.Context, socket, tmuxSession string) (bool, error) {
	panes, err := ListTmuxPanes(ctx, socket)
	if err != nil {
		return false, err
	}
	for _, p := range panes {
		if p.Session == tmuxSession {
			return true, nil
		}
	}
	return false, nil
}

// cmdlineNoBypass 读一个进程的命令行并检查。进程已经退出（读不到）不算错：它不再运行任何东西。
func cmdlineNoBypass(proc string, pid int) error {
	data, err := os.ReadFile(filepath.Join(proc, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return nil
	}
	args := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	if err := CheckNoBypass(args...); err != nil {
		return fmt.Errorf("的命令行带着绕过参数：%w", err)
	}
	return nil
}

// procChildren 从 /proc/*/stat 建父进程 → 子进程表。
func procChildren(proc string) map[int][]int {
	out := map[int][]int{}
	entries, _ := os.ReadDir(proc)
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(proc, e.Name(), "stat"))
		if err != nil {
			continue
		}
		// 格式：pid (comm) state ppid …；comm 里可能有空格与括号，从最后一个 ')' 之后取
		i := bytes.LastIndexByte(data, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(string(data[i+1:]))
		if len(f) < 2 {
			continue
		}
		if ppid, err := strconv.Atoi(f[1]); err == nil {
			out[ppid] = append(out[ppid], pid)
		}
	}
	return out
}
