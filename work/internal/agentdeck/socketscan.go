package agentdeck

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

// ErrAmbiguousTmuxTarget 表示同一个会话名出现在多个 tmux server 上。
//
// 这时候不许连。tmux 的不同 server 各有各的命名空间，同名会话完全可能是
// 毫不相干的两个东西；不带 -L 去连，连上的是默认 server 上那个，而不是目标。
// 「通常不会同名」不是一个可以拿来赌的前提。
type ErrAmbiguousTmuxTarget struct {
	Session string
	Sockets []string
}

func (e *ErrAmbiguousTmuxTarget) Error() string {
	return fmt.Sprintf(
		"会话名 %s 同时存在于 %d 个 tmux server 上（%v），说不清该连哪一个，拒绝直连",
		e.Session, len(e.Sockets), e.Sockets)
}

// tmuxSocketDir 是本机 tmux socket 的存放目录。
//
// tmux 的约定：$TMUX_TMPDIR 优先，否则 /tmp/tmux-<uid>。
func tmuxSocketDir() string {
	if d := os.Getenv("TMUX_TMPDIR"); d != "" {
		return filepath.Join(d, fmt.Sprintf("tmux-%d", os.Getuid()))
	}
	return filepath.Join("/tmp", fmt.Sprintf("tmux-%d", os.Getuid()))
}

// socketsHosting 扫本机每一个 tmux server，找出哪些上面有这个会话名。
//
// 死掉的 socket 文件会留在目录里，对它们 has-session 会报 "no server running"
// 并以非零退出码返回，自然被跳过——所以这里不需要额外判活。
func socketsHosting(ctx context.Context, tmuxSession string) ([]string, error) {
	dir := tmuxSocketDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读不到 tmux socket 目录 %s：%w", dir, err)
	}
	var hosting []string
	for _, e := range entries {
		name := e.Name()
		cmd := exec.CommandContext(ctx, "tmux", "-L", name, "has-session", "-t", tmuxSession)
		if err := cmd.Run(); err == nil {
			hosting = append(hosting, name)
		}
	}
	sort.Strings(hosting)
	return hosting, nil
}

// ResolveSocketFor 在拿不到 socket 名时，靠扫描证明目标唯一并把 socket 名找回来。
//
// 这是降级路径唯一可以接受的做法。`list --json` 给得出 tmux 会话名却给不出
// socket 名，而不带 -L 直接连，连上的是默认 server 上的同名会话——
// 那可能是完全不相干的另一个会话。**连错比连不上严重得多。**
//
// 三种结果，只有一种放行：
//
//	恰好一个 server 上有它  → 目标唯一，把 socket 名带回去
//	一个都没有              → 会话不在了，拒绝
//	多个 server 上都有      → 说不清是哪个，拒绝（ErrAmbiguousTmuxTarget）
//
// 返回的 socket 名为 "default" 时给出空串：那就是默认 server，
// 带不带 -L 都指向同一个，空串沿用本包一贯的「空即默认」约定。
func ResolveSocketFor(ctx context.Context, tmuxSession string) (string, error) {
	if tmuxSession == "" {
		return "", fmt.Errorf("没有 tmux 会话名，无从扫描")
	}
	hosting, err := socketsHosting(ctx, tmuxSession)
	if err != nil {
		return "", err
	}
	switch len(hosting) {
	case 0:
		return "", fmt.Errorf("本机没有任何 tmux server 上有会话 %s，它可能已经结束了", tmuxSession)
	case 1:
		if hosting[0] == "default" {
			return "", nil
		}
		return hosting[0], nil
	default:
		return "", &ErrAmbiguousTmuxTarget{Session: tmuxSession, Sockets: hosting}
	}
}
