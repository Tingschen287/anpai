package deckd

import (
	"context"
	"errors"
	"net"

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

// withConn 把底层连接放进 context，供 resolve 取对端 pid。
func withConn(ctx context.Context, c net.Conn) context.Context {
	return context.WithValue(ctx, connKey{}, c)
}

// resolve 判定调用者。
func (s *Server) resolve(ctx context.Context) Caller {
	c, _ := ctx.Value(connKey{}).(*net.UnixConn)
	if c == nil {
		return Caller{Role: RoleUnknown, Err: errors.New("不是 unix socket 连接")}
	}
	pid, err := identity.PeerPID(c)
	if err != nil {
		return Caller{Role: RoleUnknown, Err: err}
	}
	if s.peerOverride != nil {
		pid = s.peerOverride(pid)
	}
	pane, err := s.resolver.Resolve(ctx, pid)
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
