package panels

import (
	"fmt"
	"math"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckd"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// ---- 对抗测试（n069adv）----

func tryRender(p deckd.Progress, w int) (out string, panicked any) {
	defer func() { panicked = recover() }()
	return RenderProgressW(p, w), nil
}

// 3a 畸形输入：负的 Unknown / 负的计数 → strings.Repeat 负数 panic（面板进程崩）。
func TestAdvRenderNegativeCountsPanic(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	cases := map[string]deckd.Progress{
		"Unknown<0":   {Project: "p", Graph: true, Unknown: -5, Counts: map[string]int{"accepted": 10}},
		"count<0":     {Project: "p", Graph: true, Counts: map[string]int{"accepted": 10, "planned": -3}},
		"accepted<0":  {Project: "p", Graph: true, Counts: map[string]int{"accepted": -4, "failed": 9}},
		"total==0 混合": {Project: "p", Graph: true, Unknown: -2, Counts: map[string]int{"accepted": 2}},
	}
	for name, p := range cases {
		for _, w := range []int{40, 8} {
			if _, pan := tryRender(p, w); pan != nil {
				t.Errorf("%s 宽 %d：panic：%v", name, w, pan)
			}
		}
	}
}

// 3b 宽度/计数/名字的极端值，不该崩，分段条有界。
func TestAdvRenderExtremes(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	ten := 10.0
	long := strings.Repeat("项目名", 400)
	widths := []int{math.MinInt, -1 << 40, -100, -1, 0, 1, 2, 3, 9, 10, 11, 12, 59, 60, 61, 62, 63, 1000, 1 << 20}
	progs := map[string]deckd.Progress{
		"zero":        {Project: "p", Graph: true},
		"nilcounts":   {Project: "p", Graph: true, Counts: nil},
		"unknownkey":  {Project: "p", Graph: true, Counts: map[string]int{"weird": 3, "accepted": 1}},
		"onlyunknown": {Project: "p", Graph: true, Unknown: 7},
		"big":         {Project: "p", Graph: true, Counts: map[string]int{"accepted": 1 << 30, "failed": 1, "blocked": 1 << 29}},
		"longname":    {Project: long, Graph: true, Counts: map[string]int{"accepted": 3}},
		"nilbudgets":  {Project: "p", Graph: true, Budgets: []deckd.BudgetView{{Label: "x"}, {Label: "y", Limit: &ten}, {Label: "z", Used: &ten}}},
		"follow-nil":  {Follow: true},
	}
	for name, p := range progs {
		for _, w := range widths {
			if out, pan := tryRender(p, w); pan != nil {
				t.Errorf("%s 宽 %d：panic：%v", name, w, pan)
			} else if len(out) > 1<<20 {
				t.Errorf("%s 宽 %d：输出 %d 字节", name, w, len(out))
			}
		}
	}
}

// 3b' 非常大的计数：c*n 溢出 int64 → 分段条给错/负格。只在畸形线上数据才会出现（真实节点数不会到 1e17）：推测级。
func TestAdvSegmentCellsOverflow(t *testing.T) {
	got := segmentCells([]int{math.MaxInt64 / 10, math.MaxInt64 / 10, 0, 0}, 40)
	t.Logf("%v", got)
	sum := 0
	for _, g := range got {
		if g < 0 {
			t.Errorf("溢出出现负格：%v", got)
		}
		sum += g
	}
	if sum != 40 {
		t.Errorf("总格数 %d != 40：%v", sum, got)
	}
}

// 3c segmentCells 不变量：非负输入、n 0..80，随机 + 悬殊。
func TestAdvSegmentCellsInvariants(t *testing.T) {
	rng := rand.New(rand.NewSource(69))
	check := func(counts []int, n int) {
		got := segmentCells(counts, n)
		total, nz, sum := 0, 0, 0
		for i, c := range counts {
			total += c
			if c > 0 {
				nz++
			}
			if got[i] < 0 {
				t.Fatalf("%v/%d 负：%v", counts, n, got)
			}
			if c == 0 && got[i] != 0 {
				t.Fatalf("%v/%d 零段拿到格：%v", counts, n, got)
			}
			sum += got[i]
		}
		want := n
		if total == 0 || n <= 0 {
			want = 0
		}
		if sum != want {
			t.Fatalf("%v/%d 总格 %d != %d：%v", counts, n, sum, want, got)
		}
		if n >= nz && total > 0 {
			for i, c := range counts {
				if c > 0 && got[i] == 0 {
					t.Fatalf("%v/%d 非零段 %d 没格：%v", counts, n, i, got)
				}
			}
		}
		// 单调性：更多节点的段不该比更少的段格子少（允许保底 1 格造成的借位：相差不超过 1）
		for i := range counts {
			for j := range counts {
				if counts[i] > counts[j] && got[i]+1 < got[j] {
					t.Fatalf("%v/%d 单调性：%v", counts, n, got)
				}
			}
		}
	}
	for it := 0; it < 200000; it++ {
		n := rng.Intn(81)
		counts := make([]int, 4)
		for i := range counts {
			switch rng.Intn(4) {
			case 0:
			case 1:
				counts[i] = rng.Intn(3)
			case 2:
				counts[i] = rng.Intn(100)
			default:
				counts[i] = rng.Intn(100000)
			}
		}
		check(counts, n)
	}
	for _, n := range []int{1, 2, 3, 4, 5, 8, 60} {
		check([]int{1, 1, 1, 1000000}, n)
		check([]int{1000000, 1, 1, 1}, n)
		check([]int{1, 0, 0, 1}, n)
	}
}

// 3d 项目名/问题里的转义序列与伪造打码标记：面板输出里不该有 lipgloss 之外的控制序列。
func TestAdvRenderControlSequencesAndForgedMark(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	evil := "\x1b]0;PWNED\x07\x1b[2J\x1b[31mred"
	p := deckd.Progress{Project: "p" + evil, Graph: true, Counts: map[string]int{"accepted": 1},
		Problems: []string{"读取失败 " + evil + i18n.RedactMark + "\r\n伪造第二行"}}
	out := RenderProgressW(p, 40)
	stripped := ansi.Strip(out)
	t.Logf("输出（%%q）：%q", out)
	if strings.Contains(stripped, "\x1b") || strings.ContainsRune(stripped, 0x07) {
		t.Errorf("面板把外部文字里的控制序列原样输出：%q", stripped)
	}
	if strings.ContainsAny(out, i18n.RedactMark) {
		t.Errorf("伪造的内部打码标记漏出到终端：%q", out)
	}
	if strings.Contains(stripped, "\r") {
		t.Errorf("回车符漏出：%q", stripped)
	}
	// 超长问题不限长、不换行
	long := strings.Repeat("x", 100000)
	o := RenderProgressW(deckd.Progress{Project: "p", Graph: true, Problems: []string{long}}, 40)
	if len(o) > 5000 {
		t.Errorf("一条问题把面板输出撑到 %d 字节（没有限长）", len(o))
	}
}

// 5 英文界面里的汉字：逐项枚举。
func TestAdvEnglishHasNoHan(t *testing.T) {
	i18n.SetLang(i18n.EN)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	one, ten := 1.0, 10.0
	all := map[string]int{}
	for k := range progressCountKey {
		all[k] = 3
	}
	base := deckd.Progress{Project: "alpha", Graph: true, Counts: all, Unknown: 2, Health: "健康",
		Budgets: []deckd.BudgetView{
			{Label: "worker rounds", Used: &one, Limit: &ten, Unit: "轮", UnitKey: "round"},
			{Label: "frontier calls", Used: &one, Limit: &ten, Unit: "次", UnitKey: "call"},
			{Label: "elapsed", Used: &one, Limit: &ten, Unit: "小时", UnitKey: "hour"},
			{Label: "tokens", Limit: &ten, Reason: "读不到"},
		}}
	cases := map[string]deckd.Progress{
		"full": base,
		"nogr": {Project: "alpha"},
		"f1":   {Follow: true, Reason: "no_session"},
		"f2":   {Follow: true, Reason: "no_project"},
		"f3":   {Follow: true, Reason: "something_new"},
	}
	for name, p := range cases {
		if out := plain(RenderProgressW(p, 40)); hasHan(out) {
			t.Errorf("%s：英文有汉字：\n%s", name, out)
		}
	}
	// 老 deckd（没有 unit_key）：预算单位是中文原文
	old := base
	old.Budgets = []deckd.BudgetView{{Label: "worker rounds", Used: &one, Limit: &ten, Unit: "轮"}}
	if out := plain(RenderProgressW(old, 40)); hasHan(out) {
		t.Logf("（推测级，新面板配旧 deckd 才会遇到）没有 unit_key 的预算单位漏中文：\n%s", out)
	}
	// 没有码的问题（外部原文）：中文原样
	np := base
	np.Problems = []string{"读不到 work-graph.yaml：open x: no such file"}
	if out := plain(RenderProgressW(np, 40)); hasHan(out) {
		t.Logf("（已知局限，非缺陷）无码的外部原文问题保持原文：\n%s", out)
	}
	// 带码但码对不上参数个数：回退中文
	bp := base
	bp.Problems = []string{"读不到 x"}
	bp.ProblemsCoded = []i18n.Nest{{Code: "err.projectctl.graph.read", Args: []any{"only-one"}}}
	if out := plain(RenderProgressW(bp, 40)); hasHan(out) {
		t.Logf("（已知局限，非缺陷）码与参数不符时回退到中文原文：\n%s", out)
	}
}

// 语言中途切换：面板存的是画好的字符串，要等下一次取数（≤2s+请求时间）才换。
func TestAdvLanguageSwitchMidDisplay(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	c, _ := recordingDeckd(t, `{"project":"alpha","graph":true,"counts":{"accepted":2},"unknown":0,"budgets":[]}`)
	m := &model{name: "progress", c: c, width: 40}
	m.Update(m.fetch()())
	if v := plain(m.View()); !strings.Contains(v, "已验收") {
		t.Fatal(v)
	}
	i18n.SetLang(i18n.EN)
	if v := plain(m.View()); hasHan(v) {
		t.Errorf("语言已换成英文，下一帧 View() 仍是中文（要等下一次取数）：\n%s", v)
	}
	// 窗口宽度变化同理：分段条仍按旧宽度
	i18n.SetLang(i18n.ZH)
	m.Update(m.fetch()())
	before := m.View()
	m.Update(tea.WindowSizeMsg{Width: 20, Height: 10})
	if m.View() != before {
		t.Log("宽度变化后立刻重画了")
	} else {
		t.Errorf("窗口缩到 20 列后 View() 仍是 40 列时画的分段条（要等下一次取数）：%d 列", len(strings.Split(plain(m.View()), "\n")[1])/3)
	}
}

// 数据竞争：bubbletea 在独立 goroutine 里跑 fetch 的 Cmd，Update 在事件循环里写 m.width。go test -race 应报。
func TestAdvFetchRacesWithResize(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	c, _ := recordingDeckd(t, `{"project":"alpha","graph":true,"counts":{"accepted":2},"unknown":0,"budgets":[]}`)
	m := &model{name: "progress", c: c, width: 40, height: 20}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		cmd := m.fetch()
		go func() { defer wg.Done(); cmd() }() // tea 就是这样跑 Cmd 的
		m.Update(tea.WindowSizeMsg{Width: 30 + i, Height: 10})
	}
	wg.Wait()
}

// 2 面板这一端：服务端 12 秒才答，面板 10 秒超时：显示错误，不显示旧数据；下一次恢复后显示新数据。
func TestAdvPanelTimeoutDoesNotShowStale(t *testing.T) {
	if testing.Short() {
		t.Skip("要等 10 秒")
	}
	i18n.SetLang(i18n.ZH)
	dir, _ := os.MkdirTemp("/tmp", "n69s")
	t.Cleanup(func() { os.RemoveAll(dir) })
	ln, _ := net.Listen("unix", filepath.Join(dir, "p.sock"))
	var calls atomic.Int32
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		switch n {
		case 1:
			fmt.Fprint(w, `{"project":"alpha","graph":true,"counts":{"accepted":2},"unknown":0,"budgets":[]}`)
		case 2:
			select {
			case <-time.After(12 * time.Second):
			case <-r.Context().Done():
			}
			fmt.Fprint(w, `{"project":"beta","graph":true,"counts":{"accepted":9},"unknown":0,"budgets":[]}`)
		default:
			fmt.Fprint(w, `{"project":"beta","graph":true,"counts":{"accepted":9},"unknown":0,"budgets":[]}`)
		}
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	m := &model{name: "progress", c: deckclient.NewUI(filepath.Join(dir, "p.sock")), width: 40}
	m.Update(m.fetch()())
	t0 := time.Now()
	m.Update(m.fetch()()) // 第二次超时
	t.Logf("超时耗时 %v", time.Since(t0))
	v := plain(m.View())
	t.Logf("超时后显示：\n%s", v)
	if strings.Contains(v, "alpha") || strings.Contains(v, "已验收") {
		t.Errorf("超时后仍显示旧项目的数据")
	}
	if !strings.Contains(v, "取不到") {
		t.Errorf("超时没有给出错误：\n%s", v)
	}
	m.Update(m.fetch()())
	if v := plain(m.View()); !strings.Contains(v, "beta") {
		t.Errorf("恢复后应显示新数据：\n%s", v)
	}
}

// 2c 无 Graph 的老响应 / 回调返回 null：面板的表现
func TestAdvPanelOddReplies(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	for name, reply := range map[string]string{
		"null":          `null`,
		"empty-obj":     `{}`,
		"graph-no-body": `{"project":"alpha","graph":true}`,
		"wrong-types":   `{"project":"alpha","graph":true,"counts":{"accepted":"x"}}`,
		"unknown-reason": `{"project":"","graph":false,"follow":true,"reason":"future_reason"}`,
	} {
		c, _ := recordingDeckd(t, reply)
		m := &model{name: "progress", c: c, width: 40}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s：panic %v", name, r)
				}
			}()
			m.Update(m.fetch()())
			t.Logf("%s => %q", name, plain(m.View()))
		}()
	}
}

// 2d 读不到 Graph 的项目（problems 非空、没有节点）被画成「全 0」的进度条。
func TestAdvUnreadableGraphLooksLikeEmptyProject(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	p := deckd.Progress{Project: "alpha", Graph: true, Counts: map[string]int{}, Problems: []string{"读不到 /x/work-graph.yaml：no such file"}}
	out := plain(RenderProgressW(p, 40))
	t.Logf("\n%s", out)
	if strings.Contains(out, "已验收 0") && strings.Contains(out, "其余 0") {
		t.Log("读不到 Graph 与「0 个节点」画法相同，只靠最后一行 ! 区分")
	}
}
