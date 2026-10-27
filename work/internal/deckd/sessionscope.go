package deckd

import (
	"context"
	"net/http"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/sessionenv"
)

// sessionScope 取一份会话 PATH 并放进 ctx（n060 段 4）：之后 toolInfo（设置里的已装与版本、Codex trust 门）、列模型、
// agent-deck launch 都用 ctx 里的这一份，所以同一次派工里 trust 门查到的和启动时交给上游的出自同一份快照。
// 取快照从不运行登录 shell（返工，owner 2026-10-27 定）：快照在 deckd 启动时后台读一次，之后只有 owner 在 F9 按「刷新会话 PATH」键才重读
// （sessionenv.Resolver）。打开设置、进加入页和 agent 页、派工、列模型都只是读当前快照；快照不会过期。
// 只有 deckd 刚启动、第一次读还没完成时才会等它（上限 5.5 秒）；刷新进行期间不等，用旧快照。
// 没有配置 Config.SessionEnv（测试、没开解析）时返回原 ctx 和 ok=false：行为与改动前一样，按进程自己的 PATH。
func (s *Server) sessionScope(ctx context.Context) (context.Context, sessionenv.Snapshot, bool) {
	r := s.cfg.SessionEnv
	if r == nil {
		return ctx, sessionenv.Snapshot{}, false
	}
	snap := r.Get(ctx)
	return agentdeck.WithSessionPath(ctx, snap.Path), snap, true
}

// startSessionEnv：Config.SessionPath 为真且没有现成的 Resolver 时，建一个解析登录 shell 的，并在后台读第一次
// （有超时，卡住也不影响 deckd 起来；第一次打开设置时就不用等）。后台读的 ctx 跟着 deckd：deckd 关闭时探针的进程组一起被杀。
func (c *Config) startSessionEnv(ctx context.Context) {
	if c.SessionPath && c.SessionEnv == nil {
		c.SessionEnv = sessionenv.NewLoginResolver(agentdeck.RunLoginShell)
		c.SessionEnv.Base = ctx
		c.SessionEnv.Start()
	}
}

// getToolsRefresh 是刷新键按下之后界面轮询用的：只说读完没有，不跑任何 CLI 的版本查询（GET /v1/tools 每次要把各 CLI 的 --version 都跑一遍，轮询用它太重）。
func (s *Server) getToolsRefresh(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, false, RoleOwner) {
		return
	}
	res := s.cfg.SessionEnv
	refreshing := false
	if res.Enabled() {
		refreshing = res.State(r.Context()).Refreshing
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": res.Enabled(), "refreshing": refreshing})
}

// postToolsRefresh 是 F9 里「刷新会话 PATH」键：在后台重读一次登录 shell 的 PATH（会运行 owner 的登录启动文件），立即返回。
// 已有一次读在跑就合并进去，不叠加。界面随后轮询 GET /v1/tools/refresh 看读完没有，读完再取一次 /v1/tools 拿结果。没开会话 PATH 解析时什么都不做。
func (s *Server) postToolsRefresh(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, true, RoleOwner) {
		return
	}
	res := s.cfg.SessionEnv
	started := res.Refresh() // nil 安全：Refresh 对没开解析的 Resolver 返回 false（见下）
	writeJSON(w, http.StatusAccepted, map[string]any{"enabled": res.Enabled(), "started": started})
}

// scopedToolInfo 是 deckd 生产里用的 ToolInfoFunc（agentdeck.ToolInfo 的会话 PATH 版，n060 段 4）：
// ctx 里有会话 PATH 就在这份 PATH 里解出可执行文件、按这份 PATH 跑 --version；没有就和 agentdeck.ToolInfo 一样按进程自己的 PATH。
func scopedToolInfo(ctx context.Context, binary string) (path, version string, err error) {
	sp, ok := agentdeck.SessionPathOf(ctx)
	if !ok {
		return agentdeck.ToolInfo(ctx, binary)
	}
	path = sessionenv.LookPath(sp, binary)
	if path == "" {
		return "", "", i18n.E("err.agentdeck.toolinfo.3", binary)
	}
	version, err = agentdeck.ToolVersionAt(ctx, path, sp)
	if err != nil {
		return path, "", i18n.E("err.agentdeck.toolinfo.2", binary, err)
	}
	return path, version, nil
}

// scopedListModels 是 listModels 的生产实现：同一份会话 PATH 里解出的可执行文件跑列模型命令，与 scopedToolInfo 查到的是同一个。
func scopedListModels(ctx context.Context, binary string, args []string) (string, error) {
	sp, ok := agentdeck.SessionPathOf(ctx)
	if !ok {
		return agentdeck.ListModels(ctx, binary, args)
	}
	exe := sessionenv.LookPath(sp, binary)
	if exe == "" {
		return "", i18n.E("err.agentdeck.toolinfo.3", binary)
	}
	return agentdeck.ListModelsAt(ctx, exe, args, sp)
}
