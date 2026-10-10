package deckui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/viewstate"
)

func keys(evs []Event) string {
	var out []string
	for _, e := range evs {
		if e.Mouse != nil {
			out = append(out, "mouse")
		} else {
			out = append(out, e.Key)
		}
	}
	return strings.Join(out, ",")
}

func TestParseKeys(t *testing.T) {
	evs, rest := Parse([]byte("\x1b[A\x1bOB\r\x1bOQ\x1b[15~\x1b[21~h你\x7f\t"), false)
	if got := keys(evs); got != "up,down,enter,f2,f5,f10,h,你,backspace,tab" || len(rest) != 0 {
		t.Fatalf("按键：%s（剩 %q）", got, rest)
	}
	// 不完整的转义序列与半个汉字留到下一段
	evs, rest = Parse([]byte("a\x1b[1"), false)
	if keys(evs) != "a" || string(rest) != "\x1b[1" {
		t.Fatalf("不完整序列应留下：%s %q", keys(evs), rest)
	}
	evs, rest = Parse([]byte("你")[:2], false)
	if len(evs) != 0 || len(rest) != 2 {
		t.Fatal("半个汉字应留下")
	}
	evs, _ = Parse([]byte("\x1b"), false)
	if len(evs) != 0 {
		t.Fatal("单独 ESC 不 flush 时应等")
	}
	evs, _ = Parse([]byte("\x1b"), true)
	if keys(evs) != "esc" {
		t.Fatal("flush 后单独 ESC 是 esc 键")
	}
}

func TestParseMouse(t *testing.T) {
	evs, _ := Parse([]byte("\x1b[<0;5;3M\x1b[<0;5;3m\x1b[<64;1;1M\x1b[<65;1;1M\x1b[<32;9;9M"), false)
	if len(evs) != 4 {
		t.Fatalf("应有 4 个鼠标事件（拖动丢弃）：%d", len(evs))
	}
	if m := evs[0].Mouse; m.X != 4 || m.Y != 2 || !m.Press || m.Button != 0 {
		t.Fatalf("左键按下（坐标从 0 起）：%+v", m)
	}
	if evs[1].Mouse.Press {
		t.Fatal("小写 m 是松开")
	}
	if evs[2].Mouse.Wheel != -1 || evs[3].Mouse.Wheel != 1 {
		t.Fatal("滚轮方向")
	}
}

func TestFrameEncode(t *testing.T) {
	out := Frame{Lines: []string{"abcdefghij", "你好世界"}, Images: []Image{{Row: 1, Col: 2, Data: "<IMG>"}}}.Encode(5, 3)
	if !strings.Contains(out, "abcde\x1b[0m\x1b[K") || strings.Contains(out, "abcdef") {
		t.Fatalf("应截到 5 列：%q", out)
	}
	if !strings.Contains(out, "你好\x1b[0m\x1b[K") {
		t.Fatalf("中文按两列截：%q", out)
	}
	if strings.Count(out, "\r\n") != 2 {
		t.Fatal("3 行屏应有 2 个换行（多余行也清掉）")
	}
	if !strings.Contains(out, "\x1b[2;3H<IMG>") || strings.Index(out, "<IMG>") < strings.LastIndex(out, "\x1b[K") {
		t.Fatal("图在文字之后、按行列定位")
	}
}

func sample() Data {
	t0 := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	s := func(key, id, project, role, title, tool, status, act string, at int) SessionInfo {
		return SessionInfo{Session: deckstore.Session{Key: key, AgentDeckID: id, TmuxSession: "agentdeck_" + id, Project: project,
			Role: role, Title: title, Tool: tool, Status: status, CreatedAt: t0.Add(time.Duration(at) * time.Minute)}, Activity: act}
	}
	return Data{
		Projects: []deckstore.ProjectDef{{ID: "deck", Name: "Agent Deck"}, {ID: "wiki", Name: "Wiki"}},
		Sessions: []SessionInfo{
			s("k2", "sub1", "deck", "sub", "写测试", "codex", deckstore.StatusRunning, "running", 2),
			s("k1", "main1", "deck", "main", "主 agent", "claude", deckstore.StatusRunning, "waiting", 1),
			s("k3", "", "deck", "sub", "启动中的", "grok", deckstore.StatusLaunching, "", 3),
			s("k4", "w1", "wiki", "main", "wiki 主\x1b]0;evil\x07 sk-abcdefghijklmnopqrst", "kimi", deckstore.StatusRunning, "idle", 4),
		},
		View: viewstate.State{Open: []string{"main1", "sub1"}, Focus: "sub1", TakenOver: []string{"sub1"}},
	}
}

func TestSidebarGroupsAndMarks(t *testing.T) {
	sb := &Sidebar{Data: sample(), Mode: "none"}
	f := sb.Render(40, 30)
	plain := make([]string, len(f.Lines))
	for i, l := range f.Lines {
		plain[i] = ansi.Strip(l)
	}
	all := strings.Join(plain, "\n")
	order := []string{"Agent Deck · 3", "★ 主 agent", "写测试", "启动中的", "Wiki · 1", "wiki 主"}
	pos := 0
	for _, want := range order {
		i := strings.Index(all[pos:], want)
		if i < 0 {
			t.Fatalf("缺少或顺序不对：%q\n%s", want, all)
		}
		pos += i
	}
	if strings.Contains(all, "evil") || strings.Contains(all, "abcdefghijklmnopqrst") || strings.Contains(strings.Join(f.Lines, ""), "\x1b]0;") {
		t.Fatal("标题里的转义序列与疑似密钥应被清洗")
	}
	for _, want := range []string{"claude · 等你输入", "codex · 在跑 · 接管中", "grok · 启动中", "kimi · 空闲"} {
		if !strings.Contains(all, want) {
			t.Fatalf("缺状态说明 %q\n%s", want, all)
		}
	}
	// 焦点会话（写测试）左边有竖线，打开的会话眼睛是 ◉，没打开的是 ○
	for i, l := range plain {
		switch {
		case strings.Contains(l, "写测试"):
			if !strings.HasPrefix(l, "▌") || !strings.HasSuffix(strings.TrimRight(l, " "), "◉") {
				t.Fatalf("焦点会话行：%q", l)
			}
		case strings.Contains(l, "wiki 主"):
			if !strings.HasSuffix(strings.TrimRight(l, " "), "○") {
				t.Fatalf("没打开的会话：%q", l)
			}
		}
		_ = i
	}
	if len(f.Images) != 0 {
		t.Fatal("none 模式不画图")
	}
}

func TestSidebarHitTesting(t *testing.T) {
	sb := &Sidebar{Data: sample(), Mode: "none"}
	f := sb.Render(40, 30)
	row := -1
	for i, l := range f.Lines {
		if strings.Contains(ansi.Strip(l), "写测试") {
			row = i
		}
	}
	idx, eye, ok := sb.Hit(10, row)
	list := sb.Data.Ordered()
	if !ok || eye || list[idx].Key != "k2" {
		t.Fatalf("点标题应命中「写测试」：%d %v %v", idx, eye, ok)
	}
	if _, eye, _ := sb.Hit(39, row); !eye {
		t.Fatal("点行尾应是眼睛")
	}
	if idx2, eye2, ok2 := sb.Hit(10, row+1); !ok2 || eye2 || idx2 != idx {
		t.Fatal("第二行也属于同一个会话，且没有眼睛")
	}
	if _, _, ok := sb.Hit(5, 0); ok {
		t.Fatal("标题行不是会话")
	}
	if Openable(list[2]) {
		t.Fatal("还没建好的会话不能打开")
	}
}

func TestSidebarSixelReservesSpaceAndPlacesImages(t *testing.T) {
	sb := &Sidebar{Data: sample(), Mode: "sixel", CellW: 9, CellH: 18}
	f := sb.Render(40, 30)
	if len(f.Images) != 3 { // claude、codex、kimi 有素材；grok 也有——启动中的会话同样画
		if len(f.Images) != 4 {
			t.Fatalf("有素材的会话都应画图：%d", len(f.Images))
		}
	}
	for _, im := range f.Images {
		if im.Col != 1 || !strings.HasPrefix(im.Data, "\x1bP0;1;0q\"1;1;36;36") {
			t.Fatalf("图应在第 1 列、按两行高（36px）的正方形：%+v", im.Data[:20])
		}
		if !strings.HasPrefix(string([]rune(ansi.Strip(f.Lines[im.Row]))[1:]), "    ") {
			t.Fatalf("图的位置应留空：%q", ansi.Strip(f.Lines[im.Row]))
		}
	}
	blocks := (&Sidebar{Data: sample(), Mode: "blocks"}).Render(40, 30)
	if !strings.Contains(strings.Join(blocks.Lines, ""), "▀") || len(blocks.Images) != 0 {
		t.Fatal("色块模式用半格字符、不画图")
	}
}

func TestSidebarScrollKeepsSelectionVisible(t *testing.T) {
	d := sample()
	for i := 0; i < 20; i++ {
		x := d.Sessions[0]
		x.Key, x.AgentDeckID, x.Title = "kx"+string(rune('a'+i)), "x"+string(rune('a'+i)), "多余"+string(rune('a'+i))
		d.Sessions = append(d.Sessions, x)
	}
	sb := &Sidebar{Data: d, Mode: "none"}
	sb.Sel = len(sb.Data.Ordered()) - 1
	f := sb.Render(40, 12)
	last := sanitize(sb.Data.Ordered()[sb.Sel].Title)
	if !strings.Contains(ansi.Strip(strings.Join(f.Lines, "\n")), last) {
		t.Fatalf("选中的最后一个会话应滚进屏：%q", last)
	}
	if len(f.Lines) != 12 {
		t.Fatalf("不应超过屏高：%d", len(f.Lines))
	}
}

func TestTabsRenderHitAndHints(t *testing.T) {
	tb := &Tabs{Data: sample()}
	f := tb.Render(80)
	line := ansi.Strip(f.Lines[0])
	if !strings.Contains(line, "主 agent") || !strings.Contains(line, "写测试 ✋") || !strings.Contains(line, "你在操作") {
		t.Fatalf("标签条：%q", line)
	}
	if ansi.StringWidth(f.Lines[0]) != 80 {
		t.Fatalf("应正好占满一行：%d", ansi.StringWidth(f.Lines[0]))
	}
	id, closeIt, ok := tb.Hit(3)
	if !ok || closeIt || id != "main1" {
		t.Fatalf("点第一个标签：%s %v %v", id, closeIt, ok)
	}
	first := tb.hits[0]
	if id, closeIt, _ := tb.Hit(first.close); !closeIt || id != "main1" {
		t.Fatal("点 ✕ 应是关闭")
	}
	// 提示随状态变化
	d := sample()
	d.View.TakenOver = nil
	if !strings.Contains(ansi.Strip((&Tabs{Data: d}).Hint()), "F2 接管") {
		t.Fatal("子 agent 只读时提示 F2 接管")
	}
	d.View.TakenOver, d.View.HandbackPending = []string{"sub1"}, []string{"sub1"}
	if !strings.Contains(ansi.Strip((&Tabs{Data: d}).Hint()), "F2 再接管") {
		t.Fatal("待交还时提示 F2 再接管")
	}
	d.View.Focus = "main1"
	if (&Tabs{Data: d}).Hint() != "" {
		t.Fatal("主 agent 不需要接管提示")
	}
	if !strings.Contains(ansi.Strip((&Tabs{Data: Data{}}).Hint()), "左栏点一个会话") {
		t.Fatal("没打开任何会话时的提示")
	}
	// 太窄时截断，不超宽
	if w := ansi.StringWidth((&Tabs{Data: sample()}).Render(30).Lines[0]); w > 30 {
		t.Fatalf("窄屏不应超宽：%d", w)
	}
}
