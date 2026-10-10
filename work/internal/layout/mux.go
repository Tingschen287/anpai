package layout

import (
	"context"
	"strconv"
	"strings"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
)

// TmuxMux 是真实的布局 server（经 agentdeck 适配层）。
type TmuxMux struct{ Socket string }

func (t TmuxMux) NewSession(conf, session, window, width, height, command string) (string, error) {
	return agentdeck.LayoutNewSession(context.Background(), t.Socket, conf, session, window, width, height, command)
}
func (t TmuxMux) Split(target string, spec agentdeck.SplitSpec, command string) (string, error) {
	return agentdeck.LayoutSplit(context.Background(), t.Socket, target, spec, command)
}
func (t TmuxMux) SetRole(pane, role string) error {
	return agentdeck.LayoutSetRole(context.Background(), t.Socket, pane, role)
}
func (t TmuxMux) Source(path string) error {
	return agentdeck.LayoutSource(context.Background(), t.Socket, path)
}
func (t TmuxMux) Panes() ([]agentdeck.LayoutPane, error) {
	return agentdeck.LayoutPanes(context.Background(), t.Socket)
}
func (t TmuxMux) Select(pane string) error {
	return agentdeck.LayoutSelect(context.Background(), t.Socket, pane)
}
func (t TmuxMux) Park(pane, window string) error {
	return agentdeck.LayoutPark(context.Background(), t.Socket, pane, window)
}
func (t TmuxMux) Unpark(pane, target string, spec agentdeck.SplitSpec) error {
	return agentdeck.LayoutUnpark(context.Background(), t.Socket, pane, target, spec)
}

func (t TmuxMux) ResizeWidth(pane, width string) error {
	return agentdeck.LayoutResizeWidth(context.Background(), t.Socket, pane, width)
}
func (t TmuxMux) ResizeHeight(pane, height string) error {
	return agentdeck.LayoutResizeHeight(context.Background(), t.Socket, pane, height)
}

// paneNum 取 pane id（%12）里的数字，取不到返回很大的数（排到最后）。
func paneNum(id string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "%"))
	if err != nil {
		return 1 << 30
	}
	return n
}
