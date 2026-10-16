package ui

import (
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/dispatch"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

// 挑模型时把路由因子摆出来：推荐与否及理由、每个候选的能力与额度、额度依据。
func TestModelPickingShowsRoutingFactors(t *testing.T) {
	n := projectctl.Node{ID: "n1", Kind: "task", RouteClass: "frontier", State: projectctl.Known(projectctl.StateReady)}
	fd := &fakeDispatcher{cands: []dispatch.Model{
		{ID: "claude-opus-5", Tool: "claude", Class: dispatch.Frontier, Caps: map[string]string{"resume": "yes"}},
	}}
	m := modelWithDispatcher(t, []projectctl.Node{n}, fd)
	m, _ = press(m, 'd')
	out := m.View()
	for _, must := range []string{"不自动推荐，交给你挑", "额度 unknown", "未知：额度未知", "额度依据：n026"} {
		if !strings.Contains(out, must) {
			t.Errorf("挑模型界面缺 %q：\n%s", must, out)
		}
	}
}
