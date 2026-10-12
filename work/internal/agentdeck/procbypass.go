package agentdeck

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

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
