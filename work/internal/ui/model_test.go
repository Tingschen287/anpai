package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tingschen287/terminal-deck/work/internal/binding"
	"github.com/Tingschen287/terminal-deck/work/internal/dispatch"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

// 心跳不许压着取数堆。
//
// 真实项目上一轮完整取数要近两分钟，而心跳是 5 秒一拍。不挡一下的话，
// 取数会一轮压一轮，每轮又各开几个控制器进程，很快就把机器占满。
// 这个问题由 n035 的两位独立评审者各自发现并列为观察项。
func TestTickDoesNotStackFetchesWhileOneIsInFlight(t *testing.T) {
	m := New(nil)

	// 刚造出来就该算在途：Init 必定发起一轮取数。
	if !m.inflight {
		t.Fatal("新建的界面应当已经登记了在途取数")
	}

	// 在途时收到心跳，不该再起一轮，但心跳本身要续上。
	next, cmd := m.Update(tickMsg(time.Now()))
	m2 := next.(Model)
	if !m2.inflight {
		t.Error("在途状态被心跳弄丢了")
	}
	if cmd == nil {
		t.Error("心跳必须续上，否则界面从此不再自动刷新")
	}

	// 取数回来后解除在途。
	next, _ = m2.Update(snapshotMsg{})
	m3 := next.(Model)
	if m3.inflight {
		t.Error("取数回来后应当解除在途")
	}
	if m3.loading {
		t.Error("取数回来后不该还停在加载态")
	}

	// 空闲时的心跳才真正起一轮新的。
	next, cmd = m3.Update(tickMsg(time.Now()))
	m4 := next.(Model)
	if !m4.inflight {
		t.Error("空闲时的心跳应当起一轮新取数并登记在途")
	}
	if cmd == nil {
		t.Error("空闲时的心跳也要续上")
	}
}

// 手动刷新同样不许叠。
func TestManualRefreshIsIgnoredWhileOneIsInFlight(t *testing.T) {
	m := New(nil)
	if !m.inflight {
		t.Fatal("前提不成立")
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd != nil {
		t.Error("在途时按 r 不该再起一轮取数")
	}
	if !next.(Model).inflight {
		t.Error("在途状态不该被按键清掉")
	}
}

// fakeDispatcher 记下被请求的派工，并按脚本回应。
type fakeDispatcher struct {
	cands []dispatch.Model
	got   []dispatch.Request
	err   error
}

func (f *fakeDispatcher) Candidates(required dispatch.RouteClass) []dispatch.Model {
	var out []dispatch.Model
	for _, m := range f.cands {
		if m.Class.Meets(required) {
			out = append(out, m)
		}
	}
	return out
}

func (f *fakeDispatcher) Dispatch(ctx context.Context, req dispatch.Request) (binding.Binding, error) {
	f.got = append(f.got, req)
	if f.err != nil {
		return binding.Binding{}, f.err
	}
	return binding.Binding{
		NodeID: req.NodeID, Attempt: req.Attempt, SessionID: "sess-x",
		Model: req.ModelID, RouteClass: string(req.RequiredClass),
	}, nil
}

func nodeWith(id string, state projectctl.State, class string) projectctl.Node {
	return projectctl.Node{
		ID: id, Title: id, RouteClass: class,
		State:   projectctl.Known(state),
		Attempt: projectctl.Unknown[int]("没开工过"),
	}
}

func modelWithDispatcher(t *testing.T, nodes []projectctl.Node, d Dispatcher) Model {
	t.Helper()
	m := New(nil).WithDispatcher(d)
	m.snap = projectctl.Snapshot{Nodes: nodes, ProjectRoot: "/tmp/proj"}
	m.inflight = false
	m.loading = false
	return m
}

func pressD(m Model) Model {
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	return next.(Model)
}

// 按了键没反应比报错更难查：每种拒绝都要把原因摆到屏幕上。
func TestDispatchRefusalsAlwaysExplainThemselves(t *testing.T) {
	fd := &fakeDispatcher{cands: []dispatch.Model{
		{ID: "claude-opus-5", Tool: "claude", Class: dispatch.Frontier},
	}}

	cases := []struct {
		name   string
		nodes  []projectctl.Node
		disp   Dispatcher
		expect string
	}{
		{
			name:   "节点不是 ready",
			nodes:  []projectctl.Node{nodeWith("n1", projectctl.StateAccepted, "frontier")},
			disp:   fd,
			expect: "只有 ready 的节点能派工",
		},
		{
			name:   "没配派工能力",
			nodes:  []projectctl.Node{nodeWith("n1", projectctl.StateReady, "frontier")},
			disp:   nil,
			expect: "没有配置派工",
		},
		{
			name:   "节点没写 route class",
			nodes:  []projectctl.Node{nodeWith("n1", projectctl.StateReady, "")},
			disp:   fd,
			expect: "拒绝派工，不猜",
		},
		{
			name:   "档位表里没有够格的模型",
			nodes:  []projectctl.Node{nodeWith("n1", projectctl.StateReady, "human")},
			disp:   fd,
			expect: "拒绝降档派工",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := modelWithDispatcher(t, c.nodes, c.disp)
			if c.disp == nil {
				m.dispatcher = nil
			}
			after := pressD(m)
			if after.mode == ModePickModel {
				t.Fatal("不该进入挑模型界面")
			}
			if after.noticeOK {
				t.Error("拒绝不该标成成功")
			}
			if !strings.Contains(after.notice, c.expect) {
				t.Errorf("提示里没说清原因，期望含 %q，实际 %q", c.expect, after.notice)
			}
			// 屏幕上真的要看得见。
			out := RenderScreen(after.snap, 100, Screen{
				Cursor: after.cursor, Notice: after.notice, NoticeOK: after.noticeOK,
			})
			if !strings.Contains(out, c.expect) {
				t.Errorf("拒绝原因没画到屏幕上：%q", out)
			}
		})
	}
}

// 状态还不知道的节点不许派。
func TestDispatchRefusesNodeWithUnknownState(t *testing.T) {
	n := nodeWith("n1", projectctl.StateReady, "frontier")
	n.State = projectctl.Unknown[projectctl.State]("查询中")
	m := modelWithDispatcher(t, []projectctl.Node{n}, &fakeDispatcher{
		cands: []dispatch.Model{{ID: "m", Tool: "t", Class: dispatch.Frontier}},
	})
	after := pressD(m)
	if after.mode == ModePickModel {
		t.Fatal("状态未知时不该进入挑模型界面")
	}
	if !strings.Contains(after.notice, "状态还不知道") {
		t.Errorf("没说清为什么不派：%q", after.notice)
	}
}

// 正常路径：挑模型，回车，派出去。
func TestDispatchHappyPathPassesTheChosenModel(t *testing.T) {
	fd := &fakeDispatcher{cands: []dispatch.Model{
		{ID: "claude-sonnet-5", Tool: "claude", Class: dispatch.Balanced},
		{ID: "claude-opus-5", Tool: "claude", Class: dispatch.Frontier},
	}}
	m := modelWithDispatcher(t, []projectctl.Node{
		nodeWith("n1", projectctl.StateReady, "balanced"),
	}, fd)

	m = pressD(m)
	if m.mode != ModePickModel {
		t.Fatal("ready 节点按 d 应当进入挑模型界面")
	}
	if len(m.candidates) != 2 {
		t.Fatalf("候选数是 %d，期望 2", len(m.candidates))
	}
	// 第一个候选应当是刚好够用的那档，不是最贵的。
	if m.candidates[0].Class != dispatch.Balanced {
		t.Errorf("第一个候选是 %s，期望刚好够用的 balanced", m.candidates[0].Class)
	}

	// 往下挑一个，回车。
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(Model)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("回车应当真的发起一次派工")
	}
	msg := cmd()
	done, ok := msg.(dispatchDoneMsg)
	if !ok {
		t.Fatalf("派工应当回报结果，实际是 %T", msg)
	}
	if !done.ok {
		t.Fatalf("派工不该失败：%s", done.text)
	}
	if len(fd.got) != 1 {
		t.Fatalf("应当只派一次，实际 %d 次", len(fd.got))
	}
	if fd.got[0].ModelID != "claude-opus-5" {
		t.Errorf("派出去的模型是 %q，期望挑中的 claude-opus-5", fd.got[0].ModelID)
	}
	if fd.got[0].NodeID != "n1" || fd.got[0].Attempt != 1 {
		t.Errorf("派工请求不对：%+v", fd.got[0])
	}
}

// Esc 取消要说一声，不能默默回去。
func TestEscapeCancelsAndSaysSo(t *testing.T) {
	m := modelWithDispatcher(t, []projectctl.Node{
		nodeWith("n1", projectctl.StateReady, "frontier"),
	}, &fakeDispatcher{cands: []dispatch.Model{{ID: "m", Tool: "t", Class: dispatch.Frontier}}})
	m = pressD(m)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	after := next.(Model)
	if after.mode != ModeBrowse {
		t.Error("Esc 应当退出挑模型界面")
	}
	if !strings.Contains(after.notice, "已取消") {
		t.Errorf("取消了却没说一声：%q", after.notice)
	}
}
