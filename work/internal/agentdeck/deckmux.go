package agentdeck

// 「安排 Anpai」v1 的 tmux 操作（n049，架构稿 §1、§2、§7）。
//
// 两台 server：
//   - 布局 server（deck 自己的，socket 默认 $TMUX_TMPDIR/tmux-UID/deck）：拼左/中/右/下布局、显隐面板、选中窗格、
//     新开设置窗口。
//   - agent-deck 的 server：只对**中间窗格那一个嵌套 client** 做三件事——切只读、切会话、读状态；外加建空白占位会话
//     deck-idle 与抓取会话画面（交还摘要用）。不往任何会话里敲键（本包的禁令见 TestAdapterMayAttachButNeverSendsKeysOrKills）。
//
// 外部命令只在 adapter 包里调（TestEachAdapterOnlyTouchesItsOwnUpstream），参数只由常量与入参构成
// （TestCommandArgumentsAreBuiltFromConstantsOnly）。所以这里不写「传一串参数进来」的通用封装：
// 每个操作在自己的函数里拼好命令，只把执行这一步抽出来。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// muxTimeout 是一条 tmux 命令的上限。布局与中间 client 的操作都是瞬时的，卡住就当失败。
const muxTimeout = 5 * time.Second

// runMux 执行一条已拼好的 tmux 命令：不继承外层的 TMUX，失败时带上 stderr。
func runMux(cmd *exec.Cmd) ([]byte, error) {
	cmd.Env = withoutTmux(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out, fmt.Errorf("%w：%s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return out, err
	}
	return out, nil
}

// ErrNoServer 表示 socket 上没有 tmux server 在跑。
var ErrNoServer = errors.New("tmux server 没有在跑")

func noServer(socket string, err error) bool {
	if _, statErr := os.Stat(socket); errors.Is(statErr, os.ErrNotExist) {
		return true
	}
	return err != nil && (strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "error connecting"))
}

// ---------------- 布局 server ----------------

// LayoutHasSession 说明布局 server 上有没有这个会话。
func LayoutHasSession(ctx context.Context, socket, session string) bool {
	ctx, cancel := context.WithTimeout(ctx, muxTimeout)
	defer cancel()
	_, err := runMux(exec.CommandContext(ctx, "tmux", "-S", socket, "has-session", "-t", exactTarget(session)))
	return err == nil
}

// LayoutNewSession 用配置文件 conf 起布局 server 并建会话，第一个窗格跑 command，返回它的 pane id。
func LayoutNewSession(ctx context.Context, socket, conf, session, window, width, height, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, muxTimeout)
	defer cancel()
	out, err := runMux(exec.CommandContext(ctx, "tmux", "-S", socket, "-f", conf, "new-session", "-d", "-P", "-F", "#{pane_id}",
		"-s", session, "-n", window, "-x", width, "-y", height, command))
	if err != nil {
		return "", fmt.Errorf("起布局 server 失败：%w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// SplitSpec 是切一个新窗格的方式。
type SplitSpec struct {
	Horizontal bool   // true：左右切（-h）；false：上下切（-v）
	Before     bool   // 新窗格放在目标的左边或上边（-b）
	Full       bool   // 横跨整个窗口（-f），用于下栏、整列的左栏
	Size       string // 新窗格的宽或高，例如 "32" 或 "25%"
}

// LayoutSplit 在 target 窗格旁切出新窗格跑 command，返回新 pane id。
func LayoutSplit(ctx context.Context, socket, target string, spec SplitSpec, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, muxTimeout)
	defer cancel()
	args := []string{"-S", socket, "split-window", "-d", "-P", "-F", "#{pane_id}"}
	if spec.Horizontal {
		args = append(args, "-h")
	} else {
		args = append(args, "-v")
	}
	if spec.Before {
		args = append(args, "-b")
	}
	if spec.Full {
		args = append(args, "-f")
	}
	args = append(args, "-l", spec.Size, "-t", target, command)
	out, err := runMux(exec.CommandContext(ctx, "tmux", args...))
	if err != nil {
		return "", fmt.Errorf("切窗格失败：%w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// LayoutSetRole 给窗格打上 @deck_role 标记（center、sidebar、tabs、panel:<名字>）。
func LayoutSetRole(ctx context.Context, socket, pane, role string) error {
	ctx, cancel := context.WithTimeout(ctx, muxTimeout)
	defer cancel()
	_, err := runMux(exec.CommandContext(ctx, "tmux", "-S", socket, "set-option", "-p", "-t", pane, "@deck_role", role))
	return err
}

// LayoutSource 让布局 server 执行一个配置文件（按键绑定在窗格建好、知道 pane id 之后才写得出来）。
func LayoutSource(ctx context.Context, socket, path string) error {
	ctx, cancel := context.WithTimeout(ctx, muxTimeout)
	defer cancel()
	_, err := runMux(exec.CommandContext(ctx, "tmux", "-S", socket, "source-file", path))
	return err
}

// LayoutPane 是布局 server 上的一个窗格。
type LayoutPane struct {
	ID     string
	PID    int
	Role   string // @deck_role
	Window string // 所在窗口名；停放中的面板在 parked-* 窗口里
	Width  int
	Height int
	Active bool
	// WindowHeight 是所在窗口的高度（显隐与复原尺寸时按它分右栏面板的高度）。
	WindowHeight int
}

// LayoutPanes 列出布局 server 上的全部窗格。server 没在跑时返回 ErrNoServer。
func LayoutPanes(ctx context.Context, socket string) ([]LayoutPane, error) {
	ctx, cancel := context.WithTimeout(ctx, muxTimeout)
	defer cancel()
	out, err := runMux(exec.CommandContext(ctx, "tmux", "-S", socket, "list-panes", "-a", "-F",
		"#{pane_id}\t#{pane_pid}\t#{@deck_role}\t#{window_name}\t#{pane_width}\t#{pane_height}\t#{pane_active}\t#{window_height}"))
	if err != nil {
		if noServer(socket, err) {
			return nil, ErrNoServer
		}
		return nil, fmt.Errorf("列布局窗格失败：%w", err)
	}
	var panes []LayoutPane
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 8 {
			continue
		}
		pid, _ := strconv.Atoi(f[1])
		w, _ := strconv.Atoi(f[4])
		h, _ := strconv.Atoi(f[5])
		wh, _ := strconv.Atoi(f[7])
		panes = append(panes, LayoutPane{ID: f[0], PID: pid, Role: f[2], Window: f[3], Width: w, Height: h, Active: f[6] == "1", WindowHeight: wh})
	}
	return panes, nil
}

// LayoutSelect 选中窗格（让键盘输入落到它上面）。
func LayoutSelect(ctx context.Context, socket, pane string) error {
	ctx, cancel := context.WithTimeout(ctx, muxTimeout)
	defer cancel()
	_, err := runMux(exec.CommandContext(ctx, "tmux", "-S", socket, "select-pane", "-t", pane))
	return err
}

// LayoutPark 把窗格挪进一个后台窗口（程序不退出），用于隐藏整栏面板。
func LayoutPark(ctx context.Context, socket, pane, window string) error {
	ctx, cancel := context.WithTimeout(ctx, muxTimeout)
	defer cancel()
	_, err := runMux(exec.CommandContext(ctx, "tmux", "-S", socket, "break-pane", "-d", "-s", pane, "-n", window))
	return err
}

// LayoutUnpark 把停放的窗格挪回 target 旁边。
func LayoutUnpark(ctx context.Context, socket, pane, target string, spec SplitSpec) error {
	ctx, cancel := context.WithTimeout(ctx, muxTimeout)
	defer cancel()
	args := []string{"-S", socket, "join-pane", "-d"}
	if spec.Horizontal {
		args = append(args, "-h")
	} else {
		args = append(args, "-v")
	}
	if spec.Before {
		args = append(args, "-b")
	}
	if spec.Full {
		args = append(args, "-f")
	}
	args = append(args, "-l", spec.Size, "-s", pane, "-t", target)
	_, err := runMux(exec.CommandContext(ctx, "tmux", args...))
	return err
}

// LayoutResizeWidth 把窗格调到 width 列。
func LayoutResizeWidth(ctx context.Context, socket, pane, width string) error {
	ctx, cancel := context.WithTimeout(ctx, muxTimeout)
	defer cancel()
	_, err := runMux(exec.CommandContext(ctx, "tmux", "-S", socket, "resize-pane", "-t", pane, "-x", width))
	return err
}

// LayoutResizeHeight 把窗格调到 height 行。
func LayoutResizeHeight(ctx context.Context, socket, pane, height string) error {
	ctx, cancel := context.WithTimeout(ctx, muxTimeout)
	defer cancel()
	_, err := runMux(exec.CommandContext(ctx, "tmux", "-S", socket, "resize-pane", "-t", pane, "-y", height))
	return err
}

// LayoutNewWindow 在布局会话里新开一个窗口跑 command（设置界面用：它在窗格里，因此有 owner 身份）。
func LayoutNewWindow(ctx context.Context, socket, session, name, command string) error {
	ctx, cancel := context.WithTimeout(ctx, muxTimeout)
	defer cancel()
	_, err := runMux(exec.CommandContext(ctx, "tmux", "-S", socket, "new-window", "-t", exactTarget(session), "-n", name, command))
	return err
}

// LayoutEnsureSixel 让布局 server 认为外层终端支持 sixel（n056）：deck up 判定用 sixel 时调。
// 布局可能是早先用别的画法起的（BaseConf 只在 sixel 时带这项），没有这项时 tmux 对所有客户端一律画占位文字。
// 只对之后接上的客户端生效，所以要在 attach 之前调。已经有了就不再追加。
func LayoutEnsureSixel(ctx context.Context, socket string) error {
	ctx, cancel := context.WithTimeout(ctx, muxTimeout)
	defer cancel()
	out, err := runMux(exec.CommandContext(ctx, "tmux", "-S", socket, "show-options", "-s", "terminal-features"))
	if err == nil && strings.Contains(string(out), "sixel") {
		return nil
	}
	_, err = runMux(exec.CommandContext(ctx, "tmux", "-S", socket, "set-option", "-s", "-a", "terminal-features", ",*:sixel"))
	return err
}

// LayoutRefreshClient 让布局 server 把某个客户端整屏重画（n056：补上像素尺寸后，把之前画成占位的图换回来）。
func LayoutRefreshClient(ctx context.Context, socket, tty string) error {
	ctx, cancel := context.WithTimeout(ctx, muxTimeout)
	defer cancel()
	_, err := runMux(exec.CommandContext(ctx, "tmux", "-S", socket, "refresh-client", "-t", tty))
	return err
}

// LayoutBackgroundWindow 在布局会话里开一个后台窗口跑 command，不切过去（deck up 用它跑 deckd）。
func LayoutBackgroundWindow(ctx context.Context, socket, session, name, command string) error {
	ctx, cancel := context.WithTimeout(ctx, muxTimeout)
	defer cancel()
	_, err := runMux(exec.CommandContext(ctx, "tmux", "-S", socket, "new-window", "-d", "-t", exactTarget(session), "-n", name, command))
	return err
}

// LayoutTermFeatures 返回连在布局 server 上的外层终端的特性（#{client_termfeatures}），用来判断能不能画 Sixel。
// 没有 client 连着时返回空串。
func LayoutTermFeatures(ctx context.Context, socket string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, muxTimeout)
	defer cancel()
	out, err := runMux(exec.CommandContext(ctx, "tmux", "-S", socket, "list-clients", "-F", "#{client_termfeatures}"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]), nil
}

// LayoutAttachCommand 造一条「把当前终端接到布局会话」的命令（deck up 最后一步），不执行。
func LayoutAttachCommand(socket, session string) *exec.Cmd {
	return prepareNested(interactiveCommand("tmux", "-S", socket, "attach-session", "-t", exactTarget(session)))
}

// ---------------- agent-deck server 上的中间 client ----------------

// CenterAttachCommand 造中间窗格里那个嵌套 client：以只读方式连到 agent-deck server 的 session，不执行。
// 只读是起点（架构稿 §2）：放开输入只由 deckd 按目标决定。
func CenterAttachCommand(socket, session string) *exec.Cmd {
	return prepareNested(interactiveCommand("tmux", "-S", socket, "attach-session", "-r", "-t", exactTarget(session)))
}

// TmuxClient 是某个 tmux server 上的一个 client。
type TmuxClient struct {
	TTY      string
	PID      int
	Session  string
	ReadOnly bool
	Activity time.Time
}

// ListTmuxClients 列出 server 上全部 client。server 没在跑时返回空列表。
func ListTmuxClients(ctx context.Context, socket string) ([]TmuxClient, error) {
	ctx, cancel := context.WithTimeout(ctx, muxTimeout)
	defer cancel()
	out, err := runMux(exec.CommandContext(ctx, "tmux", "-S", socket, "list-clients", "-F",
		"#{client_tty}\t#{client_pid}\t#{session_name}\t#{client_readonly}\t#{client_activity}"))
	if err != nil {
		if noServer(socket, err) {
			return nil, nil
		}
		return nil, fmt.Errorf("列 client 失败：%w", err)
	}
	return parseClients(string(out)), nil
}

func parseClients(out string) []TmuxClient {
	var list []TmuxClient
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 5 {
			continue
		}
		pid, _ := strconv.Atoi(f[1])
		act, _ := strconv.ParseInt(f[4], 10, 64)
		list = append(list, TmuxClient{TTY: f[0], PID: pid, Session: f[2], ReadOnly: f[3] == "1", Activity: time.Unix(act, 0)})
	}
	return list
}

// ClientState 读一个 client 当前的会话、只读标记与最后输入时间。
//
// 从 list-clients 里按 tty 挑，不用 `display-message -c tty -p`：在 tmux 外执行时，后者格式里的 session_name
// 不是这个 client 所在的会话（n049 实测：切换成功后读回的仍是另一个会话名）。
func ClientState(ctx context.Context, socket, tty string) (TmuxClient, error) {
	list, err := ListTmuxClients(ctx, socket)
	if err != nil {
		return TmuxClient{}, fmt.Errorf("读 client %s 失败：%w", tty, err)
	}
	for _, c := range list {
		if c.TTY == tty {
			return c, nil
		}
	}
	return TmuxClient{}, fmt.Errorf("client %s 不在了", tty)
}

// ToggleClientReadOnly 翻转 client 的只读标记。tmux 3.4 只有翻转（switch-client -r），没有「设为」：
// refresh-client -f read-only 不改变只读状态（n049 实测）。所以调用方要先读、不一致才翻、翻完再读回核对。
//
// 必须带 -t 指明 client 现在所在的会话：只写 -r 时，tmux 在翻转的同时会把 client 切到它眼里的「当前会话」
// （在 tmux 外执行时是最近用过的那个），n049 实测切到了另一个子 agent。
func ToggleClientReadOnly(ctx context.Context, socket, tty, session string) error {
	ctx, cancel := context.WithTimeout(ctx, muxTimeout)
	defer cancel()
	_, err := runMux(exec.CommandContext(ctx, "tmux", "-S", socket, "switch-client", "-c", tty, "-r", "-t", exactTarget(session)))
	return err
}

// SwitchClient 把 client 切到 session（精确匹配会话名）。
func SwitchClient(ctx context.Context, socket, tty, session string) error {
	ctx, cancel := context.WithTimeout(ctx, muxTimeout)
	defer cancel()
	_, err := runMux(exec.CommandContext(ctx, "tmux", "-S", socket, "switch-client", "-c", tty, "-t", exactTarget(session)))
	return err
}

// CaptureSession 抓取会话当前窗格的画面文本（不带转义序列）。
func CaptureSession(ctx context.Context, socket, session string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, muxTimeout)
	defer cancel()
	// 「=会话:」：精确匹配会话名，取它当前窗口的当前窗格（只写「=会话」时 capture-pane 找不到窗格，实测）
	out, err := runMux(exec.CommandContext(ctx, "tmux", "-S", socket, "capture-pane", "-p", "-t", exactTarget(session)+":"))
	if err != nil {
		return nil, fmt.Errorf("抓不到 %s 的画面：%w", session, err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	return lines, nil
}

// EnsureSession 在 server 上确保有一个名为 session 的会话（没有就建，跑 command）。用于空白占位 deck-idle。
func EnsureSession(ctx context.Context, socket, session, command string) error {
	ctx, cancel := context.WithTimeout(ctx, muxTimeout)
	defer cancel()
	if _, err := runMux(exec.CommandContext(ctx, "tmux", "-S", socket, "has-session", "-t", exactTarget(session))); err == nil {
		return nil
	}
	_, err := runMux(exec.CommandContext(ctx, "tmux", "-S", socket, "new-session", "-d", "-s", session, command))
	if err != nil {
		return fmt.Errorf("建会话 %s 失败：%w", session, err)
	}
	return nil
}
