package deckui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// n060 段 4 返工：读登录 PATH 的触发时机。界面这一侧：进页面不触发；加入页与 agent 页同一个键（p）触发一次；连按不叠加；
// 刷新期间界面照常可用；启动时读失败有提示、按键可恢复；新增文字走文字表、英文下没有汉字。

// refreshAPI 是带「刷新会话 PATH」的假接口：RefreshPath 记次数，之后 PathRefreshing 先回 busy 次「还在读」，再回读完，Tools 回 after。
type refreshAPI struct {
	*fakeSettingsAPI
	mu      sync.Mutex
	calls   atomic.Int32 // RefreshPath 被调用的次数
	tools   atomic.Int32 // Tools 被调用的次数
	polls   atomic.Int32 // PathRefreshing（轮询）被调用的次数
	after   []ToolInfo
	busy    int           // RefreshPath 之后，Tools 还要回几次「正在刷新」
	err     error         // 不为 nil：RefreshPath 失败
	gate    chan struct{} // 非 nil：Tools 在刷新中要等它关闭才返回（模拟慢 rc）
	pending bool
}

func (a *refreshAPI) PreloadTools() bool { return true }
func (a *refreshAPI) RefreshPath(context.Context) error {
	a.calls.Add(1)
	if a.err != nil {
		return a.err
	}
	a.mu.Lock()
	a.pending = true
	a.mu.Unlock()
	return nil
}

// PathRefreshing：RefreshPath 之后先回 busy 次「还在读」（gate 非 nil 时一直等到放行），然后读完。
func (a *refreshAPI) PathRefreshing(ctx context.Context) (bool, error) {
	a.polls.Add(1)
	a.mu.Lock()
	gate, pending := a.gate, a.pending
	if pending && a.busy > 0 && gate == nil {
		a.busy--
		a.mu.Unlock()
		return true, nil
	}
	a.mu.Unlock()
	if pending && gate != nil {
		<-gate
	}
	return false, nil
}

// Tools：读完之后（pending）回新的 after，否则回现有的。
func (a *refreshAPI) Tools(ctx context.Context) ([]ToolInfo, error) {
	a.tools.Add(1)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pending && a.after != nil {
		return a.after, nil
	}
	return a.fakeSettingsAPI.tools, nil
}

func scopedTools(version, dir string) []ToolInfo {
	return []ToolInfo{
		{Tool: "claude", Name: "Claude Code", Binary: "claude", Installed: true, Path: dir + "/claude", Version: "2.9.1", PathSource: "login-shell"},
		{Tool: "codex", Name: "Codex", Binary: "codex", Installed: true, Path: dir + "/codex", Version: version, CanList: true, PathSource: "login-shell",
			Models: []deckstore.ModelDef{{ID: "gpt-6-astra", Tier: deckstore.Frontier}, {ID: "gpt-6.1-sol", Tier: deckstore.Balanced}, {ID: "gpt-6-luna", Tier: deckstore.Economy}}},
		{Tool: "kimi", Name: "Kimi Code", Binary: "kimi", Agent: true, Installed: true, Path: dir + "/kimi", Version: "kimi 2.3.2", PathSource: "login-shell",
			Models: []deckstore.ModelDef{{ID: "kimi-code/k3", Tier: deckstore.Balanced}, {ID: "kimi-for-coding", Tier: deckstore.Economy}}},
	}
}

// refreshFixture：全局清单里只有 keep 里的 agent，其余留在「加入」页里可选。
func refreshFixture(t *testing.T, tools []ToolInfo, keep ...string) (*Settings, *refreshAPI) {
	t.Helper()
	if len(keep) == 0 {
		keep = []string{"claude", "codex"}
	}
	t.Setenv("PATH", t.TempDir())
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	i18n.SetLang(i18n.ZH)
	base := &fakeSettingsAPI{
		set: deckstore.Settings{Generation: 3, Agents: deckstore.DefaultAgents(), Projects: []deckstore.ProjectDef{{ID: "ai", Name: "ai", Path: "/tmp/ai",
			Agents: []string{"claude"}, Models: map[deckstore.Tier]deckstore.ModelList{deckstore.Frontier: {"claude-opus-5-5"}},
			MainAgent: deckstore.MainAgent{Agent: "claude", Tier: deckstore.Frontier}, Envelope: deckstore.DefaultEnvelope("ai")}}},
		tools:  tools,
		models: map[string][]string{"codex": {"gpt-6.1-sol"}},
	}
	var agents []deckstore.AgentDef
	for _, a := range base.set.Agents {
		for _, k := range keep {
			if a.ID == k {
				agents = append(agents, a)
			}
		}
	}
	base.set.Agents = agents
	api := &refreshAPI{fakeSettingsAPI: base}
	m := &Settings{API: api}
	m.width, m.height = 110, 40
	_, cmd := m.Update(m.load())
	for _, msg := range bgMsgs(cmd) {
		m.Update(msg)
	}
	return m, api
}

func openAdd(t *testing.T, m *Settings) {
	t.Helper()
	goRow(t, m, rowAddAgent, "", "")
	press(m, "enter")
}

func openCodex(t *testing.T, m *Settings) {
	t.Helper()
	goRow(t, m, rowGlobal, "codex", "")
	press(m, "enter")
}

// pKey 按一下 p，返回命令（不运行）。
func pKey(m *Settings) tea.Cmd {
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	return cmd
}

// 打开设置窗口、进加入页、进 agent 页、翻页：界面不请求刷新（不运行 rc）。
func TestN060S4FixUIEnteringPagesNeverRefreshes(t *testing.T) {
	m, api := refreshFixture(t, scopedTools("codex-cli 0.164.0", "/h/bin"))
	openAdd(t, m)
	press(m, "esc")
	openCodex(t, m)
	press(m, "esc")
	m.switchPage(pageProject)
	m.switchPage(pageAgents)
	openAdd(t, m)
	if api.calls.Load() != 0 {
		t.Fatalf("进页面不该请求刷新：%d", api.calls.Load())
	}
	if api.tools.Load() == 0 {
		t.Fatal("进页面照常取工具目录（读 deckd 当前快照）")
	}
}

// 加入页与 agent 页用同一个键 p：按一下后台读一次；连按忽略；读完更新已装与版本。
func TestN060S4FixUIKeyRefreshesOnBothPagesAndRepeatsAreIgnored(t *testing.T) {
	m, api := refreshFixture(t, scopedTools("codex-cli 0.164.0", "/h/bin"))
	api.after = scopedTools("codex-cli 0.170.0", "/h/newbin")
	api.busy = 1
	// 加入页
	openAdd(t, m)
	cmd := pKey(m)
	if cmd == nil {
		t.Fatal("按 p 应起一次刷新")
	}
	if again := pKey(m); again != nil {
		t.Fatal("等待期间再按 p 要忽略（不叠加）")
	}
	if v := flat(plainView(m)); !strings.Contains(v, "正在刷新会话 PATH") {
		t.Fatalf("刷新中应有说明：\n%s", plainView(m))
	}
	run(m, cmd)
	if api.calls.Load() != 1 {
		t.Fatalf("按一次、连按一次，只请求一次：%d", api.calls.Load())
	}
	v := flat(plainView(m))
	if !strings.Contains(v, "/h/newbin/codex") && !strings.Contains(v, "/h/newbin/kimi") {
		t.Fatalf("读完后加入页显示新路径：\n%s", plainView(m))
	}
	if strings.Contains(v, "正在刷新") || !strings.Contains(v, "会话 PATH 已刷新") {
		t.Fatalf("读完后不再说正在刷新，并提示已刷新：\n%s", plainView(m))
	}
	// agent 页，同一个键
	press(m, "esc")
	api.mu.Lock()
	api.pending, api.busy = false, 1
	api.mu.Unlock()
	openCodex(t, m)
	if !strings.Contains(flat(plainView(m)), "codex-cli 0.170.0") { // 打开页取到的已是 deckd 现在的快照（假接口 after）
		api.fakeSettingsAPI.tools = scopedTools("codex-cli 0.164.0", "/h/bin")
	}
	cmd = pKey(m)
	if cmd == nil {
		t.Fatal("agent 页也应能刷新")
	}
	run(m, cmd)
	if api.calls.Load() != 2 {
		t.Fatalf("agent 页按一次再请求一次：%d", api.calls.Load())
	}
	if v := flat(plainView(m)); !strings.Contains(v, "codex-cli 0.170.0") {
		t.Fatalf("agent 页显示新版本：\n%s", plainView(m))
	}
}

// 刷新期间界面照常可用：读被挂住（慢 rc）时，光标照样能动、说明照样显示；读完才更新。
func TestN060S4FixUIScreenStaysUsableWhileRefreshing(t *testing.T) {
	m, api := refreshFixture(t, scopedTools("codex-cli 0.164.0", "/h/bin"), "claude")
	api.after = scopedTools("codex-cli 0.170.0", "/h/newbin")
	openAdd(t, m)
	api.mu.Lock()
	api.gate = make(chan struct{})
	api.mu.Unlock()
	cmd := pKey(m)
	got := make(chan tea.Msg, 1)
	go func() { got <- cmd() }()
	before := m.sub.cur
	press(m, "down")
	press(m, "up")
	press(m, "down")
	if m.sub.cur == before {
		t.Fatal("刷新期间光标应能动")
	}
	if v := flat(plainView(m)); !strings.Contains(v, "正在刷新会话 PATH") || !strings.Contains(v, "界面照常可用，先用旧结果") || !strings.Contains(v, "/h/bin/") {
		t.Fatalf("刷新期间仍显示旧结果并说明：\n%s", plainView(m))
	}
	press(m, "esc") // 还能退出这一页
	if m.sub != nil {
		t.Fatal("刷新期间应能退出这一页")
	}
	close(api.gate)
	msg := <-got
	m.Update(msg)
	if m.pathBusy {
		t.Fatal("读完后清掉忙标记")
	}
}

// 启动时读失败：加入页与 agent 页可见地提示原因，并告诉 owner 按 p 重试；按键读成功后恢复。中英各一遍，英文下没有汉字。
func TestN060S4FixUIStartupFailureIsVisibleAndTheKeyRecovers(t *testing.T) {
	failed := scopedTools("codex-cli 0.150.0", "/own")
	note := []any{map[string]any{"code": "err.sessionenv.timeout", "args": []any{"/bin/zsh", "5s"}, "error": "找不到会话的 PATH：/bin/zsh 在 5s 内没有给出结果（~/.zshrc 之类的启动文件可能卡住了）"}}
	for i := range failed {
		failed[i].PathSource = "process"
		failed[i].Note = "没取到会话的 PATH，已改用 deckd 自己的 PATH 找 CLI：找不到会话的 PATH：/bin/zsh 在 5s 内没有给出结果（~/.zshrc 之类的启动文件可能卡住了）"
		failed[i].NoteCode = "err.deckd.servergettools.nopath"
		failed[i].NoteArgs = note
	}
	m, api := refreshFixture(t, failed)
	api.after = scopedTools("codex-cli 0.164.0", "/h/bin")
	for _, page := range []string{"add", "agent"} {
		if page == "add" {
			openAdd(t, m)
		} else {
			openCodex(t, m)
		}
		zh := flat(plainView(m))
		if !strings.Contains(zh, "没取到会话的 PATH，已改用 deckd 自己的 PATH 找 CLI") || !strings.Contains(zh, "按 p 重试") || !strings.Contains(zh, "登录启动文件") {
			t.Fatalf("%s 页中文：应说明原因、让按 p 重试、说明会运行登录启动文件：\n%s", page, plainView(m))
		}
		i18n.SetLang(i18n.EN)
		v := plainView(m)
		if r, bad := hanIn(v); bad {
			t.Fatalf("%s 页英文有汉字 %q：\n%s", page, r, v)
		}
		if f := flat(v); !strings.Contains(f, "Couldn't get the session PATH, so deckd's own PATH is used") || !strings.Contains(f, "Press p to try again") || !strings.Contains(f, "login startup files") {
			t.Fatalf("%s 页英文：\n%s", page, v)
		}
		i18n.SetLang(i18n.ZH)
		press(m, "esc")
	}
	openAdd(t, m)
	run(m, pKey(m))
	if v := flat(plainView(m)); strings.Contains(v, "没取到会话的 PATH") || strings.Contains(v, "按 p 重试") || !strings.Contains(v, "/h/bin/") {
		t.Fatalf("按键成功后恢复：\n%s", plainView(m))
	}
}

// 按键刷新失败：请求发不出去，提示原因（可再试）；deckd 回「刷新失败、仍用旧快照」时显示那条原因，同时仍是平时的说明。
func TestN060S4FixUIRefreshFailureIsShownAndCanBeRetried(t *testing.T) {
	m, api := refreshFixture(t, scopedTools("codex-cli 0.164.0", "/h/bin"))
	api.err = errors.New("connection refused")
	openAdd(t, m)
	run(m, pKey(m))
	if v := flat(plainView(m)); !strings.Contains(v, "刷新会话 PATH 失败：") || !strings.Contains(v, "connection refused") {
		t.Fatalf("请求失败要提示：\n%s", plainView(m))
	}
	if m.pathBusy {
		t.Fatal("失败后可以再按")
	}
	api.err = nil
	if pKey(m) == nil {
		t.Fatal("失败后再按应能重试")
	}
	// deckd 回：最近一次刷新失败，仍用上一份好快照
	m.pathBusy = false
	tools := scopedTools("codex-cli 0.164.0", "/h/bin")
	for i := range tools {
		tools[i].Note = "刷新会话 PATH 失败，仍用上一次读到的：找不到会话的 PATH：运行 /bin/zsh 失败：exit status 1"
		tools[i].NoteCode = "err.deckd.servergettools.refreshfail"
		tools[i].NoteArgs = []any{map[string]any{"code": "err.sessionenv.failed", "args": []any{"/bin/zsh", "exit status 1"}, "error": "找不到会话的 PATH：运行 /bin/zsh 失败：exit status 1"}}
	}
	m.tools, m.deckTools = tools, tools
	zh := flat(plainView(m))
	if !strings.Contains(zh, "刷新会话 PATH 失败，仍用上一次读到的") || !strings.Contains(zh, "p 重新读取会话 PATH") || strings.Contains(zh, "按 p 重试") {
		t.Fatalf("刷新失败：显示原因，仍是平时的说明（没退回 deckd 自己的 PATH）：\n%s", plainView(m))
	}
	i18n.SetLang(i18n.EN)
	v := plainView(m)
	if r, bad := hanIn(v); bad {
		t.Fatalf("英文有汉字 %q：\n%s", r, v)
	}
	if f := flat(v); !strings.Contains(f, "Refreshing the session PATH failed; the last one read is still in use") {
		t.Fatalf("英文：\n%s", v)
	}
}

// deckd 没有用会话 PATH（测试、没开）或接口没有刷新能力：没有这个键，也没有相关文字，页面与以前一模一样。
func TestN060S4FixUINoKeyWithoutSessionPath(t *testing.T) {
	plain := scopedTools("codex-cli 0.164.0", "/h/bin")
	for i := range plain {
		plain[i].PathSource = ""
	}
	m, api := refreshFixture(t, plain)
	openAdd(t, m)
	if pKey(m) != nil || api.calls.Load() != 0 {
		t.Fatal("没开会话 PATH：p 什么都不做")
	}
	if v := flat(plainView(m)); strings.Contains(v, "会话 PATH") || strings.Contains(v, "登录启动文件") {
		t.Fatalf("没开会话 PATH：页面不该多出相关文字：\n%s", plainView(m))
	}
	// 接口没有刷新能力（旧接口、别的假接口）
	m2, _, _ := kimiFixture(t)
	openAdd(t, m2)
	if pKey(m2) != nil {
		t.Fatal("接口没有刷新能力：p 什么都不做")
	}
}

// deckd 说有一次读在跑（启动那次，或别的窗口按的）：页面显示「正在刷新」，英文下没有汉字；80x24 的窗口里页面不超出高度。
func TestN060S4FixUIShowsARefreshStartedElsewhereAndFitsSmallWindows(t *testing.T) {
	busy := scopedTools("codex-cli 0.164.0", "/h/bin")
	for i := range busy {
		busy[i].PathRefreshing = true
	}
	m, _ := refreshFixture(t, busy)
	m.width, m.height = 80, 24
	for _, open := range []func(){func() { openAdd(t, m) }, func() { openCodex(t, m) }} {
		open()
		if f := flat(plainView(m)); !strings.Contains(f, "正在刷新会话 PATH") {
			t.Fatalf("别处按的刷新也要显示：\n%s", plainView(m))
		}
		if n := strings.Count(plainView(m), "\n"); n > 24 {
			t.Fatalf("80x24 里页面不超出高度：%d 行\n%s", n, plainView(m))
		}
		i18n.SetLang(i18n.EN)
		v := plainView(m)
		if r, bad := hanIn(v); bad || !strings.Contains(flat(v), "Refreshing the session PATH") {
			t.Fatalf("英文：%q %v\n%s", r, bad, v)
		}
		i18n.SetLang(i18n.ZH)
		press(m, "esc")
	}
}
