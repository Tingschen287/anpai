package deckd

import (
	"context"
	"errors"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"net"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/identity"
)

// 角色（架构稿 §3 权限表）。
const (
	RoleOwner   = "owner"   // 布局 server 的窗格：侧栏、标签条、面板
	RoleMain    = "main"    // 注册表里某项目主 agent 的窗格
	RoleSub     = "sub"     // 注册表里某项目子 agent 的窗格
	RoleUnknown = "unknown" // 都不在：只能读额度与健康状态
)

// Caller 是服务端判定出的调用者。项目、会话一律由这里推导，不信任请求体。
type Caller struct {
	Role    string `json:"role"`
	Project string `json:"project,omitempty"`
	Session string `json:"session,omitempty"` // 注册表 key
	Pane    string `json:"pane,omitempty"`    // server|pane
	// Err 非空：身份查询本身失败（进程已消失、列窗格失败等）。这种调用者一律不能写。
	Err error `json:"-"`
}

type connKey struct{}
type connAtKey struct{}

// withConn 把底层连接与建立时间放进 context，供 resolve 取对端 pid 并核对它在连接前就已出生（n050 评审 B6）。
func withConn(ctx context.Context, c net.Conn) context.Context {
	ctx = context.WithValue(ctx, connAtKey{}, time.Now())
	return context.WithValue(ctx, connKey{}, c)
}

// peerOf 取连接的对端：pid、连接建立时间，以及（系统支持时）绑定对端进程本身的生命周期检查
// （Linux 是 pidfd，macOS 是出生时间，见 identity.PeerLifetime）——
// 连接身份绑定到进程，而不只是一个会被复用的整数 pid（n050 评审 R4）。内核不支持 SO_PEERPIDFD 时
// 返回没有 Same 的 Peer，由 classify 降为未知调用者（评审 A3-1）；其他错误按身份查询失败处理（不能写）。
// done 必须调用。
func peerOf(c *net.UnixConn, at time.Time) (identity.Peer, func(), error) {
	pid, err := identity.PeerPID(c)
	if err != nil {
		return identity.Peer{}, func() {}, err
	}
	peer := identity.Peer{PID: pid, ConnectedAt: at}
	same, release, err := identity.PeerLifetime(c, pid)
	switch {
	case err == nil:
		peer.Same = same
		return peer, release, nil
	case errors.Is(err, identity.ErrNoPIDFD):
		return peer, func() {}, nil
	default:
		return identity.Peer{}, func() {}, err
	}
}

// resolve 判定调用者。
func (s *Server) resolve(ctx context.Context) Caller {
	c, _ := ctx.Value(connKey{}).(*net.UnixConn)
	if c == nil {
		return Caller{Role: RoleUnknown, Err: i18n.E("err.deckd.serverresolve.1")}
	}
	at, _ := ctx.Value(connAtKey{}).(time.Time)
	peer, done, err := peerOf(c, at)
	if err != nil {
		return Caller{Role: RoleUnknown, Err: err}
	}
	defer done()
	return s.classify(ctx, peer)
}

// classify 按对端判定角色。没有 pidfd 绑定（peer.Same 为空，旧内核）时不授予任何角色：一律当未知调用者，
// 并带上 Err，使写接口全部拒绝，只能读健康与额度（n050 评审 A3-1）。identity.ResolvePeer 对这种连接本来就报错，
// 这里在授权层再拦一次，不依赖下层的实现。
func (s *Server) classify(ctx context.Context, peer identity.Peer) Caller {
	if peer.Same == nil {
		return Caller{Role: RoleUnknown, Err: identity.ErrNoLifetimeBinding}
	}
	return s.classifyPane(ctx, peer)
}

func (s *Server) classifyPane(ctx context.Context, peer identity.Peer) Caller {
	pane, err := s.resolver.ResolvePeer(ctx, peer)
	if errors.Is(err, identity.ErrUnknown) {
		return Caller{Role: RoleUnknown}
	}
	if err != nil {
		return Caller{Role: RoleUnknown, Err: err}
	}
	if pane.Server == s.cfg.LayoutSocket {
		return Caller{Role: RoleOwner, Pane: pane.Key()}
	}
	if pane.Server == s.cfg.AgentSocket {
		if x, ok := s.store.SessionByTmux(pane.Session); ok {
			role := RoleSub
			if x.Role == "main" {
				role = RoleMain
			}
			return Caller{Role: role, Project: x.Project, Session: x.Key, Pane: pane.Key()}
		}
	}
	// 在某个窗格里，但不是布局窗格、也不是注册表里的会话：未知
	return Caller{Role: RoleUnknown, Pane: pane.Key()}
}
