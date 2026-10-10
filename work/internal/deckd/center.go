package deckd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/identity"
	"github.com/Tingschen287/terminal-deck/work/internal/viewstate"
)

// TmuxCenter 是真实的中间窗格（n049，架构稿 §1、§2），实现 viewstate.Center。
//
// 中间窗格是布局 server 上 @deck_role=center 的窗格，里面跑 `deck center`，它再起一个连到 agent-deck server 的
// 只读 tmux client。deckd 每次操作前按进程链认出这个 client：client 进程（或它的祖先）就是中间窗格的进程。
// 不缓存 tty：Ctrl-Q 之后 client 会重建，tty 会变，按进程链现找最稳。
type TmuxCenter struct {
	LayoutSocket string
	AgentSocket  string
	// Ancestry 返回进程链（identity.Resolver.Ancestry）；测试可换。
	Ancestry func(pid int) ([]identity.Proc, error)

	mu    sync.Mutex
	bound int // Bind 绑定的 client 进程号（0 未绑定）
}

// Bind 见 viewstate.Center：之后的操作只作用于这个 client，它被替换或不在了就返回 ErrStaleClient。
func (c *TmuxCenter) Bind(client int) {
	c.mu.Lock()
	c.bound = client
	c.mu.Unlock()
}

// NewTmuxCenter 用真实的 /proc 建中间窗格。
func NewTmuxCenter(layoutSocket, agentSocket string) *TmuxCenter {
	r := identity.Resolver{}
	return &TmuxCenter{LayoutSocket: layoutSocket, AgentSocket: agentSocket, Ancestry: r.Ancestry}
}

// ErrNoCenter 表示布局没起来，或中间窗格里没有连上的 client。
var ErrNoCenter = errors.New("找不到中间窗格的 client（布局没起来，或中间窗格正在重连）")

// client 找中间现在的 client；有绑定时还要求就是绑定的那个。
func (c *TmuxCenter) client() (agentdeck.TmuxClient, error) {
	c.mu.Lock()
	bound := c.bound
	c.mu.Unlock()
	return c.find(bound)
}

func (c *TmuxCenter) find(bound int) (agentdeck.TmuxClient, error) {
	ctx := context.Background()
	panes, err := agentdeck.LayoutPanes(ctx, c.LayoutSocket)
	if errors.Is(err, agentdeck.ErrNoServer) {
		return agentdeck.TmuxClient{}, ErrNoCenter
	}
	if err != nil {
		return agentdeck.TmuxClient{}, err
	}
	centerPID := 0
	for _, p := range panes {
		if p.Role == "center" {
			if centerPID != 0 {
				return agentdeck.TmuxClient{}, errors.New("布局里有两个中间窗格，无法判定")
			}
			centerPID = p.PID
		}
	}
	if centerPID == 0 {
		return agentdeck.TmuxClient{}, ErrNoCenter
	}
	clients, err := agentdeck.ListTmuxClients(ctx, c.AgentSocket)
	if err != nil {
		return agentdeck.TmuxClient{}, err
	}
	var hits []agentdeck.TmuxClient
	for _, cl := range clients {
		if cl.PID == centerPID {
			hits = append(hits, cl)
			continue
		}
		chain, err := c.Ancestry(cl.PID)
		if err != nil {
			continue // client 恰好退出：不算它
		}
		for _, p := range chain {
			if p.PID == centerPID {
				hits = append(hits, cl)
				break
			}
		}
	}
	switch {
	case len(hits) > 1:
		return agentdeck.TmuxClient{}, fmt.Errorf("中间窗格下有 %d 个 client，无法判定", len(hits))
	case bound != 0 && (len(hits) == 0 || hits[0].PID != bound):
		// 重建的 client 与旧的共用中间窗格的 tty：不核对进程号就会把旧操作做到新 client 上（n049 评审 2 B1）
		return agentdeck.TmuxClient{}, fmt.Errorf("%w（这次操作绑定的 client %d 已不在中间）", viewstate.ErrStaleClient, bound)
	case len(hits) == 0:
		return agentdeck.TmuxClient{}, ErrNoCenter
	}
	return hits[0], nil
}

// SetReadOnly 设为只读或可写：tmux 只有翻转，所以先读、不一致才翻、翻完读回核对。
func (c *TmuxCenter) SetReadOnly(ro bool) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	if cl.ReadOnly == ro {
		return nil
	}
	ctx := context.Background()
	if err := agentdeck.ToggleClientReadOnly(ctx, c.AgentSocket, cl.TTY, cl.Session); err != nil {
		return err
	}
	st, err := agentdeck.ClientState(ctx, c.AgentSocket, cl.TTY)
	if err != nil {
		return err
	}
	if st.PID != cl.PID {
		return fmt.Errorf("翻转只读时中间 client 被替换了（%d → %d）", cl.PID, st.PID) // tty 被新 client 复用
	}
	if st.ReadOnly != ro {
		return fmt.Errorf("只读标记读回不对（要 %v，得 %v）", ro, st.ReadOnly)
	}
	if st.Session != cl.Session {
		return fmt.Errorf("翻转只读时 client 被切走了（%s → %s）", cl.Session, st.Session)
	}
	return nil
}

// ClientID 是中间现在那个 client 的进程号。
// 不看绑定：绑定之前要先知道是谁。
func (c *TmuxCenter) ClientID() (int, error) {
	cl, err := c.find(0)
	if err != nil {
		return 0, err
	}
	return cl.PID, nil
}

// ReadOnly 读回中间 client 的只读标记。
func (c *TmuxCenter) ReadOnly() (bool, error) {
	cl, err := c.client()
	if err != nil {
		return false, err
	}
	st, err := agentdeck.ClientState(context.Background(), c.AgentSocket, cl.TTY)
	if err != nil {
		return false, err
	}
	if st.PID != cl.PID {
		return false, fmt.Errorf("读只读标记时中间 client 被替换了（%d → %d）", cl.PID, st.PID)
	}
	return st.ReadOnly, nil
}

// Switch 把中间 client 切到 agent-deck server 上的 tmuxSession。
func (c *TmuxCenter) Switch(tmuxSession string) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	return agentdeck.SwitchClient(context.Background(), c.AgentSocket, cl.TTY, tmuxSession)
}

// LastInput 是 owner 最后一次在中间打字的时间（client_activity，精度 1 秒）。
func (c *TmuxCenter) LastInput() (time.Time, error) {
	cl, err := c.client()
	if err != nil {
		return time.Time{}, err
	}
	st, err := agentdeck.ClientState(context.Background(), c.AgentSocket, cl.TTY)
	if err != nil {
		return time.Time{}, err
	}
	return st.Activity, nil
}

// Capture 抓 agent-deck server 上指定会话的画面，返回末尾 lines 行非空行。
// 按会话名抓，不看中间 client 正显示哪个（client 重建后它在 deck-idle 上）。
func (c *TmuxCenter) Capture(session string, lines int) ([]string, error) {
	if session == "" || session == viewstate.Idle {
		return nil, fmt.Errorf("不抓占位会话 %q 当交还摘要", session)
	}
	all, err := agentdeck.CaptureSession(context.Background(), c.AgentSocket, session)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range all {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimRight(l, " "))
		}
	}
	if len(out) > lines {
		out = out[len(out)-lines:]
	}
	return out, nil
}
