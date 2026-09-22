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

// probeVerdict 是一次探测的结论。
//
// 为什么不用 bool：三种「不算命中」的情形，用户该做的事完全不同——
// 没有这个会话就去看会话是不是结束了；身份对不上说明同名的是别人，
// 不能连；读不到身份则多半是那个 server 不响应，或者会话不是上游建的。
// 上一版把三者一律折叠成 false，界面只能笼统说一句「找不到」，
// 独立评审指出这一点。fail-closed 的判定不变，变的是说得清。
type probeVerdict int

const (
	probeNoSession       probeVerdict = iota // 这个 server 上没有这个名字（精确匹配）
	probeIdentityUnknown                     // 名字在，但读不到 AGENTDECK_INSTANCE_ID
	probeIdentityMismatch                    // 名字在，身份是别人
	probeMatch                               // 名字在，身份也对
)

// probe 是一个 socket 上的探测结果，带上读到的身份好让错误信息说得出来。
type probe struct {
	socket  string
	verdict probeVerdict
	gotID   string // 只在 probeIdentityMismatch 时有意义
}

// instanceIDOf 读一个 tmux 会话里的 AGENTDECK_INSTANCE_ID。
//
// 上游在建会话时把它写进 tmux 会话环境（internal/session/instance.go 的
// SetEnvironment），值就是上游的 session id。这是**权威身份**：
// 会话名可能撞、可能被前缀匹配蒙混，而这个 id 是一对一的。
//
// 读不到与读到空串是两回事，所以第二个返回值单独说：
// 上游老版本建的会话、探测超时、server 不答，都会落进「读不到」。
func instanceIDOf(ctx context.Context, socket, tmuxSession string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	args := []string{}
	if socket != "" && socket != "default" {
		args = append(args, "-L", socket)
	}
	args = append(args, "show-environment", "-t", exactTarget(tmuxSession), "AGENTDECK_INSTANCE_ID")
	out, err := exec.CommandContext(ctx, "tmux", args...).Output()
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(string(out))
	// 形如 AGENTDECK_INSTANCE_ID=<id>；变量不存在时 tmux 会输出 -AGENTDECK_INSTANCE_ID。
	_, value, ok := strings.Cut(line, "=")
	if !ok || strings.HasPrefix(line, "-") {
		return "", false
	}
	return value, true
}

// probeSocket 看某个 socket 上有没有这个会话，且它就是我们要的那一个。
//
// 两道：名字精确匹配，再加身份比对。wantInstanceID 为空时只比名字——
// 那是调用方明确表示「这个会话不是上游派的，没有 id 可比」的情况。
func probeSocket(ctx context.Context, socket, tmuxSession, wantInstanceID string) probe {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	args := []string{"-L", socket, "has-session", "-t", exactTarget(tmuxSession)}
	if err := exec.CommandContext(probeCtx, "tmux", args...).Run(); err != nil {
		return probe{socket: socket, verdict: probeNoSession}
	}
	if wantInstanceID == "" {
		return probe{socket: socket, verdict: probeMatch}
	}
	got, ok := instanceIDOf(ctx, socket, tmuxSession)
	switch {
	case !ok:
		return probe{socket: socket, verdict: probeIdentityUnknown}
	case got == wantInstanceID:
		return probe{socket: socket, verdict: probeMatch}
	default:
		return probe{socket: socket, verdict: probeIdentityMismatch, gotID: got}
	}
}

// scanSockets 扫本机每一个 tmux server，把每个的探测结论都带回来。
//
// 死掉的 socket 文件会留在目录里，对它们探测会失败并落进 probeNoSession；
// 僵死但仍在监听的 server 由 probeTimeout 兜着，不会拖住调用方。
//
// 不命中的也要带回来：拒绝的时候得说得出为什么。
func scanSockets(ctx context.Context, tmuxSession, wantInstanceID string) ([]probe, error) {
	dir := tmuxSocketDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读不到 tmux socket 目录 %s：%w", dir, err)
	}
	probes := make([]probe, 0, len(entries))
	for _, e := range entries {
		probes = append(probes, probeSocket(ctx, e.Name(), tmuxSession, wantInstanceID))
	}
	sort.Slice(probes, func(i, j int) bool { return probes[i].socket < probes[j].socket })
	return probes, nil
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
	probes, err := scanSockets(ctx, tmuxSession, wantInstanceID)
	if err != nil {
		return "", err
	}
	var matched, mismatched, unknown []probe
	for _, p := range probes {
		switch p.verdict {
		case probeMatch:
			matched = append(matched, p)
		case probeIdentityMismatch:
			mismatched = append(mismatched, p)
		case probeIdentityUnknown:
			unknown = append(unknown, p)
		}
	}

	switch len(matched) {
	case 1:
		if matched[0].socket == "default" {
			return "", nil
		}
		return matched[0].socket, nil
	case 0:
		return "", noMatchError(tmuxSession, wantInstanceID, mismatched, unknown)
	default:
		sockets := make([]string, 0, len(matched))
		for _, p := range matched {
			sockets = append(sockets, p.socket)
		}
		return "", &ErrAmbiguousTmuxTarget{Session: tmuxSession, Sockets: sockets}
	}
}

// noMatchError 说清楚「一个都没对上」到底是哪一种。
//
// 三种情形的下一步不一样，所以不能都说「找不到」：
// 同名但身份是别人 → 千万别连，那是另一个会话；
// 读不到身份 → 多半是 server 不答，或者会话不是上游建的（老版本没写这个变量）；
// 什么都没有 → 会话多半已经结束了。
func noMatchError(tmuxSession, wantInstanceID string, mismatched, unknown []probe) error {
	if len(mismatched) > 0 {
		var parts []string
		for _, p := range mismatched {
			parts = append(parts, fmt.Sprintf("%s 上那个的身份是 %s", p.socket, p.gotID))
		}
		return fmt.Errorf(
			"有叫 %s 的 tmux 会话，但身份都不是 %s（%s）——同名的是别人，不能连",
			tmuxSession, wantInstanceID, strings.Join(parts, "；"))
	}
	if len(unknown) > 0 {
		var sockets []string
		for _, p := range unknown {
			sockets = append(sockets, p.socket)
		}
		return fmt.Errorf(
			"有叫 %s 的 tmux 会话（在 %v 上），但读不到它的 AGENTDECK_INSTANCE_ID，"+
				"证明不了就是 %s：可能那个 tmux server 没响应，也可能这个会话不是上游建的。"+
				"宁可不连",
			tmuxSession, sockets, wantInstanceID)
	}
	if wantInstanceID != "" {
		return fmt.Errorf(
			"本机没有任何 tmux server 上有会话 %s 且身份对得上 %s：它可能已经结束了",
			tmuxSession, wantInstanceID)
	}
	return fmt.Errorf("本机没有任何 tmux server 上有会话 %s，它可能已经结束了", tmuxSession)
}
