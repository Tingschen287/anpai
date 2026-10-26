package deckd

import (
	"context"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/sessionenv"
)

// sessionScope 取一份会话 PATH 并放进 ctx（n060 段 4）：之后 toolInfo（设置里的已装与版本、Codex trust 门）、列模型、
// agent-deck launch 都用 ctx 里的这一份，所以 trust 门记下的可执行文件就是会话里实际跑的那一个。
// fresh 为真时超过 10 秒的缓存重新解析（设置里看「装了哪些」）；否则用缓存（派工、列模型）。
// 没有配置 Config.SessionEnv（测试、没开解析）时返回原 ctx 和 ok=false：行为与改动前一样，按进程自己的 PATH。
func (s *Server) sessionScope(ctx context.Context, fresh bool) (context.Context, sessionenv.Snapshot, bool) {
	r := s.cfg.SessionEnv
	if r == nil {
		return ctx, sessionenv.Snapshot{}, false
	}
	var snap sessionenv.Snapshot
	if fresh {
		snap = r.Fresh(ctx)
	} else {
		snap = r.Get(ctx)
	}
	return agentdeck.WithSessionPath(ctx, snap.Path), snap, true
}

// startSessionEnv：Config.SessionPath 为真且没有现成的 Resolver 时，建一个解析登录 shell 的，并在后台先解析一次
// （有超时，卡住也不影响 deckd 起来；第一次打开设置时就不用等）。
func (c *Config) startSessionEnv(ctx context.Context) {
	if c.SessionPath && c.SessionEnv == nil {
		c.SessionEnv = sessionenv.NewLoginResolver(agentdeck.RunLoginShell)
		go c.SessionEnv.Get(ctx)
	}
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
