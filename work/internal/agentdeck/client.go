// Package agentdeck 是 Work Deck 与 upstream agent-deck 之间的唯一通道。
//
// 这里说的 upstream agent-deck 指的是开源项目 github.com/asheshgoplani/agent-deck，
// 不是本仓自己那个同名的 Agent Deck 启动器。两者重名但毫无关系，
// 项目里的 2.code/link.md 明文要求提到上游时写全称，本包沿用这个规矩。
//
// 包外的代码不得 exec 它的命令、不得请求它的 HTTP 端点、不得读它的文件。
// 要加一条新的上游调用，就加在这个包里，别处不开第二个口子。上游一旦变更，
// 对照 manifest.go 里的依赖清单逐条核，改动范围就锁在这一个包内。
package agentdeck

import (
	"bytes"
	"context"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/sessionenv"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Subcommands 是本包允许执行的全部上游子命令。
//
// 这个清单就是静态检查的依据：包内不得出现清单以外的子命令，
// 包外不得出现任何一条。
// 带空格的条目是二级子命令，必须整条匹配。
//
// 只写 "session" 是不够的：那样 session stop / remove / kill / restart 会一并
// 放行，而 Work Deck 没有杀掉别人会话的道理。白名单精确到它真正要用的那几条。
//
// `session send` 是 n037 attempt 6 加的，为了把评审 brief 送进一个已经核验过
// 会话号的评审会话（两阶段投递，见 dispatch 包的 DispatchReviewer）。
// 加它**不会**放开 session 底下别的命令：上面这套匹配是整条二级路径比对，
// stop / remove / restart / archive / fork / set 一条都不在清单里，照样被拒。
//
// 执行者此前以「加这个等于放开一整片操作面」为由拒绝实现两阶段投递，
// 那个理由是错的，独立评审指出了这一点。
//
// `list` 是 attempt 7 加的，直接读上游存储不经 headless 服务——
// 这一条正是它的用处：服务的菜单缓存看不见刚 launch 的会话，而 list 看得见。
// 实测见 TestIntegrationListSeesWhatTheCachedMenuCannot。
//
// **它不是只读的。** 我在 attempt 7 写过「只读」，那是错的：
// 上游 `list --json` 会先跑 RefreshInstancesForCLIStatus 暖缓存、读 hook 状态，
// 那条路最终会把 last_activity_at 写进 state.db：
//
//	cmd/agent-deck/main.go:2277        RefreshInstancesForCLIStatus(instances)
//	internal/session/cli_status_refresh.go:37   inst.UpdateHookStatus(hs)
//	internal/session/instance.go:6280           defer i.persistLastActivity(false)
//	internal/session/last_activity_persist.go:124  db.WriteLastActivityAt(...)
//
// 写的只是一个活动时间戳，不改会话状态、不停会话，但它确实是持久化写入。
// 白名单的含义因此是「允许的命令」，不是「无副作用的命令」——
// SideEffects 里逐条记着哪几条会写。client_test.go 的 TestEveryAllowedSubcommandDeclaresItsSideEffects
// 与 TestSideEffectEvidencePointsAtRealUpstreamCode 核对这份声明，核到哪一步见后者的注释。
//
// 这条是独立评审顺着源码追出来的。我读过 `list --json` 那段代码，
// RefreshInstancesForCLIStatus 就在我查的结构体下面几行，我没追进去看它做什么。
//
// inbox 只放两条非消费式读取（n038）：export 读全机记录不删任何东西，writer-status 问
// notify-daemon 在不在。drain 与裸 `inbox <id>` 都会消费记录，和 conductor 抢，一律拒绝。
//
// `session stop` 是 n044 加的，只有一个用处：派工器刚起的会话没拿到任务说明（Graph 开工失败、投递失败、
// 上游起成了 shell）时把它停掉，不留一个没人认领的会话。它要一张写入权许可（StopSession），
// 所以 owner 接管着、或探测到有人连着的会话一律不停。remove / restart / archive / fork / set 仍不在清单里。
// 上游 stop 会杀掉该会话的 tmux 会话；本包自己仍不拼任何 tmux kill 命令（TestAdapterMayAttachButNeverSendsKeysOrKills）。
var Subcommands = []string{"launch", "web", "list", "session attach", "session output", "session send",
	"session stop", "inbox export", "inbox writer-status"}

// isAllowedSubcommand 按整条命令路径匹配。
//
// parts 是子命令层级，例如 ["session","output"]。多余的参数不参与匹配。
func isAllowedSubcommand(parts ...string) bool {
	if len(parts) == 0 {
		return false
	}
	one := parts[0]
	two := ""
	if len(parts) > 1 {
		two = parts[0] + " " + parts[1]
	}
	for _, s := range Subcommands {
		if s == one {
			return true
		}
		if two != "" && s == two {
			return true
		}
	}
	return false
}

// depthOf 返回某个一级子命令需要匹配几层。
//
// session 这种带危险兄弟命令的，必须连第二级一起看；launch / web 没有子命令。
func depthOf(first string) int {
	for _, s := range Subcommands {
		if strings.Contains(s, " ") && strings.HasPrefix(s, first+" ") {
			return 2
		}
	}
	return 1
}

// ErrSubcommandNotAllowed 表示有人想通过本包跑一条清单之外的上游子命令。
type ErrSubcommandNotAllowed struct{ Subcommand string }

func (e *ErrSubcommandNotAllowed) Error() string { return e.I18n().Error() }

func (e *ErrSubcommandNotAllowed) I18n() *i18n.Error {
	return i18n.E("err.agentdeck.errsubcommand", e.Subcommand, strings.Join(Subcommands, " / "))
}

// Client 持有连上一个 upstream agent-deck 实例所需的全部信息。
type Client struct {
	// 上游可执行文件路径。
	Binary string
	// 隔离用的 profile 名。空串意味着会落到上游的默认 profile，
	// 那可能是使用者的真实工作数据，所以本包一律要求显式指定。
	Profile string
	// headless 服务的地址，形如 127.0.0.1:8420。
	Addr string
	// 访问 API 与 SSE 的 bearer token。
	//
	// 只在内存里传，本包绝不把它写进任何文件、日志或命令行参数之外的地方。
	Token string
	// 单条命令的超时。
	Timeout time.Duration

	httpClient *http.Client
}

// NewClient 构造一个客户端。profile 必须显式给出。
func NewClient(binary, profile, addr, token string) (*Client, error) {
	if binary == "" {
		return nil, i18n.E("err.agentdeck.newclient.1")
	}
	if profile == "" {
		// 上游自己的事故记录（2025-12-11）就是测试跑在真实 profile 上，
		// 一次覆盖掉 36 个生产会话。这里不接受默认值。
		return nil, i18n.E("err.agentdeck.newclient.2")
	}
	return &Client{
		Binary:     binary,
		Profile:    profile,
		Addr:       addr,
		Token:      token,
		Timeout:    60 * time.Second,
		httpClient: &http.Client{},
	}, nil
}

// env 组装子进程环境：在当前环境之上强制写入 profile，并去掉 TMUX/TMUX_PANE（见 withoutTmux）。
func (c *Client) env() []string {
	base := withoutTmux(os.Environ())
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if strings.HasPrefix(kv, "AGENTDECK_PROFILE=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "AGENTDECK_PROFILE="+c.Profile)
}

// exec 跑一条上游子命令，返回标准输出。
func (c *Client) exec(ctx context.Context, sub string, args ...string) ([]byte, error) {
	return c.execWithStdin(ctx, nil, sub, args...)
}

// execWithStdin 与 exec 相同，另外把 stdin 接上。
//
// 为什么要它：评审 brief 是一大段文本，走 argv 要经过 shell 引号和长度限制，
// 而上游自己提供了 `--message-file -` 从标准输入读。文本不进命令行，
// 就不存在被当成参数解释的可能。
func (c *Client) execWithStdin(ctx context.Context, stdin io.Reader, sub string, args ...string) ([]byte, error) {
	// session 这类带危险兄弟命令的，要连第二级一起核。
	parts := []string{sub}
	if depthOf(sub) == 2 && len(args) > 0 {
		parts = append(parts, args[0])
	}
	if !isAllowedSubcommand(parts...) {
		return nil, &ErrSubcommandNotAllowed{Subcommand: strings.Join(parts, " ")}
	}
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, c.Binary, append([]string{sub}, args...)...)
	cmd.Env = c.env()
	if sp, ok := SessionPathOf(ctx); ok && sub == "launch" {
		// 会话的 PATH 就是启动 agent-deck 的进程的 PATH（n060 段 4 实测）：把派工时查 CLI 用的那份交给它，
		// 会话里实际跑的才是 trust 门记下的那个可执行文件
		cmd.Env = sessionenv.Env(cmd.Env, sp)
	}
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// 外部命令的输出是外部原文：伪造的打码标记在这里换掉（返工 4）
	if err := cmd.Run(); err != nil {
		return i18n.NeutralizeBytes(stdout.Bytes()), i18n.E("err.agentdeck.clientexecwithstdin.1", sub, err, truncate(i18n.Neutralize(stderr.String()), 400))
	}
	return i18n.NeutralizeBytes(stdout.Bytes()), nil
}

// get 请求 headless 服务的一个 JSON 端点。
func (c *Client) get(ctx context.Context, path string, out any) error {
	if c.Addr == "" {
		return i18n.E("err.agentdeck.clientget.1")
	}
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+c.Addr+path, nil)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return i18n.E("err.agentdeck.clientget.2", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return i18n.E("err.agentdeck.clientget.3", path, resp.StatusCode)
	}
	// 上游的 HTTP 响应是外部原文：先整体读进来换掉伪造的标记再解码（返工 4）
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return i18n.E("err.agentdeck.clientget.2", path, err)
	}
	return i18n.DecodeExternal(data, out) // 解码之后再中和：JSON 转义写法的伪造标记只有解码后才出现（段 3）
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
