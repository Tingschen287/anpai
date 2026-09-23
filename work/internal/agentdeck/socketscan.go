package agentdeck

import (
	"context"
	"errors"
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
// 为什么不用 bool：几种「不算命中」的情形，用户该做的事完全不同——
// 没有这个会话就去看会话是不是结束了；身份对不上说明同名的是别人，不能连；
// 读不到身份多半是那个 server 不响应；而**探测本身失败**（tmux 不在、
// 超时、权限不足）根本没查出任何事实，不能当成「会话不存在」来报。
//
// 上一版先是把三种折叠成 bool，改完又把「探测失败」并进了 probeNoSession：
// 所有 socket 都探测失败时，界面照样说「它可能已经结束了」——
// 那是一句没有依据的断言。独立评审两轮都点在这里。
//
// 判定仍然 fail-closed：除 probeMatch 外一概不放行。变的只是说得清。
type probeVerdict int

const (
	probeNoSession        probeVerdict = iota // tmux 明确答「没有这个会话」
	probeDeadSocket                           // socket 文件还在，server 已经没了
	probeUnavailable                          // 探测本身失败，什么都没查出来
	probeIdentityUnknown                      // 名字在，但读不到 AGENTDECK_INSTANCE_ID
	probeIdentityMismatch                     // 名字在，身份是别人
	probeMatch                                // 名字在，身份也对
)

// probe 是一个 socket 上的探测结果。
//
// why 只在 probeUnavailable 时有意义：拒绝时要把原因原样带给人，
// 「探测失败」本身不构成任何关于会话的结论。
type probe struct {
	socket  string
	verdict probeVerdict
	gotID   string // 只在 probeIdentityMismatch 时有意义
	why     string // 只在 probeUnavailable 时有意义
}

// classifyHasSession 把 `has-session` 的失败分门别类。
//
// tmux 用退出码 1 加一句 stderr 回答「没有这个会话」；死掉的 socket 会说
// "no server running"。除此之外的失败——二进制不在、context 到点、权限不足、
// server 故障——**都没有回答我们的问题**，不能算作「会话不存在」。
func classifyHasSession(socket string, ctxErr error, err error, stderr string) probe {
	if ctxErr != nil {
		return probe{socket: socket, verdict: probeUnavailable,
			why: fmt.Sprintf("探测超时或被取消：%v", ctxErr)}
	}
	if errors.Is(err, exec.ErrNotFound) {
		return probe{socket: socket, verdict: probeUnavailable, why: "本机找不到 tmux 可执行文件"}
	}
	msg := strings.ToLower(stderr)
	switch {
	case strings.Contains(msg, "no server running"):
		return probe{socket: socket, verdict: probeDeadSocket}
	case strings.Contains(msg, "can't find session"),
		strings.Contains(msg, "no such session"),
		strings.Contains(msg, "session not found"):
		return probe{socket: socket, verdict: probeNoSession}
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && stderr == "" {
		// tmux 以非零码退出却什么都没说。它对「没有这个会话」一向是会说话的，
		// 所以这里不替它断言，只如实记下。
		return probe{socket: socket, verdict: probeUnavailable,
			why: fmt.Sprintf("tmux 退出码 %d，没有给出原因", exitErr.ExitCode())}
	}
	return probe{socket: socket, verdict: probeUnavailable,
		why: fmt.Sprintf("探测失败：%v", err)}
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
	cmd := exec.CommandContext(probeCtx, "tmux", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return classifyHasSession(socket, probeCtx.Err(), err, strings.TrimSpace(stderr.String()))
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
//	恰好一个 server 上名字和身份都对上  → 目标唯一，把 socket 名带回去
//	一个都没对上                        → 拒绝，原因分四种，见 noMatchError
//	多个 server 上都对上                → 说不清是哪个，拒绝
//
// 「一个都没对上」不等于会话不在了：可能同名的是别人、读不到身份、或者探测失败。
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
	var matched, mismatched, unknown, unavailable []probe
	for _, p := range probes {
		switch p.verdict {
		case probeMatch:
			matched = append(matched, p)
		case probeIdentityMismatch:
			mismatched = append(mismatched, p)
		case probeIdentityUnknown:
			unknown = append(unknown, p)
		case probeUnavailable:
			unavailable = append(unavailable, p)
		}
	}

	switch len(matched) {
	case 1:
		if matched[0].socket == "default" {
			return "", nil
		}
		return matched[0].socket, nil
	case 0:
		return "", noMatchError(tmuxSession, wantInstanceID, mismatched, unknown, unavailable)
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
// 几种情形的下一步不一样，所以不能都说「找不到」：
// 同名但身份是别人 → 千万别连，那是另一个会话；
// 读不到身份 → 多半是 server 不答，或者会话不是上游建的（老版本没写这个变量）；
// 探测失败 → **什么都没查出来**，不能说会话不在；
// 以上都不是 → tmux 明确答过「没有这个会话」，那它多半真的结束了。
//
// 顺序是有讲究的：越具体、越该阻止人做错事的，越先说。
func noMatchError(tmuxSession, wantInstanceID string, mismatched, unknown, unavailable []probe) error {
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
	if len(unavailable) > 0 {
		var parts []string
		for _, p := range unavailable {
			parts = append(parts, fmt.Sprintf("%s（%s）", p.socket, p.why))
		}
		// 注意措辞：**没查出来**，不是「不在」。
		// 说成「会话已结束」会让人去查一个根本没发生的问题。
		return fmt.Errorf(
			"有 %d 个 tmux server 探测失败，查不出会话 %s 在不在：%s。"+
				"先把探测本身修好再说",
			len(unavailable), tmuxSession, strings.Join(parts, "；"))
	}
	if wantInstanceID != "" {
		return fmt.Errorf(
			"本机没有任何 tmux server 上有会话 %s 且身份对得上 %s：它可能已经结束了",
			tmuxSession, wantInstanceID)
	}
	return fmt.Errorf("本机没有任何 tmux server 上有会话 %s，它可能已经结束了", tmuxSession)
}
