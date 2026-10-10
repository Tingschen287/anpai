package deckd

import (
	"context"
	"errors"
	"net"
	"syscall"
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

// peerOf 取连接的对端：pid、连接建立时间，以及（内核支持时）绑定对端进程本身的 pidfd 检查——
// 连接身份绑定到进程，而不只是一个会被复用的整数 pid（n050 评审 R4）。内核不支持 SO_PEERPIDFD 时
// 退回出生时间检查；其他错误按身份查询失败处理（不能写）。done 必须调用。
func peerOf(c *net.UnixConn, at time.Time) (identity.Peer, func(), error) {
	pid, err := identity.PeerPID(c)
	if err != nil {
		return identity.Peer{}, func() {}, err
	}
	peer := identity.Peer{PID: pid, ConnectedAt: at}
	fd, err := identity.PeerPIDFD(c)
	switch {
	case err == nil:
		peer.Same = identity.PIDFDHolds(fd, pid)
		return peer, func() { syscall.Close(fd) }, nil
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
		return Caller{Role: RoleUnknown, Err: errors.New("不是 unix socket 连接")}
	}
	at, _ := ctx.Value(connAtKey{}).(time.Time)
	peer, done, err := peerOf(c, at)
	if err != nil {
		return Caller{Role: RoleUnknown, Err: err}
	}
	defer done()
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
