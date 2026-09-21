package agentdeck

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// probeTimeout 是单个 tmux server 探测的上限。
//
// 卡住一个不响应的 server 就不该拖住整个界面：按 T 是同步动作，
// 探测跑在用户按键和终端交出去之间。上一版用的是不带超时的 context，
// 一个僵死的 socket 足以让 Work Deck 整个停住，独立评审指出了这一点。
const probeTimeout = 2 * time.Second

// exactTarget 是 tmux 的精确目标形式。
//
// **`-t name` 是前缀匹配。** 只有 `name-suffix` 存在时，`-t name` 照样命中，
// 随后 attach 进去的就是那个后缀会话。加一个等号才是精确匹配：
//
//	tmux -L s has-session -t target           → exit 0（只有 target-suffix 时也命中）
//	tmux -L s has-session -t '=target'        → exit 1
//	tmux -L s has-session -t '=target-suffix' → exit 0
//
// 上一版扫描没带等号，于是「目标已结束、只剩一个同前缀会话」时会判成唯一，
// 然后连错。实测复现过。
func exactTarget(session string) string { return "=" + session }

// ErrAmbiguousTmuxTarget 表示同一个会话在多个 tmux server 上都对得上。
type ErrAmbiguousTmuxTarget struct {
	Session string
	Sockets []string
}

func (e *ErrAmbiguousTmuxTarget) Error() string {
	return fmt.Sprintf(
		"会话 %s 同时存在于 %d 个 tmux server 上（%v），说不清该连哪一个，拒绝直连",
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

// instanceIDOf 读一个 tmux 会话里的 AGENTDECK_INSTANCE_ID。
//
// 上游在建会话时把它写进 tmux 会话环境（internal/session/instance.go 的
// SetEnvironment），值就是上游的 session id。这是**权威身份**：
// 会话名可能撞、可能被前缀匹配蒙混，而这个 id 是一对一的。
//
// 取不到就返回空串，由调用方决定怎么办——这里不猜。
func instanceIDOf(ctx context.Context, socket, tmuxSession string) string {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	args := []string{}
	if socket != "" && socket != "default" {
		args = append(args, "-L", socket)
	}
	args = append(args, "show-environment", "-t", exactTarget(tmuxSession), "AGENTDECK_INSTANCE_ID")
	out, err := exec.CommandContext(ctx, "tmux", args...).Output()
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(out))
	// 形如 AGENTDECK_INSTANCE_ID=<id>；变量不存在时 tmux 会输出 -AGENTDECK_INSTANCE_ID。
	_, value, ok := strings.Cut(line, "=")
	if !ok || strings.HasPrefix(line, "-") {
		return ""
	}
	return value
}

// hostsSession 判断某个 socket 上有没有这个会话，且它就是我们要的那一个。
//
// 两道：名字精确匹配，再加身份比对。wantInstanceID 为空时只比名字——
// 那是调用方明确表示「这个会话不是上游派的，没有 id 可比」的情况。
func hostsSession(ctx context.Context, socket, tmuxSession, wantInstanceID string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	args := []string{"-L", socket, "has-session", "-t", exactTarget(tmuxSession)}
	if err := exec.CommandContext(probeCtx, "tmux", args...).Run(); err != nil {
		return false
	}
	if wantInstanceID == "" {
		return true
	}
	return instanceIDOf(ctx, socket, tmuxSession) == wantInstanceID
}

// socketsHosting 扫本机每一个 tmux server，找出哪些上面有这个会话。
//
// 死掉的 socket 文件会留在目录里，对它们探测会失败并被跳过；
// 僵死但仍在监听的 server 由 probeTimeout 兜着，不会拖住调用方。
func socketsHosting(ctx context.Context, tmuxSession, wantInstanceID string) ([]string, error) {
	dir := tmuxSocketDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读不到 tmux socket 目录 %s：%w", dir, err)
	}
	var hosting []string
	for _, e := range entries {
		if hostsSession(ctx, e.Name(), tmuxSession, wantInstanceID) {
			hosting = append(hosting, e.Name())
		}
	}
	sort.Strings(hosting)
	return hosting, nil
}

// ResolveSocketFor 在拿不到 socket 名时，证明目标唯一并把 socket 名找回来。
//
// wantInstanceID 是上游的 session id。会话名只当查找键用，**认不认这个会话
// 由身份说了算**——名字可能撞，也可能被 tmux 的前缀匹配蒙混过去。
//
// 三种结果，只有一种放行：
//
//	恰好一个 server 上有它  → 目标唯一，把 socket 名带回去
//	一个都没有              → 会话不在了（或身份对不上），拒绝
//	多个 server 上都有      → 说不清是哪个，拒绝
//
// 返回的 socket 名为 "default" 时给出空串：那就是默认 server，
// 空串沿用本包一贯的「空即默认」约定。
func ResolveSocketFor(ctx context.Context, tmuxSession, wantInstanceID string) (string, error) {
	if tmuxSession == "" {
		return "", fmt.Errorf("没有 tmux 会话名，无从扫描")
	}
	hosting, err := socketsHosting(ctx, tmuxSession, wantInstanceID)
	if err != nil {
		return "", err
	}
	switch len(hosting) {
	case 0:
		if wantInstanceID != "" {
			return "", fmt.Errorf(
				"本机没有任何 tmux server 上有会话 %s 且身份对得上 %s："+
					"它可能已经结束，也可能同名的是另一个会话",
				tmuxSession, wantInstanceID)
		}
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
