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
	"time"

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

// Proc 是进程链上的一个进程：pid 加上出生时间（/proc/<pid>/stat 第 22 列，开机后的时钟滴答数）。
// pid 会被复用，pid + 出生时间才唯一标识一个进程（n050 评审 B6）。
type Proc struct {
	PID   int
	Start uint64
}

// Resolve 等同于 ResolveAt(ctx, pid, time.Time{})：不检查出生时间与连接时间的先后。
func (r Resolver) Resolve(ctx context.Context, pid int) (Pane, error) {
	return r.ResolveAt(ctx, pid, time.Time{})
}

// ResolveAt 返回 pid 所在的窗格。进程已消失、读不了 /proc、列窗格失败、或判定过程中进程链变了，都返回错误，
// 调用方据此拒绝写入。connectedAt 非零时，调用进程必须在连接建立之前（含 1 秒误差）就已经出生：
// 连接之后才出生的，只可能是原进程退出后复用了它的 pid。
func (r Resolver) ResolveAt(ctx context.Context, pid int, connectedAt time.Time) (Pane, error) {
	chain, err := r.Ancestry(pid)
	if err != nil {
		return Pane{}, err
	}
	if !connectedAt.IsZero() {
		born, err := r.bornAt(chain[0].Start)
		if err != nil {
			return Pane{}, err
		}
		if born.After(connectedAt.Add(time.Second)) {
			return Pane{}, fmt.Errorf("pid %d 在连接建立之后才出生（%s > %s），疑似 pid 被复用", pid, born.Format(time.RFC3339Nano), connectedAt.Format(time.RFC3339Nano))
		}
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
	hit := -1
	for i, p := range chain {
		if _, ok := byPID[p.PID]; ok {
			hit = i
			break
		}
	}
	// 列完窗格后再读一次进程链：从调用者到命中的那一层，pid 与出生时间必须一模一样，
	// 否则说明判定过程中有进程退出、pid 被复用，结论不可信
	again, err := r.Ancestry(pid)
	if err != nil {
		return Pane{}, fmt.Errorf("判定过程中调用进程变了：%w", err)
	}
	upto := len(chain)
	if hit >= 0 {
		upto = hit + 1
	}
	if len(again) < upto {
		return Pane{}, fmt.Errorf("判定过程中进程链变了")
	}
	for i := 0; i < upto; i++ {
		if again[i] != chain[i] {
			return Pane{}, fmt.Errorf("判定过程中进程链变了（第 %d 层 %v → %v）", i, chain[i], again[i])
		}
	}
	if hit < 0 {
		return Pane{}, ErrUnknown
	}
	return byPID[chain[hit].PID], nil
}

// Ancestry 返回从 pid 起、沿父进程向上直到 1 的进程链（含 pid 本身，不含 1）。
// pid 本身读不到就是错误：对端已退出，不能把它当成「未知但无害」。
func (r Resolver) Ancestry(pid int) ([]Proc, error) {
	root := r.root()
	var chain []Proc
	seen := map[int]bool{}
	for cur := pid; cur > 1; {
		if seen[cur] {
			return nil, fmt.Errorf("进程链出现环（pid %d）", cur)
		}
		seen[cur] = true
		ppid, start, err := statOf(root, cur)
		if err != nil {
			if cur == pid {
				return nil, fmt.Errorf("读不到调用进程 %d：%w", pid, err)
			}
			break // 链上更早的进程恰好退出：到此为止（第二次读链时会发现差异）
		}
		chain = append(chain, Proc{PID: cur, Start: start})
		cur = ppid
	}
	return chain, nil
}

func (r Resolver) root() string {
	if r.ProcRoot == "" {
		return "/proc"
	}
	return r.ProcRoot
}

// clockTicks 是 USER_HZ。Linux 在 x86/arm 上恒为 100；不用 cgo 取 sysconf。
const clockTicks = 100

// bornAt 把出生滴答数换成墙钟时间：开机时间（/proc/stat 的 btime）+ 滴答数 / USER_HZ。
func (r Resolver) bornAt(start uint64) (time.Time, error) {
	data, err := os.ReadFile(filepath.Join(r.root(), "stat"))
	if err != nil {
		return time.Time{}, fmt.Errorf("读不到开机时间：%w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "btime "); ok {
			bt, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return time.Time{}, err
			}
			return time.Unix(bt, 0).Add(time.Duration(start) * time.Second / clockTicks), nil
		}
	}
	return time.Time{}, fmt.Errorf("/proc/stat 里没有 btime")
}

// statOf 返回父进程 pid 与出生时间。
func statOf(root string, pid int) (int, uint64, error) {
	data, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, 0, err
	}
	// 第 2 列 comm 带括号且可能含空格与括号，取最后一个 ')' 之后的字段：状态、ppid、……、第 22 列 starttime
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, 0, fmt.Errorf("/proc/%d/stat 格式不认识", pid)
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return 0, 0, fmt.Errorf("/proc/%d/stat 字段不足", pid)
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return 0, 0, err
	}
	start, err := strconv.ParseUint(f[19], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	return ppid, start, nil
}
