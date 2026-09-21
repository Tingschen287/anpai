package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

// 这个文件把「按 a 进入」和「按 p 预览」两条路上所有的拒绝出口列全，逐条测。
//
// 口径说清楚，免得再数错一次：
//
//	按代码分支数，进入有 8 个拒绝出口，预览有 7 个，一共 15 个。
//	其中找会话那 4 个（没接绑定存储、不知道第几次 attempt、没有绑定、绑定里没会话号）
//	是两条路共用的同一段实现 sessionOf，所以 15 个出口落在 11 个不同的分支上。
//
// 上一版的报告写的是「九条」，既没说清是按出口还是按分支数，数也不对。
// 下面这张表就是那个口径的全集；表里每条都要求提示里带一个能照着做的下一步。

// refusalCase 是一个拒绝出口：怎么把界面摆成那个样子，按哪个键，该看到什么。
type refusalCase struct {
	name string
	// key 是触发的按键：'a' 进入，'p' 预览。
	key rune
	// setup 把界面摆成会走到这个出口的样子。
	setup func(t *testing.T) Model
	// wantContains 是提示里必须出现的话，用来认出确实是这个出口。
	wantContains string
	// wantAction 是提示里必须给出的下一步动作。每条都得有，没有豁免。
	wantAction string
	// viaMsg 不为 nil 时，这个出口不是按键当场走到的，而是异步回来的消息。
	viaMsg func(m Model) tea.Msg
}

// boundNode 是一个开过工、绑定齐全的运行中节点。
func boundNode() ([]projectctl.Node, fakeBindings) {
	return []projectctl.Node{runningNode("n1", 1)},
		fakeBindings{"n1#1": {NodeID: "n1", Attempt: 1, SessionID: "sess-1"}}
}

// noAttemptNode 是还没开过工的节点：不知道该进哪一次 attempt。
func noAttemptNode() []projectctl.Node {
	n := nodeWith("n1", projectctl.StateReady, "frontier")
	n.Attempt = projectctl.Unknown[int]("还没开过工")
	return []projectctl.Node{n}
}

func refusalCases() []refusalCase {
	// 找会话那四个出口，两条路各走一遍，共八个。
	sessionOutlets := []struct {
		name  string
		nodes []projectctl.Node
		binds Bindings
		want  string
		act   string
	}{
		{
			name:  "没接绑定存储",
			nodes: []projectctl.Node{runningNode("n1", 1)},
			binds: nil,
			want:  "没有配置会话绑定",
			act:   "config.json",
		},
		{
			name:  "不知道是第几次attempt",
			nodes: noAttemptNode(),
			binds: fakeBindings{},
			want:  "还没开过工",
			act:   "按 d",
		},
		{
			name:  "这个节点没有绑定",
			nodes: []projectctl.Node{runningNode("n1", 1)},
			binds: fakeBindings{},
			want:  "没有 Work Deck 派出的会话",
			act:   "按 d",
		},
		{
			name:  "绑定里没有会话号",
			nodes: []projectctl.Node{runningNode("n1", 1)},
			binds: fakeBindings{"n1#1": {NodeID: "n1", Attempt: 1}},
			want:  "这条绑定是坏的",
			act:   "按 d",
		},
	}

	var cases []refusalCase
	for _, key := range []rune{'a', 'p'} {
		for _, o := range sessionOutlets {
			o := o
			key := key
			label := "进入"
			if key == 'p' {
				label = "预览"
			}
			cases = append(cases, refusalCase{
				name: fmt.Sprintf("%s-%s", label, o.name),
				key:  key,
				setup: func(t *testing.T) Model {
					return modelWithAttacher(t, o.nodes, &fakeAttacher{}, o.binds)
				},
				wantContains: o.want,
				wantAction:   o.act,
			})
		}
	}

	nodes, binds := boundNode()

	// 两条路各自专有的出口。
	cases = append(cases,
		refusalCase{
			name: "进入-没选中任何节点",
			key:  'a',
			setup: func(t *testing.T) Model {
				return modelWithAttacher(t, nil, &fakeAttacher{}, binds)
			},
			wantContains: "没有选中任何节点",
			wantAction:   "↑↓",
		},
		refusalCase{
			name: "预览-没选中任何节点",
			key:  'p',
			setup: func(t *testing.T) Model {
				return modelWithAttacher(t, nil, &fakeAttacher{}, binds)
			},
			wantContains: "没有选中任何节点",
			wantAction:   "↑↓",
		},
		refusalCase{
			name: "进入-没配置进入能力",
			key:  'a',
			setup: func(t *testing.T) Model {
				m := modelWithAttacher(t, nodes, &fakeAttacher{}, binds)
				m.attacher = nil
				return m
			},
			wantContains: "没有配置进入会话",
			wantAction:   "config.json",
		},
		refusalCase{
			name: "预览-没配置预览能力",
			key:  'p',
			setup: func(t *testing.T) Model {
				m := modelWithAttacher(t, nodes, &fakeAttacher{}, binds)
				m.attacher = nil
				return m
			},
			wantContains: "没有配置会话预览",
			wantAction:   "config.json",
		},
		refusalCase{
			name: "进入-造不出进入命令",
			key:  'a',
			setup: func(t *testing.T) Model {
				return modelWithAttacher(t, nodes,
					&fakeAttacher{cmdErr: fmt.Errorf("session attach 不在白名单里")}, binds)
			},
			wantContains: "进不去",
			wantAction:   "按 p",
		},
		refusalCase{
			name: "进入-从会话返回时报错",
			key:  'a',
			setup: func(t *testing.T) Model {
				return modelWithAttacher(t, nodes, &fakeAttacher{}, binds)
			},
			viaMsg: func(m Model) tea.Msg {
				return attachDoneMsg{sessionID: "sess-1", err: fmt.Errorf("终端被别人占着")}
			},
			wantContains: "进入过程报错",
			wantAction:   "按 p",
		},
		refusalCase{
			name: "预览-取不到画面",
			key:  'p',
			setup: func(t *testing.T) Model {
				return modelWithAttacher(t, nodes, &fakeAttacher{textErr: fmt.Errorf("会话没了")}, binds)
			},
			viaMsg: func(m Model) tea.Msg {
				return previewDoneMsg{sessionID: "sess-1", err: fmt.Errorf("会话没了")}
			},
			wantContains: "预览取不到",
			wantAction:   "按 r",
		},
	)
	return cases
}

// 每一条拒绝都要说清楚是哪一条，并且给出照着做就能往下走的动作。
func TestEveryAttachAndPreviewRefusalIsCoveredAndActionable(t *testing.T) {
	cases := refusalCases()

	// 口径对不上就先改口径，别改数字。
	const wantOutlets = 15
	if len(cases) != wantOutlets {
		t.Fatalf("这张表列了 %d 个拒绝出口，口径说的是 %d 个：先把口径和表对齐", len(cases), wantOutlets)
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := c.setup(t)
			var after Model
			if c.viaMsg != nil {
				next, _ := m.Update(c.viaMsg(m))
				after = next.(Model)
			} else {
				after, _ = press(m, c.key)
			}
			if after.noticeOK {
				t.Fatalf("这是一条拒绝，却标成了成功：%q", after.notice)
			}
			if !strings.Contains(after.notice, c.wantContains) {
				t.Fatalf("提示认不出是哪个出口，期望含 %q，实际 %q", c.wantContains, after.notice)
			}
			// 每条拒绝都必须给出下一步，一条都不放过。
			//
			// 这里原先写成「wantAction 非空时才检查」，而表里有一条把它留了空——
			// 等于自己给自己开了个豁免，再宣称 15 条全部可操作。
			// 独立评审把这条挑了出来，是对的。
			if c.wantAction == "" {
				t.Fatalf("用例 %q 没写期望的下一步：这张表不接受豁免", c.name)
			}
			if !strings.Contains(after.notice, c.wantAction) {
				t.Errorf("拒绝没给下一步，期望含 %q，实际 %q", c.wantAction, after.notice)
			}
		})
	}
}

// 找会话那一段是两条路共用的，确认两边真的走的是同一段实现。
//
// 共用这件事是上面那张表「15 个出口落在 11 个分支上」的依据，得测出来，
// 不能只写在注释里。
func TestAttachAndPreviewShareTheSameSessionLookup(t *testing.T) {
	nodes := []projectctl.Node{runningNode("n1", 1)}
	binds := fakeBindings{}

	viaA, _ := press(modelWithAttacher(t, nodes, &fakeAttacher{}, binds), 'a')
	viaP, _ := press(modelWithAttacher(t, nodes, &fakeAttacher{}, binds), 'p')

	if viaA.notice != viaP.notice {
		t.Errorf("同一个原因，两条路给的话不一样：\n进入：%q\n预览：%q", viaA.notice, viaP.notice)
	}
}

// 预览这条路只读：拒绝也好成功也好，都不许去动会话。
func TestPreviewNeverAttaches(t *testing.T) {
	nodes, binds := boundNode()
	fa := &fakeAttacher{text: "画面"}
	m := modelWithAttacher(t, nodes, fa, binds)
	_, cmd := press(m, 'p')
	if cmd != nil {
		cmd()
	}
	if len(fa.attached) != 0 {
		t.Errorf("预览把会话接管了：%v", fa.attached)
	}
}


