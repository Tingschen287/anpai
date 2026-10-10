// Package identity 判定一个 unix socket 连接来自哪个 tmux 窗格（n050，架构稿 §3）。
//
// 做法：取 SO_PEERCRED 拿到对方 pid，沿 /proc 父进程链向上，匹配各 tmux server 的窗格进程。窗格用
// 「server socket 路径 + pane id」标识——pane id 只在同一 server 内唯一。
//
// 这条判定防的是模型越权与误操作：子 agent 冒充 owner、用 setsid 脱离进程链（落到「未知」）。
// 同一系统用户下它防不住恶意代码（直接操作 tmux、ptrace），见架构稿 O10。
package identity

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
)

// Pane 是某个 tmux server 上的一个窗格。
type Pane struct {
	Server  string // server 的 socket 路径
	PaneID  string // 例如 %3，只在同一 server 内唯一
	PanePID int    // 窗格里第一个进程的 pid
	Session string // tmux session 名
}

// Key 是窗格的全局唯一键。
func (p Pane) Key() string { return p.Server + "|" + p.PaneID }

// PaneSource 列出一个 tmux server 上的全部窗格。
type PaneSource interface {
	Panes(ctx context.Context) ([]Pane, error)
}

// TmuxSource 是一个真实 tmux server。
type TmuxSource struct {
	Socket string // tmux -S 的 socket 路径
}

// Panes 列出这个 server 的全部窗格（经 agentdeck adapter 调 tmux）。server 没在跑时返回空列表，不算错。
func (s TmuxSource) Panes(ctx context.Context) ([]Pane, error) {
	list, err := agentdeck.ListTmuxPanes(ctx, s.Socket)
	if err != nil {
		return nil, err
	}
	panes := make([]Pane, 0, len(list))
	for _, p := range list {
		panes = append(panes, Pane{Server: s.Socket, PaneID: p.PaneID, PanePID: p.PanePID, Session: p.Session})
	}
	return panes, nil
}

// PeerPID 取 unix socket 连接对端进程的 pid。
func PeerPID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *syscall.Ucred
	var serr error
	if err := raw.Control(func(fd uintptr) {
		cred, serr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if serr != nil {
		return 0, serr
	}
	return int(cred.Pid), nil
}

// Resolver 把一个 pid 对应到它所在的窗格。
type Resolver struct {
	Sources  []PaneSource
	ProcRoot string // 默认 /proc；测试可换
}

// ErrUnknown 表示进程链上没有任何已知窗格。
var ErrUnknown = errors.New("调用者不在任何已知的 tmux 窗格里")

// Resolve 返回 pid 所在的窗格。进程已消失、读不了 /proc、列窗格失败都返回错误，调用方据此拒绝写入。
func (r Resolver) Resolve(ctx context.Context, pid int) (Pane, error) {
	chain, err := r.Ancestry(pid)
	if err != nil {
		return Pane{}, err
	}
	byPID := map[int]Pane{}
	for _, src := range r.Sources {
		panes, err := src.Panes(ctx)
		if err != nil {
			return Pane{}, err
		}
		for _, p := range panes {
			if prev, dup := byPID[p.PanePID]; dup && prev.Key() != p.Key() {
				return Pane{}, fmt.Errorf("pid %d 同时是两个窗格的进程（%s、%s），无法判定", p.PanePID, prev.Key(), p.Key())
			}
			byPID[p.PanePID] = p
		}
	}
	for _, p := range chain {
		if pane, ok := byPID[p]; ok {
			return pane, nil
		}
	}
	return Pane{}, ErrUnknown
}

// Ancestry 返回从 pid 起、沿父进程向上直到 1 的 pid 链（含 pid 本身，不含 1）。
// pid 本身读不到就是错误：对端已退出，不能把它当成「未知但无害」。
func (r Resolver) Ancestry(pid int) ([]int, error) {
	root := r.ProcRoot
	if root == "" {
		root = "/proc"
	}
	var chain []int
	seen := map[int]bool{}
	for cur := pid; cur > 1; {
		if seen[cur] {
			return nil, fmt.Errorf("进程链出现环（pid %d）", cur)
		}
		seen[cur] = true
		ppid, err := parentOf(root, cur)
		if err != nil {
			if cur == pid {
				return nil, fmt.Errorf("读不到调用进程 %d：%w", pid, err)
			}
			break // 链上更早的进程恰好退出：到此为止
		}
		chain = append(chain, cur)
		cur = ppid
	}
	return chain, nil
}

func parentOf(root string, pid int) (int, error) {
	data, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}
	// 第 2 列 comm 带括号且可能含空格与括号，取最后一个 ')' 之后的字段。
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, fmt.Errorf("/proc/%d/stat 格式不认识", pid)
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0, fmt.Errorf("/proc/%d/stat 字段不足", pid)
	}
	return strconv.Atoi(f[1])
}
