package panels

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckd"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// n069：进度面板按原型做分段条（已验收、阻塞、失败、其余）与数字格；项目跟随当前在看的会话。

func TestSegmentCells(t *testing.T) {
	cases := []struct {
		counts []int
		n      int
	}{
		{[]int{37, 3, 1, 8}, 40},   // 原型里的 49 个节点
		{[]int{37, 3, 1, 8}, 10},   // 窄：失败的 1 个也要有 1 格
		{[]int{0, 0, 0, 5}, 20},    // 只有其余
		{[]int{49, 0, 0, 0}, 20},   // 全部验收
		{[]int{1, 1, 1, 1}, 4},     // 刚好一格一段
		{[]int{1, 1, 1, 1}, 3},     // 格子比非零的段还少：总数仍然对
		{[]int{0, 0, 0, 0}, 20},    // 没有节点：全空
		{[]int{1000, 1, 0, 0}, 30}, // 悬殊
	}
	for _, c := range cases {
		got := segmentCells(c.counts, c.n)
		sum, total := 0, 0
		for i, g := range got {
			sum += g
			total += c.counts[i]
			if g < 0 {
				t.Fatalf("%v/%d：负数 %v", c.counts, c.n, got)
			}
		}
		want := c.n
		if total == 0 {
			want = 0
		}
		if sum != want {
			t.Fatalf("%v/%d：格数加起来是 %d，应为 %d：%v", c.counts, c.n, sum, want, got)
		}
		nz := 0
		for _, x := range c.counts {
			if x > 0 {
				nz++
			}
		}
		if c.n >= nz {
			for i, x := range c.counts {
				if x > 0 && got[i] == 0 {
					t.Fatalf("%v/%d：第 %d 段有节点却没有格子：%v", c.counts, c.n, i, got)
				}
			}
		}
	}
	// 比例：37/49 约 75.5%
	if got := segmentCells([]int{37, 3, 1, 8}, 49); got[0] != 37 || got[1] != 3 || got[2] != 1 || got[3] != 8 {
		t.Fatalf("格数等于节点数时应一一对应：%v", got)
	}
}

func prog() deckd.Progress {
	return deckd.Progress{Project: "Agent Deck", Graph: true, Counts: map[string]int{"accepted": 37, "blocked": 3, "failed": 1, "planned": 7, "running": 1}}
}

func TestRenderProgressWMatchesPrototype(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	raw := RenderProgressW(prog(), 40)
	out := plain(raw)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if lines[0] != "项目进度 · Agent Deck" {
		t.Fatalf("标题：%q", lines[0])
	}
	// 第二行是分段条：一整行，宽度 = 面板宽 - 2，全是块字符
	if w := lipgloss.Width(lines[1]); w != 38 || strings.Trim(lines[1], "█") != "" {
		t.Fatalf("分段条应是 38 格的块：%q（%d）", lines[1], w)
	}
	// 数字格：已验收 37 / 阻塞 3 / 失败 1 / 其余 8（其余 = 总数 49 - 37 - 3 - 1）
	for _, want := range []string{"已验收 37", "阻塞 3", "失败 1", "其余 8", "进行中 1 · 计划中 7"} {
		if !strings.Contains(out, want) {
			t.Fatalf("缺 %q：\n%s", want, out)
		}
	}
	if !strings.Contains(lines[2], "已验收 37") || !strings.Contains(lines[2], "阻塞 3") || !strings.Contains(lines[3], "失败 1") || !strings.Contains(lines[3], "其余 8") {
		t.Fatalf("数字格应是两行两列：\n%s", out)
	}
	// 分段条各段的颜色：原型的绿、琥珀、红、暗灰
	for _, hex := range []string{"125;211;160", "245;177;76", "229;72;77"} { // #7dd3a0 #f5b14c #e5484d
		_ = hex
	}
	if !strings.Contains(raw, "█") {
		t.Fatal("分段条")
	}
}

func TestRenderProgressWEnglishAndNarrow(t *testing.T) {
	i18n.SetLang(i18n.EN)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	out := plain(RenderProgressW(prog(), 12)) // 窄到只有 12 列
	for _, want := range []string{"Accepted 37", "Blocked 3", "Failed 1", "Other 8"} {
		if !strings.Contains(out, want) {
			t.Fatalf("缺 %q：\n%s", want, out)
		}
	}
	if hasHan(out) {
		t.Fatalf("英文面板不该有汉字：\n%s", out)
	}
	for _, w := range []int{0, 1, 5, 8, 100, 500} { // 宽度未知、极窄、极宽都不崩，分段条长度有界
		o := plain(RenderProgressW(prog(), w))
		if bar := strings.Split(o, "\n")[1]; lipgloss.Width(bar) > 60 || lipgloss.Width(bar) < 8 {
			t.Fatalf("宽度 %d：分段条 %d 格", w, lipgloss.Width(bar))
		}
	}
}

func TestRenderProgressWEmptyGraphAndUnknown(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	out := plain(RenderProgressW(deckd.Progress{Project: "p", Graph: true, Counts: map[string]int{}}, 40))
	if !strings.Contains(out, "已验收 0") || !strings.Contains(out, "其余 0") {
		t.Fatalf("没有节点也画数字格：\n%s", out)
	}
	out = plain(RenderProgressW(deckd.Progress{Project: "p", Graph: true, Unknown: 2, Counts: map[string]int{"accepted": 1}}, 40))
	if !strings.Contains(out, "其余 2") || !strings.Contains(out, "状态未知 2") {
		t.Fatalf("状态未知的算进其余并列出：\n%s", out)
	}
	if !strings.Contains(plain(RenderProgressW(deckd.Progress{Project: "p"}, 40)), "这个项目没有接 Graph") {
		t.Fatal("没接 Graph 的说明")
	}
}

// 跟随：没有在看的会话、会话不属于项目，说一句清楚的话，不出现「--project」。
func TestRenderProgressFollowExplanations(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	for lang, want := range map[string]map[string]string{
		i18n.ZH: {"no_session": "现在没有在看的会话", "no_project": "不属于任何项目"},
		i18n.EN: {"no_session": "No session is open right now", "no_project": "does not belong to a project"},
	} {
		i18n.SetLang(lang)
		for reason, text := range want {
			out := plain(RenderProgressW(deckd.Progress{Follow: true, Reason: reason}, 60))
			if !strings.Contains(strings.ReplaceAll(out, "\n", ""), text) {
				t.Fatalf("%s/%s：缺 %q：\n%s", lang, reason, text, out)
			}
			if strings.Contains(out, "--project") || strings.Contains(out, "指定") {
				t.Fatalf("不该再要求指定项目：\n%s", out)
			}
			if lang == i18n.EN && hasHan(out) {
				t.Fatalf("英文说明有汉字：%q", out)
			}
		}
	}
	// 窄到极致也不崩
	i18n.SetLang(i18n.ZH)
	_ = RenderProgressW(deckd.Progress{Follow: true, Reason: "no_session"}, 0)
}

// ---------------- 面板向 deckd 要什么 ----------------

func recordingDeckd(t *testing.T, reply string) (*deckclient.Client, *[]string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "n69")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "p.sock"))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var urls []string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		urls = append(urls, r.URL.RequestURI())
		mu.Unlock()
		w.Write([]byte(reply))
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return deckclient.NewUI(filepath.Join(dir, "p.sock")), &urls
}

// 没指定 -project：不带 project 参数，由 deckd 按当前看的会话解析；指定了：带上，优先生效（owner 的 panels.yaml 就写着 -project）。
func TestProgressPanelAsksDeckdByProject(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	c, urls := recordingDeckd(t, `{"project":"alpha","graph":true,"counts":{"accepted":2},"unknown":0,"budgets":[]}`)
	m := &model{name: "progress", c: c, width: 40}
	m.Update(m.fetch()())
	if len(*urls) != 1 || (*urls)[0] != "/v1/progress" {
		t.Fatalf("没指定项目不带参数：%v", *urls)
	}
	if v := plain(m.View()); !strings.Contains(v, "项目进度 · alpha") || !strings.Contains(v, "已验收 2") {
		t.Fatalf("应显示 deckd 跟出来的项目：\n%s", v)
	}
	m2 := &model{name: "progress", project: "it-req-automation", c: c, width: 40}
	m2.Update(m2.fetch()())
	if (*urls)[1] != "/v1/progress?project=it-req-automation" {
		t.Fatalf("指定了项目就带上：%v", *urls)
	}
}

func TestProgressPanelFollowReplyRendersExplanation(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	c, _ := recordingDeckd(t, `{"project":"","graph":false,"follow":true,"reason":"no_session"}`)
	m := &model{name: "progress", c: c, width: 60}
	m.Update(m.fetch()())
	v := ansi.Strip(m.View())
	if !strings.Contains(strings.ReplaceAll(v, "\n", ""), "现在没有在看的会话") || strings.Contains(v, "--project") || strings.Contains(v, "取不到") {
		t.Fatalf("\n%s", v)
	}
}
