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
	"encoding/json"
	"fmt"
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
// 放行，而 Work Deck 没有杀掉别人会话的道理。白名单精确到它真正要用的那两条。
var Subcommands = []string{"launch", "web", "session attach", "session output"}

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

func (e *ErrSubcommandNotAllowed) Error() string {
	return fmt.Sprintf("拒绝执行上游子命令 %q：不在 agentdeck 包的允许清单内（%s）",
		e.Subcommand, strings.Join(Subcommands, " / "))
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
		return nil, fmt.Errorf("必须指定 upstream agent-deck 可执行文件路径")
	}
	if profile == "" {
		// 上游自己的事故记录（2025-12-11）就是测试跑在真实 profile 上，
		// 一次覆盖掉 36 个生产会话。这里不接受默认值。
		return nil, fmt.Errorf("必须显式指定 profile：留空会落到上游默认 profile，可能覆盖真实会话")
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

// env 组装子进程环境：在当前环境之上强制写入 profile。
func (c *Client) env() []string {
	out := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "AGENTDECK_PROFILE=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "AGENTDECK_PROFILE="+c.Profile)
}

// exec 跑一条上游子命令，返回标准输出。
func (c *Client) exec(ctx context.Context, sub string, args ...string) ([]byte, error) {
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
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("upstream agent-deck %s 执行失败：%w（stderr: %s）",
			sub, err, truncate(stderr.String(), 400))
	}
	return stdout.Bytes(), nil
}

// get 请求 headless 服务的一个 JSON 端点。
func (c *Client) get(ctx context.Context, path string, out any) error {
	if c.Addr == "" {
		return fmt.Errorf("没有配置 upstream agent-deck 的服务地址")
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
		return fmt.Errorf("请求 upstream agent-deck %s 失败：%w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream agent-deck %s 返回 HTTP %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
