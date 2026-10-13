package deckui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// n055：项目标题行也能选；新建、还没有会话的项目能用键盘选到，按 m 为它起主 agent。
func TestSidebarProjectRowsSelectable(t *testing.T) {
	d := sample()
	d.Projects = append(d.Projects, deckstore.ProjectDef{ID: "trial", Name: "试用项目"})
	sb := &Sidebar{Data: d, Mode: "none"}
	es := sb.Data.Entries()
	// deck 标题、3 个会话、wiki 标题、1 个会话、trial 标题
	if len(es) != 7 || es[0] != (Entry{"deck", -1}) || es[4] != (Entry{"wiki", -1}) || es[6] != (Entry{"trial", -1}) {
		t.Fatalf("先后：%+v", es)
	}
	// 从第一个会话往上一行到 deck 标题，一路往下走到最后的空项目
	sb.Sel = 0
	sb.Move(-1)
	if sb.Proj != "deck" || sb.SelectedProject() != "deck" {
		t.Fatalf("上移到项目标题：%q", sb.Proj)
	}
	sb.Move(1)
	if sb.Proj != "" || sb.Sel != 0 || sb.SelectedProject() != "deck" {
		t.Fatalf("下移回第一个会话：%q %d", sb.Proj, sb.Sel)
	}
	sb.Move(100)
	if sb.Proj != "trial" || sb.SelectedProject() != "trial" {
		t.Fatalf("移到底是空项目的标题：%q", sb.Proj)
	}
	sb.Move(100)
	if sb.Proj != "trial" {
		t.Fatal("到底不再动")
	}
	f := sb.Render(48, 40)
	all := ansi.Strip(strings.Join(f.Lines, "\n"))
	if !strings.Contains(all, "试用项目 · 0") || !strings.Contains(all, "按 m 起主 agent") || !strings.Contains(all, " m 起主 agent") {
		t.Fatalf("空项目要提示怎么起主 agent：\n%s", all)
	}
	// 侧栏实际只有三十来列宽：提示与结果说明都不能被截掉（n055 端到端发现）
	narrow := &Sidebar{Data: d, Mode: "none", Proj: "trial"}
	narrow.Data.Info = "已起「trial」的主 agent：CLI 停在目录信任确认上：请在中间窗格自己选，选完 deck 会自动送第一条 brief"
	nf := ansi.Strip(strings.Join(narrow.Render(32, 40).Lines, "\n"))
	if !strings.Contains(nf, "按 m 起主 agent") || !strings.Contains(nf, " m 起主 agent") || !strings.Contains(strings.Join(strings.Fields(nf), ""), "选完deck会自动送第一条brief") {
		t.Fatalf("32 列宽时提示与说明要完整：\n%s", nf)
	}
	// 选中的标题行有底色、点它命中项目
	row := -1
	for i, l := range f.Lines {
		if strings.Contains(ansi.Strip(l), "试用项目 · 0") {
			row = i
			if !strings.Contains(l, "\x1b[48;5;236m") {
				t.Fatalf("选中的项目标题要有底色：%q", l)
			}
		}
	}
	if p, ok := sb.HitProject(row); !ok || p != "trial" {
		t.Fatalf("点标题行命中项目：%q %v", p, ok)
	}
	if _, _, ok := sb.Hit(10, row); ok {
		t.Fatal("标题行不是会话")
	}
	// 选中项目时，会话行不再高亮
	for _, l := range f.Lines {
		if strings.Contains(ansi.Strip(l), "写测试") && strings.Contains(l, "\x1b[48;5;236m") {
			t.Fatal("选中项目标题时会话行不该高亮")
		}
	}
}

func TestStartMainText(t *testing.T) {
	cases := []struct {
		r    StartMainResult
		err  error
		want string
		ok   bool
	}{
		{StartMainResult{}, &deckclient.Error{Status: 409, Message: "项目 trial 已有主 agent"}, "已有主 agent", false},
		{StartMainResult{Pending: "codex_trust"}, nil, "右栏批准", true},
		{StartMainResult{Note: "CLI 停在目录信任确认上：请在中间窗格自己选"}, nil, "请在中间窗格自己选", true},
		{StartMainResult{BriefDelivered: true}, nil, "已起「trial」的主 agent", true},
		{StartMainResult{}, errors.New("连不上\x1b[31m"), "连不上", false},
	}
	for i, c := range cases {
		got, ok := startMainText("trial", c.r, c.err)
		if !strings.Contains(got, c.want) || ok != c.ok || strings.Contains(got, "\x1b") {
			t.Errorf("第 %d 个：%q %v", i, got, ok)
		}
	}
}

// 自查：新部署还没有任何会话时，默认选中第一个项目（按 m 就能起）；选中的项目被删掉后回到会话。
func TestSidebarDefaultsToFirstProject(t *testing.T) {
	sb := &Sidebar{Data: Data{Projects: []deckstore.ProjectDef{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}}}, Mode: "none"}
	if sb.SelectedProject() != "a" {
		t.Fatalf("没有会话时选中第一个项目：%q", sb.SelectedProject())
	}
	sb.Move(1)
	if sb.Proj != "b" {
		t.Fatalf("↓ 到第二个项目，不跳过：%q", sb.Proj)
	}
	f := sb.Render(32, 20)
	if !strings.Contains(ansi.Strip(strings.Join(f.Lines, "\n")), "B · 0") {
		t.Fatal("渲染")
	}
	d := sample()
	sb = &Sidebar{Data: d, Mode: "none", Proj: "gone"}
	if sb.SelectedProject() != "deck" || sb.Proj != "" {
		t.Fatalf("选中的项目没了：回到会话（第一个会话属于 deck）：%q %q", sb.SelectedProject(), sb.Proj)
	}
}
