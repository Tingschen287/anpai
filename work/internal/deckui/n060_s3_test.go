package deckui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 段 3：设置里的「界面语言」一项，保存后立刻生效（不重启），各界面进程跟着换。

func hanIn(s string) (rune, bool) {
	for _, r := range s {
		if (r >= 0x4e00 && r <= 0x9fff) || (r >= 0x3000 && r <= 0x303f) || (r >= 0xff00 && r <= 0xffef) {
			return r, true
		}
	}
	return 0, false
}

func goLangRow(t *testing.T, m *Settings) {
	t.Helper()
	m.switchPage(pageGeneral)
	m.cur = rowIdx(t, m, rowLang, "", "")
}

// 在单独一页「通用」上（owner 2026-10-20 定）；中文界面写「界面语言  ● 中文  ○ English」，亮着的是当前语言。
func TestN060S3LanguageRowIsOnTheGeneralPage(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	i18n.SetLang(i18n.ZH)
	m, _ := n057Fixture()
	m.width, m.height = 100, 40
	goLangRow(t, m)
	rs := m.rows()
	if len(rs) != 1 || rs[0].kind != rowLang || m.page != pageGeneral {
		t.Fatalf("通用页只有语言一行：%+v", rs)
	}
	v := plainView(m)
	for _, want := range []string{"界面语言", "● 中文", "○ English", "空格或 ⏎ 在中文与 English 之间切换"} {
		if !strings.Contains(v, want) {
			t.Fatalf("中文界面缺 %q：\n%s", want, v)
		}
	}
	// 没选过语言时，亮着的是此刻界面正在用的（第一次按系统语言）
	i18n.SetLang(i18n.EN)
	m2, _ := n057Fixture()
	goLangRow(t, m2)
	if v := plainView(m2); !strings.Contains(v, "Language") || !strings.Contains(v, "● English") || !strings.Contains(v, "○ Chinese") {
		t.Fatalf("英文界面没选过语言：\n%s", v)
	}
	// 其他页没有语言行
	for _, p := range []page{pageProject, pageAgents, pageTrust} {
		m2.switchPage(p)
		for _, r := range m2.rows() {
			if r.kind == rowLang {
				t.Fatalf("页 %d 不该有语言行", p)
			}
		}
	}
}

// 空格（或 ⏎）在语言之间切换，和别的设置一样自动保存；存上之后立刻换语言，设置页自己也是。
func TestN060S3ToggleSavesAndSwitchesImmediately(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	i18n.SetLang(i18n.ZH)
	m, api := n057Fixture()
	m.width, m.height = 100, 40
	goLangRow(t, m)
	before := bodyUpTo(plainView(m), "界面语言")

	press(m, " ")
	if len(api.saved) != 1 || api.saved[0].UILang != i18n.EN {
		t.Fatalf("应自动保存一次，带 ui_lang=en：%+v", api.saved)
	}
	if i18n.Lang() != i18n.EN {
		t.Fatalf("存上之后立刻换语言：%s", i18n.Lang())
	}
	if m.dirty || !m.saveErr.empty() || !m.saved {
		t.Fatalf("保存状态：dirty=%v err=%q saved=%v", m.dirty, m.saveErr.String(), m.saved)
	}
	// 设置页自己整页都是英文，没有汉字；三页都查
	for _, p := range []page{pageProject, pageAgents, pageTrust, pageGeneral} {
		m.switchPage(p)
		if r, bad := hanIn(plainView(m)); bad {
			t.Errorf("切到英文后，设置页 %d 还有汉字 %q：\n%s", p, r, plainView(m))
		}
	}
	m.switchPage(pageGeneral)
	m.cur = rowIdx(t, m, rowLang, "", "")
	if v := plainView(m); !strings.Contains(v, "Language") || !strings.Contains(v, "● English") || !strings.Contains(v, "✓ Saved") {
		t.Fatalf("英文页：\n%s", v)
	}

	// 切回中文（⏎ 同样切换）：页面正文与切换前逐字一致
	press(m, "enter")
	if i18n.Lang() != i18n.ZH || len(api.saved) != 2 || api.saved[1].UILang != i18n.ZH {
		t.Fatalf("切回中文：lang=%s saved=%+v", i18n.Lang(), api.saved)
	}
	m.cur = rowIdx(t, m, rowLang, "", "")
	if after := bodyUpTo(plainView(m), "界面语言"); after != before {
		t.Fatalf("切回中文后与切换前不一致：\n--- 前\n%s\n--- 后\n%s", before, after)
	}
}

// 页面里「界面语言」那一行及以上的正文（去掉底部的保存状态，那部分随保存而变）。
func bodyUpTo(view, marker string) string {
	var out []string
	for _, l := range strings.Split(view, "\n") {
		out = append(out, strings.TrimRight(l, " "))
		if strings.Contains(l, marker) {
			break
		}
	}
	return strings.Join(out, "\n")
}

// 没存上就不换：保存失败（deckd 连不上等）、冲突（别处改过）时界面语言不变，状态里写明原因。
func TestN060S3FailedOrConflictedSaveDoesNotSwitch(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	i18n.SetLang(i18n.ZH)
	m, api := n057Fixture()
	goLangRow(t, m)
	api.fail = errors.New("连不上")
	press(m, " ")
	if i18n.Lang() != i18n.ZH {
		t.Fatalf("没存上不该换语言：%s", i18n.Lang())
	}
	if m.saveErr.empty() || !m.dirty {
		t.Fatalf("应记下没存上：err=%q dirty=%v", m.saveErr.String(), m.dirty)
	}
	// 手动重试（s）：这回存上了，换语言
	api.fail = nil
	press(m, "s")
	if i18n.Lang() != i18n.EN || len(api.saved) != 1 || api.saved[0].UILang != i18n.EN {
		t.Fatalf("重试存上之后换语言：%s %+v", i18n.Lang(), api.saved)
	}

	// 冲突：409
	i18n.SetLang(i18n.ZH)
	m2, api2 := n057Fixture()
	goLangRow(t, m2)
	api2.fail = &deckclient.Error{Status: http.StatusConflict, Message: "changed"}
	press(m2, " ")
	if i18n.Lang() != i18n.ZH || !m2.conflict {
		t.Fatalf("冲突不该换语言：lang=%s conflict=%v", i18n.Lang(), m2.conflict)
	}
	// R 重读：丢掉没存上的语言选择，语言行回到 deckd 那边的
	press(m2, "R", "R")
	if m2.set.UILang != "" || i18n.Lang() != i18n.ZH {
		t.Fatalf("重读之后：UILang=%q lang=%s", m2.set.UILang, i18n.Lang())
	}
}

// n059 的自动保存不回退：语言和别的改动一样合并——一次保存在路上时又改了，回来再存最新的那份。
func TestN060S3LanguageChangeCoalescesWithAutosave(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	i18n.SetLang(i18n.ZH)
	m, api := n057Fixture()
	goLangRow(t, m)
	space := tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}
	_, c1 := m.Update(space) // en，保存发出但还没回来
	if !m.saving || c1 == nil {
		t.Fatal("第一次改动应发出保存")
	}
	_, c2 := m.Update(space) // 回 zh：保存在路上，记下待存
	if c2 != nil && !m.pending {
		t.Fatal("路上又改了，应记下待存而不是并发再存")
	}
	if !m.pending {
		t.Fatal("pending 应为真")
	}
	run(m, c1) // 第一次保存回来：存上了 en，随即发现路上又改过，接着存最新的（zh）
	if len(api.saved) != 2 || api.saved[0].UILang != i18n.EN || api.saved[1].UILang != i18n.ZH {
		t.Fatalf("应依次存 en、zh：%+v", api.saved)
	}
	if i18n.Lang() != i18n.ZH || m.dirty || m.pending || m.saving {
		t.Fatalf("最终：lang=%s dirty=%v pending=%v saving=%v", i18n.Lang(), m.dirty, m.pending, m.saving)
	}
	// 代数跟着 deckd 回的走（n057）：第二次保存带的是第一次存下的代数
	if api.saved[1].Generation != api.saved[0].Generation+1000 {
		t.Fatalf("第二次保存应带第一次存下的代数：%d -> %d", api.saved[0].Generation, api.saved[1].Generation)
	}
}

// DeckSettingsAPI 的保存请求：选过语言才带 ui_lang；没选过不带（deckd 当「不动已存的」）。
func TestN060S3SaveRequestCarriesLangOnlyWhenChosen(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	sock := filepath.Join(t.TempDir(), "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var bodies []map[string]any
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(b, &m)
		mu.Lock()
		bodies = append(bodies, m)
		mu.Unlock()
		w.Header().Set(i18n.LangHeader, "en")
		w.Write([]byte(`{"generation":99}`))
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	api := DeckSettingsAPI{C: deckclient.NewUI(sock).FollowLang()}
	i18n.SetLang(i18n.ZH)
	gen, err := api.Save(context.Background(), deckstore.Settings{Generation: 3})
	if err != nil || gen != 99 {
		t.Fatalf("%d %v", gen, err)
	}
	if _, has := bodies[0]["ui_lang"]; has {
		t.Fatalf("没选过语言不该带 ui_lang：%v", bodies[0])
	}
	if _, err := api.Save(context.Background(), deckstore.Settings{Generation: 3, UILang: "en"}); err != nil {
		t.Fatal(err)
	}
	if bodies[1]["ui_lang"] != "en" || bodies[1]["generation"] != float64(3) {
		t.Fatalf("选过语言要带 ui_lang 和代数：%v", bodies[1])
	}
	// 保存的响应头带着语言：本进程跟着换（设置界面不靠别的就能立刻换）
	if i18n.Lang() != i18n.EN {
		t.Fatalf("响应头应让本进程换到英文：%s", i18n.Lang())
	}
}

// 运行中切换（验收「切换」）：先中文渲染主要画面，切英文再渲染——没有汉字、品牌一律 Anpai；再切回中文，与切换前逐字一致。
// 画面是第 1 段的那一组（侧栏、空闲页、标签条、设置各页与子视图、新建项目表单、额度、决策、进度、通知、活动、布局文字）。
// 品牌名「安排」在英文界面里没有任何允许出现的位置（renderStage1 的英文那一遍逐个查过）。
func TestN060S3RunningSwitchChineseEnglishChinese(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH); stage1Capture = nil })
	grab := func(lang string, english bool) map[string]string {
		got := map[string]string{}
		stage1Capture = func(name, text string) { got[name] = text }
		i18n.SetLang(lang)
		renderStage1(t, english)
		stage1Capture = nil
		return got
	}
	zh1 := grab(i18n.ZH, false)
	en := grab(i18n.EN, true) // 英文那一遍里逐个画面查了没有汉字
	zh2 := grab(i18n.ZH, false)
	if len(zh1) < 25 {
		t.Fatalf("画面太少：%d", len(zh1))
	}
	same := 0
	for name, a := range zh1 {
		if b := zh2[name]; a != b {
			t.Errorf("%s：切回中文后与切换前不一致\n--- 前\n%s\n--- 后\n%s", name, a, b)
		}
		if e := en[name]; e == a && strings.TrimSpace(a) != "" {
			same++
		}
	}
	// 不同语言的画面确实不同（英文表覆盖到了）；允许个别没有文字的画面相同
	if same > 3 {
		t.Errorf("有 %d 个画面英文和中文一样，切换可能没生效", same)
	}
	if !strings.Contains(zh1["idle-wide"], "安排") || strings.Contains(en["idle-wide"], "安排") {
		t.Errorf("品牌：中文界面「安排 Anpai」，英文界面一律 Anpai：%q %q", zh1["idle-wide"], en["idle-wide"])
	}
}

// 不重启：侧栏和标签条每秒向 deckd 取一次数（DeckAPI.Fetch），响应头带的语言一变，下一次画面就是新语言。
// 用真实的 unix socket 客户端（FollowLang）和侧栏、标签条的取数，假 deckd 只改它回的语言头。
func TestN060S3SidebarAndTabsFollowDeckdWithoutRestart(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	sock := filepath.Join(t.TempDir(), "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var lang atomic.Value
	lang.Store("zh")
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(i18n.LangHeader, lang.Load().(string))
		switch r.URL.Path {
		case "/v1/projects":
			w.Write([]byte(`[{"id":"demo","name":"Demo","path":"/tmp/demo"},{"id":"empty","name":"Empty","path":"/tmp/empty"}]`))
		case "/v1/sessions":
			w.Write([]byte(`[{"key":"k1","agentdeck_id":"m1","project":"demo","role":"main","title":"Main","tool":"claude","status":"running","activity":"waiting"}]`))
		case "/v1/view":
			w.Write([]byte(`{"open":["m1"],"focus":"m1","taken_over":["m1"]}`))
		default:
			http.NotFound(w, r)
		}
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })

	i18n.SetLang(i18n.ZH)
	// 侧栏、标签条的取数循环（newLoop）每秒调一次 DeckAPI.Fetch；这里直接调 Fetch，不起循环的 goroutine（它没有停止的办法）。
	api := DeckAPI{C: deckclient.NewUI(sock).FollowLang()}
	next := func() Data {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		d, err := api.Fetch(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	render := func(d Data) (side, tabs string) {
		sb := &Sidebar{Data: d, Mode: "none"}
		return ansi.Strip(strings.Join(sb.Render(60, 30).Lines, "\n")), ansi.Strip((&Tabs{Data: d}).Render(80).Lines[0] + "\n" + (&Tabs{Data: d}).Hint())
	}
	d := next()
	if d.Err != "" || len(d.Sessions) != 1 {
		t.Fatalf("取数：%+v", d)
	}
	zhSide, zhTabs := render(d)

	lang.Store("en") // owner 在设置里切到英文；deckd 的响应头随之变
	var enSide, enTabs string
	d = next() // 下一次取数：响应头把本进程换成英文，这一次画出来的就是英文
	if i18n.Lang() != i18n.EN {
		t.Fatalf("下一次取数之后本进程应是英文：%s", i18n.Lang())
	}
	enSide, enTabs = render(d)
	if enSide == zhSide {
		t.Fatalf("侧栏应换成英文：\n%s", enSide)
	}
	if enTabs == zhTabs {
		t.Fatalf("标签条应换成英文：\n%s", enTabs)
	}
	if r, bad := hanIn(enSide + enTabs); bad {
		t.Fatalf("英文的侧栏、标签条里有汉字 %q：\n%s\n%s", r, enSide, enTabs)
	}

	lang.Store("zh")
	d = next()
	backSide, backTabs := render(d)
	if backSide != zhSide || backTabs != zhTabs {
		t.Fatalf("切回中文后与切换前不一致：\n%s\n---\n%s", zhSide, backSide)
	}
}
