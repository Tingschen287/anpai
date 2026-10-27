package layout

import (
	"context"
	"strconv"
	"strings"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
)

// TmuxMux 是真实的布局 server（经 agentdeck 适配层）。
type TmuxMux struct {
	Socket string
	// Ctx 是这一批 tmux 操作共用的 context（n069 Codex 审查 R1：挂拔面板的总 deadline 与 deckd 关闭要传到每一条 tmux 命令）；nil 是 Background。
	Ctx context.Context
}

func (t TmuxMux) c() context.Context {
	if t.Ctx == nil {
		return context.Background()
	}
	return t.Ctx
}

// Detached 是同一个布局 server 上、不随 ctx 取消的 Mux（每条命令仍各有 5 秒上限）：收尾要把暂停的标签条与侧栏接回去，
// ctx 已到期也得做完，不然布局残缺。
func (t TmuxMux) Detached() Mux { return TmuxMux{Socket: t.Socket, Ctx: context.WithoutCancel(t.c())} }

func (t TmuxMux) NewSession(conf, session, window, width, height, command string) (string, error) {
	return agentdeck.LayoutNewSession(t.c(), t.Socket, conf, session, window, width, height, command)
}
func (t TmuxMux) Split(target string, spec agentdeck.SplitSpec, command string) (string, error) {
	return agentdeck.LayoutSplit(t.c(), t.Socket, target, spec, command)
}
func (t TmuxMux) SetRole(pane, role string) error {
	return agentdeck.LayoutSetRole(t.c(), t.Socket, pane, role)
}
func (t TmuxMux) Source(path string) error {
	return agentdeck.LayoutSource(t.c(), t.Socket, path)
}
func (t TmuxMux) Panes() ([]agentdeck.LayoutPane, error) {
	return agentdeck.LayoutPanes(t.c(), t.Socket)
}
func (t TmuxMux) Select(pane string) error {
	return agentdeck.LayoutSelect(t.c(), t.Socket, pane)
}
func (t TmuxMux) Park(pane, window string) error {
	return agentdeck.LayoutPark(t.c(), t.Socket, pane, window)
}
func (t TmuxMux) Unpark(pane, target string, spec agentdeck.SplitSpec) error {
	return agentdeck.LayoutUnpark(t.c(), t.Socket, pane, target, spec)
}

func (t TmuxMux) ResizeWidth(pane, width string) error {
	return agentdeck.LayoutResizeWidth(t.c(), t.Socket, pane, width)
}
func (t TmuxMux) ResizeHeight(pane, height string) error {
	return agentdeck.LayoutResizeHeight(t.c(), t.Socket, pane, height)
}

func (t TmuxMux) Remove(pane string) error {
	return agentdeck.LayoutRemovePane(t.c(), t.Socket, pane)
}

// paneNum 取 pane id（%12）里的数字，取不到返回很大的数（排到最后）。
func paneNum(id string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "%"))
	if err != nil {
		return 1 << 30
	}
	return n
}
