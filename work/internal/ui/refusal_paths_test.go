package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

// 这个文件把「按 a 进入」「按 T 直连」「按 p 预览」三条路上所有的拒绝出口
// 列全，逐条测。
//
// 口径说清楚，免得再数错一次：
//
//	按用户可见的出口数：进入 8 个，直连 7 个，预览 7 个，一共 22 个。
//	其中找会话那 4 个（没接绑定存储、不知道第几次 attempt、没有绑定、绑定里没会话号）
//	是三条路共用的同一段实现 sessionOf，所以 22 个出口落在 14 个不同的分支上。
//
// 这个数字变过两次：最早报告写「九条」，既没说清按出口还是按分支，数也不对；
// 改成 15 / 11 之后，attempt 6 加了按 T 直连这条路，又成了 22 / 14。
// 数字不是重点，口径是——下面这张表就是口径的全集，
// 每条都要求提示里带一个能照着做的下一步，没有豁免。

// refusalCase 是一个拒绝出口：怎么把界面摆成那个样子，按哪个键，该看到什么。
type refusalCase struct {
	name string
	// key 是触发的按键：'a' 进入，'T' 直连 tmux，'p' 预览。
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
	// 找会话那四个出口，三条路各走一遍，共十二个。
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
	for _, key := range []rune{'a', 'T', 'p'} {
		for _, o := range sessionOutlets {
			o := o
			key := key
			label := map[rune]string{'a': "进入", 'T': "直连", 'p': "预览"}[key]
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

	// 三条路各自专有的出口。
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
			name: "直连-没选中任何节点",
			key:  'T',
			setup: func(t *testing.T) Model {
				return modelWithAttacher(t, nil, &fakeAttacher{}, binds)
			},
			wantContains: "没有选中任何节点",
			wantAction:   "↑↓",
		},
		refusalCase{
			name: "直连-没配置进入能力",
			key:  'T',
			setup: func(t *testing.T) Model {
				m := modelWithAttacher(t, nodes, &fakeAttacher{}, binds)
				m.attacher = nil
				return m
			},
			wantContains: "没有配置进入会话",
			wantAction:   "config.json",
		},
		refusalCase{
			name: "直连-造不出直连命令",
			key:  'T',
			setup: func(t *testing.T) Model {
				return modelWithAttacher(t, nodes,
					&fakeAttacher{directErr: fmt.Errorf("菜单里没有这个会话的 tmux 名")}, binds)
			},
			wantContains: "直连 tmux 也进不去",
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
	const wantOutlets = 22
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
			// 等于自己给自己开了个豁免，再宣称全部可操作。
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

// 找会话那一段是三条路共用的，确认三边真的走的是同一段实现。
//
// 共用这件事是上面那张表「22 个出口落在 14 个分支上」的依据，得测出来，
// 不能只写在注释里。
func TestAttachDirectAndPreviewShareTheSameSessionLookup(t *testing.T) {
	nodes := []projectctl.Node{runningNode("n1", 1)}
	binds := fakeBindings{}

	viaA, _ := press(modelWithAttacher(t, nodes, &fakeAttacher{}, binds), 'a')
	viaT, _ := press(modelWithAttacher(t, nodes, &fakeAttacher{}, binds), 'T')
	viaP, _ := press(modelWithAttacher(t, nodes, &fakeAttacher{}, binds), 'p')

	if viaA.notice != viaP.notice || viaA.notice != viaT.notice {
		t.Errorf("同一个原因，三条路给的话不一样：\n进入：%q\n直连：%q\n预览：%q",
			viaA.notice, viaT.notice, viaP.notice)
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



// 按 T 走的是直连，不碰上游 CLI 那条路。
//
// 这条路存在的理由：上游 headless 服务对菜单快照做内存缓存，
// CLI 刚起的会话它可能一直看不到，而 tmux 里那个会话是好好的。
//
// 入口是显式按键，owner 随时可按。attempt 4 那个自动退路不是这样：
// 它挂在 AttachCommand 报错上，而那件事在生产里不会发生，于是整段是死代码。
// 加一条新路之前先问「生产里什么条件下会走到这里」，答不上来就别加。
func TestDirectAttachUsesTmuxNotTheUpstreamCLI(t *testing.T) {
	nodes, binds := boundNode()
	fa := &fakeAttacher{}
	after, cmd := press(modelWithAttacher(t, nodes, fa, binds), 'T')

	if cmd == nil {
		t.Fatal("按 T 应当真的进去")
	}
	if !after.noticeOK {
		t.Errorf("直连走通了却报成拒绝：%q", after.notice)
	}
	if !strings.Contains(after.notice, "直连 tmux") {
		t.Errorf("没告诉人这次绕开了上游：%q", after.notice)
	}
	// 前缀键的话不能丢——嵌套 tmux 下这是进去之前唯一的提示。
	if !strings.Contains(after.notice, "C-b") {
		t.Errorf("直连时把前缀键提示弄丢了：%q", after.notice)
	}
	if len(fa.direct) != 1 {
		t.Errorf("直连应当恰好走一次，实际 %d 次", len(fa.direct))
	}
	if len(fa.attached) != 0 {
		t.Errorf("按 T 却去走了上游 CLI 那条路：%v", fa.attached)
	}
}

// 按 a 走上游那条，不许顺手去碰直连。
func TestAttachDoesNotTouchDirectPath(t *testing.T) {
	nodes, binds := boundNode()
	fa := &fakeAttacher{}
	if _, cmd := press(modelWithAttacher(t, nodes, fa, binds), 'a'); cmd == nil {
		t.Fatal("按 a 应当能进去")
	}
	if len(fa.direct) != 0 {
		t.Errorf("按 a 却走了直连：%v", fa.direct)
	}
	if len(fa.attached) != 1 {
		t.Errorf("应当走上游 CLI 那条路一次，实际 %d 次", len(fa.attached))
	}
}

// 主路返回报错时，要告诉人还有 T 这条路可试。
func TestAttachFailurePointsAtTheDirectPath(t *testing.T) {
	nodes, binds := boundNode()
	m := modelWithAttacher(t, nodes, &fakeAttacher{}, binds)
	next, _ := m.Update(attachDoneMsg{sessionID: "sess-1", err: fmt.Errorf("上游没认出这个会话")})
	after := next.(Model)
	if !strings.Contains(after.notice, "按 T") {
		t.Errorf("主路失败了却没提直连这条路：%q", after.notice)
	}
}

// 界面底下那行按键说明，得把能按的都写上。
//
// 上一版加了直连能力却没有入口，这一版有了入口——说明行漏了它的话，
// owner 照样不知道有这条路。
func TestKeyHintMentionsEveryKeyTheScreenHandles(t *testing.T) {
	nodes, binds := boundNode()
	out := RenderScreen(modelWithAttacher(t, nodes, &fakeAttacher{}, binds).snap, 120, Screen{})
	for _, k := range []string{"d 派工", "a 进入", "T 直连", "p 预览", "r 刷新", "q 退出"} {
		if !strings.Contains(out, k) {
			t.Errorf("按键说明里没写 %q", k)
		}
	}
}

// adapter 退到次一等的取数来源时，界面必须把代价说出来。
//
// 按 T 的主场景恰好是「服务的菜单看不见这个会话」，那时 adapter 只能从 list
// 取会话名。list 不含 socket 名，socket 是靠扫本机 tmux server、
// 按会话身份认出来的——走了哪条来源、最后连的是哪个 server，都得在进去之前讲，
// 进去之后界面就不在屏幕上了。
func TestDirectAttachTellsYouWhenItFellBack(t *testing.T) {
	nodes, binds := boundNode()
	fa := &fakeAttacher{directNote: "菜单里没有这个会话，已改从 list 取 tmux 会话名，" +
		"并扫描本机 tmux server 按会话身份确认目标只在一处，连的是默认 tmux server。"}
	after, cmd := press(modelWithAttacher(t, nodes, fa, binds), 'T')

	if cmd == nil {
		t.Fatal("降级之后仍然应当能进去")
	}
	if !strings.Contains(after.notice, "改从 list 取") {
		t.Errorf("降级了却没告诉人：%q", after.notice)
	}
	// 说明里要讲清连的是哪个 server，以及这个结论是扫出来的。
	if !strings.Contains(after.notice, "只在一处") {
		t.Errorf("没说清目标唯一性是怎么确定的：%q", after.notice)
	}
	if !strings.Contains(after.notice, "连的是") {
		t.Errorf("没说清最后连的是哪个 server：%q", after.notice)
	}
	// 前缀键的话还得在。
	if !strings.Contains(after.notice, "C-b") {
		t.Errorf("降级说明把前缀键提示挤掉了：%q", after.notice)
	}
}

// 走的是最准那条来源时，不要平白多说一句降级的话。
func TestDirectAttachStaysQuietWhenItDidNotFallBack(t *testing.T) {
	nodes, binds := boundNode()
	fa := &fakeAttacher{} // directNote 为空
	after, _ := press(modelWithAttacher(t, nodes, fa, binds), 'T')
	if strings.Contains(after.notice, "list") {
		t.Errorf("没降级却说了降级的话：%q", after.notice)
	}
}
