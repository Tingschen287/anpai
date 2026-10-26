package deckui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// n060 段 4：设置界面里「装没装」以 deckd 按会话 PATH 查的为准；回退到 deckd 自己的 PATH 时提示 owner；Codex 候选自动列出、失败退回手填；加入 Kimi。

// preloadAPI 是带「读完设置后后台取工具目录」能力的假接口（真接口 DeckSettingsAPI 有它）。
type preloadAPI struct {
	*fakeSettingsAPI
	calls *atomic.Int32
}

func (a preloadAPI) PreloadTools() bool { return true }
func (a preloadAPI) Tools(ctx context.Context) ([]ToolInfo, error) {
	a.calls.Add(1)
	return a.fakeSettingsAPI.Tools(ctx)
}

// bgMsgs 把一条命令（可能是 Batch）里能立刻跑完的消息取出来；定时 tick 不跑（跑了会睡 2 秒）。
func bgMsgs(c tea.Cmd) []tea.Msg {
	if c == nil {
		return nil
	}
	switch out := c().(type) {
	case tea.BatchMsg:
		var all []tea.Msg
		for _, x := range out {
			all = append(all, bgMsgs(x)...)
		}
		return all
	case nil:
		return nil
	default:
		return []tea.Msg{out}
	}
}

func kimiFixture(t *testing.T) (*Settings, *fakeSettingsAPI, *atomic.Int32) {
	t.Helper()
	t.Setenv("PATH", t.TempDir()) // 本进程的 PATH 里什么都找不到：「装了」只能来自 deckd 的回答
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	i18n.SetLang(i18n.ZH)
	base := &fakeSettingsAPI{
		set: deckstore.Settings{Generation: 3, Agents: deckstore.DefaultAgents(), Projects: []deckstore.ProjectDef{{ID: "ai", Name: "ai", Path: "/tmp/ai",
			Agents: []string{"claude"}, Models: map[deckstore.Tier]deckstore.ModelList{deckstore.Frontier: {"claude-opus-5-5"}},
			MainAgent: deckstore.MainAgent{Agent: "claude", Tier: deckstore.Frontier}, Envelope: deckstore.DefaultEnvelope("ai")}}},
		tools: []ToolInfo{
			{Tool: "claude", Name: "Claude Code", Binary: "claude", Installed: true, Path: "/h/.local/bin/claude", Version: "2.9.1", PathSource: "login-shell"},
			{Tool: "codex", Name: "Codex", Binary: "codex", Installed: true, Path: "/h/.local/bin/codex", Version: "codex-cli 0.164.0", CanList: true, PathSource: "login-shell",
				Models: []deckstore.ModelDef{{ID: "gpt-6-astra", Tier: deckstore.Frontier}, {ID: "gpt-6.1-sol", Tier: deckstore.Balanced}, {ID: "gpt-6-luna", Tier: deckstore.Economy}}},
			{Tool: "kimi", Name: "Kimi Code", Binary: "kimi", Agent: true, Installed: true, Path: "/h/.kimi-code/bin/kimi", Version: "kimi 2.3.2", PathSource: "login-shell",
				Models: []deckstore.ModelDef{{ID: "kimi-code/k3", Tier: deckstore.Balanced}, {ID: "kimi-for-coding", Tier: deckstore.Economy}}},
		},
		models: map[string][]string{"codex": {"gpt-6.1-sol", "gpt-6-astra", "gpt-6-sol", "gpt-6-luna"}},
	}
	// 全局清单里没有 kimi（要在界面里加入）；codex 在
	var agents []deckstore.AgentDef
	for _, a := range base.set.Agents {
		if a.ID == "claude" || a.ID == "codex" {
			agents = append(agents, a)
		}
	}
	base.set.Agents = agents
	calls := &atomic.Int32{}
	m := &Settings{API: preloadAPI{base, calls}}
	m.width, m.height = 110, 40
	_, cmd := m.Update(m.load())
	for _, msg := range bgMsgs(cmd) {
		m.Update(msg)
	}
	return m, base, calls
}

// 「没装」标记以 deckd 按会话 PATH 查的为准：本进程的 PATH 里什么都没有，deckd 说装了的就是装了，deckd 说没装的才标没装。
func TestN060S4InstalledMarksFollowDeckdNotThisProcessPath(t *testing.T) {
	m, api, calls := kimiFixture(t)
	if calls.Load() != 1 || len(m.deckTools) != 3 {
		t.Fatalf("读完设置后后台取一次工具目录：%d 次，%d 项", calls.Load(), len(m.deckTools))
	}
	m.switchPage(pageAgents)
	if markOf(t, m, "Claude Code") != "●" || markOf(t, m, "Codex") != "●" {
		t.Fatalf("deckd 说装了：标已装（本进程的 PATH 里确实没有它）：\n%s", plainView(m))
	}
	// deckd 说 codex 没装：标没装（即使本进程的 PATH 里有）
	api.tools[1].Installed = false
	m.deckTools = api.tools
	if markOf(t, m, "Codex") != "○" || markOf(t, m, "Claude Code") != "●" {
		t.Fatalf("deckd 说没装：标没装：\n%s", plainView(m))
	}
	// 命令与目录里的不一致（owner 手改过 binary）：不拿目录的答案顶替，退回本进程的 PATH（这里找不到）
	for i := range m.set.Agents {
		if m.set.Agents[i].ID == "claude" {
			m.set.Agents[i].Binary = "/opt/claude-custom"
		}
	}
	if markOf(t, m, "Claude Code") != "○" {
		t.Fatalf("命令不照目录：以本进程的 PATH 为准：\n%s", plainView(m))
	}
	// 测试注入的 Installed 钩子仍然最优先
	m.Installed = func(string) bool { return true }
	if markOf(t, m, "Claude Code") != "●" || markOf(t, m, "Codex") != "●" {
		t.Fatalf("钩子优先：\n%s", plainView(m))
	}
}

// markOf 是全局 agent 页里某个 agent 那一行的标记：● 已装，○ 没装。
func markOf(t *testing.T, m *Settings, name string) string {
	t.Helper()
	for _, l := range strings.Split(plainView(m), "\n") {
		if i := strings.Index(l, name); i > 0 {
			if pre := strings.TrimSpace(l[:i]); strings.HasSuffix(pre, "●") {
				return "●"
			} else if strings.HasSuffix(pre, "○") {
				return "○"
			}
		}
	}
	t.Fatalf("没找到 %s 那一行：\n%s", name, plainView(m))
	return ""
}

// flat 把折行与缩进压成单个空格，查文字时不受折行位置影响。
func flat(s string) string { return strings.Join(strings.Fields(s), " ") }

// 没有 PreloadTools 能力的接口（旧 deckd、测试里的假接口）不取，行为与以前一样：本进程 PATH 里找。
func TestN060S4NoPreloadMeansTheOldLookup(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	api := &fakeSettingsAPI{set: deckstore.Settings{Agents: deckstore.DefaultAgents()}, tools: []ToolInfo{{Tool: "kimi", Binary: "kimi", Installed: true}}}
	m := &Settings{API: api}
	_, cmd := m.Update(m.load())
	if cmd != nil || m.toolsAsk || len(m.deckTools) != 0 {
		t.Fatalf("不取：%v %v %v", cmd, m.toolsAsk, m.deckTools)
	}
	m.switchPage(pageAgents)
	if !strings.Contains(plainView(m), "（没装）") {
		t.Fatalf("PATH 里找不到：标没装\n%s", plainView(m))
	}
	// 静默重读、只换授权这类读取不会再取
	p := preloadAPI{api, &atomic.Int32{}}
	m2 := &Settings{API: p}
	_, c1 := m2.Update(m2.load())
	if len(bgMsgs(c1)) != 1 {
		t.Fatal("第一次读完取一次")
	}
	_, c2 := m2.Update(loadedMsg{set: api.set, quiet: true, stamp: quietStamp{}})
	_, c3 := m2.Update(m2.loadTrust())
	if len(bgMsgs(c2)) != 0 || len(bgMsgs(c3)) != 0 {
		t.Fatal("之后的读取不再取")
	}
	// 后台取失败不出声，也不丢掉以前取到的
	m2.deckTools = []ToolInfo{{Tool: "kimi", Binary: "kimi", Installed: true}}
	m2.Update(toolsBgMsg{err: context.DeadlineExceeded})
	if len(m2.deckTools) != 1 || !m2.msg.empty() {
		t.Fatalf("失败不出声：%v %q", m2.deckTools, m2.msg.String())
	}
}

// 加入本机的 agent 一页：路径与版本是会话 PATH 上的那份；取不到会话 PATH 回退时给出原因（橙色一行，随语言翻译）；Kimi 能加入。
func TestN060S4AddViewShowsPathsAndTheFallbackNote(t *testing.T) {
	m, api, _ := kimiFixture(t)
	goRow(t, m, rowAddAgent, "", "")
	press(m, "enter")
	v := plainView(m)
	for _, want := range []string{"Kimi Code", "/h/.kimi-code/bin/kimi", "kimi 2.3.2"} {
		if !strings.Contains(v, want) {
			t.Fatalf("加入页缺 %q：\n%s", want, v)
		}
	}
	if strings.Contains(v, "没取到会话") {
		t.Fatalf("取到了会话 PATH：不该有回退提示：\n%s", v)
	}
	// 回退：每项带来源与原因
	press(m, "esc")
	note := []any{map[string]any{"code": "err.sessionenv.timeout", "args": []any{"/bin/zsh", "5s"}, "error": "找不到会话的 PATH：/bin/zsh 在 5s 内没有给出结果（~/.zshrc 之类的启动文件可能卡住了）"}}
	for i := range api.tools {
		api.tools[i].PathSource = "process"
		api.tools[i].Note = "没取到会话的 PATH，已改用 deckd 自己的 PATH 找 CLI：找不到会话的 PATH：/bin/zsh 在 5s 内没有给出结果（~/.zshrc 之类的启动文件可能卡住了）"
		api.tools[i].NoteCode = "err.deckd.servergettools.nopath"
		api.tools[i].NoteArgs = note
	}
	goRow(t, m, rowAddAgent, "", "")
	press(m, "enter")
	if v := flat(plainView(m)); !strings.Contains(v, "没取到会话的 PATH，已改用 deckd 自己的 PATH 找 CLI") || !strings.Contains(v, "/bin/zsh 在 5s 内没有给出结果") {
		t.Fatalf("中文提示：\n%s", plainView(m))
	}
	i18n.SetLang(i18n.EN)
	v = plainView(m)
	if r, bad := hanIn(v); bad {
		t.Fatalf("英文下有汉字 %q：\n%s", r, v)
	}
	if f := flat(v); !strings.Contains(f, "Couldn't get the session PATH, so deckd's own PATH is used to find the CLIs") || !strings.Contains(f, "gave no result within 5s") {
		t.Fatalf("英文提示：\n%s", v)
	}
	// 加入 Kimi：命令 kimi、手填模型、默认两档
	press(m, "esc")
	i18n.SetLang(i18n.ZH)
	goRow(t, m, rowAddAgent, "", "")
	press(m, "enter")
	for i, o := range m.addable() {
		if o.Tool == "kimi" {
			m.sub.cur = i
		}
	}
	press(m, "enter")
	a, ok := m.agent("kimi")
	if !ok || a.Binary != "kimi" || !a.Agent || len(a.Models) != 2 || a.Models[0] != (deckstore.ModelDef{ID: "kimi-code/k3", Tier: deckstore.Balanced}) || a.Models[1] != (deckstore.ModelDef{ID: "kimi-for-coding", Tier: deckstore.Economy}) {
		t.Fatalf("加入后的 Kimi：%+v %v", a, ok)
	}
	if len(api.saved) == 0 {
		t.Fatal("加入后自动保存")
	}
	// 没有列模型的命令：加模型时手填，不去问 CLI
	press(m, "+")
	if m.sub.input == nil || m.sub.input.loading || !strings.Contains(m.sub.input.note.String(), "手填") {
		t.Fatalf("Kimi 手填模型：%+v %q", m.sub.input, m.sub.input.note.String())
	}
}

// Codex 加模型：候选自动列出（来自 codex debug models）；失败时退回手填并给出可译的提示，中英各一遍。
func TestN060S4CodexCandidatesAreListedAndFailureFallsBackToManual(t *testing.T) {
	m, api, _ := kimiFixture(t)
	goRow(t, m, rowGlobal, "codex", "")
	press(m, "enter") // 打开 codex；顺便取工具目录
	if !m.toolInfoCanList("codex") {
		t.Fatal("codex 现在能列模型")
	}
	_, cmd := m.Update(keyMsg("+"))
	if m.sub.input == nil || !m.sub.input.loading || !strings.Contains(m.sub.input.note.String(), "正在问 codex 有哪些模型") {
		t.Fatalf("先说正在取：%+v %q", m.sub.input, m.sub.input.note.String())
	}
	run(m, cmd)
	in := m.sub.input
	if in.loading || strings.Join(in.all, ",") != "gpt-6.1-sol,gpt-6-astra,gpt-6-sol,gpt-6-luna" {
		t.Fatalf("候选：%+v", in)
	}
	v := plainView(m)
	for _, want := range []string{"gpt-6-astra", "gpt-6-sol", "（已有）"} { // 已有的标出来
		if !strings.Contains(v, want) {
			t.Fatalf("候选画面缺 %q：\n%s", want, v)
		}
	}
	press(m, "esc", "esc")
	// 失败：deckd 回 502 + 码（嵌套 badjson）
	api.modelsErr = &deckclient.Error{Status: 502, Code: "err.deckd.servergettoolmodels.2",
		Args: []any{"codex", "debug models", map[string]any{"code": "err.deckd.codexmodels.badjson", "args": []any{"invalid character 'E' looking for beginning of value"},
			"error": "codex debug models 的输出不是预期的 JSON：invalid character 'E' looking for beginning of value"}},
		Message: "codex debug models 没有成功：codex debug models 的输出不是预期的 JSON：invalid character 'E' looking for beginning of value（可以手填模型 id）"}
	goRow(t, m, rowGlobal, "codex", "")
	press(m, "enter", "+")
	zh := plainView(m)
	if f := flat(zh); !strings.Contains(f, "列不出模型") || !strings.Contains(f, "手填") || !strings.Contains(f, "不是预期的 JSON") {
		t.Fatalf("中文：失败时退回手填并说明原因：\n%s", zh)
	}
	if m.sub.input.loading || len(m.sub.input.all) != 0 {
		t.Fatalf("失败后可以直接手填：%+v", m.sub.input)
	}
	i18n.SetLang(i18n.EN)
	en := plainView(m)
	if r, bad := hanIn(en); bad {
		t.Fatalf("英文下有汉字 %q：\n%s", r, en)
	}
	if f := flat(en); !strings.Contains(f, "Can't list models") || !strings.Contains(f, "Type the id") || !strings.Contains(f, "is not the expected JSON") {
		t.Fatalf("英文：\n%s", en)
	}
	// 手填一个 id 照样加得进去
	i18n.SetLang(i18n.ZH)
	for _, r := range "gpt-6.2-x" {
		press(m, string(r))
	}
	press(m, "enter")
	if a, _ := m.agent("codex"); !hasModel(a, "gpt-6.2-x") {
		t.Fatalf("手填的模型应加入：%+v", a.Models)
	}
}

func hasModel(a deckstore.AgentDef, id string) bool {
	for _, md := range a.Models {
		if md.ID == id {
			return true
		}
	}
	return false
}

func (m *Settings) toolInfoCanList(tool string) bool {
	t, ok := m.toolInfo(tool)
	return ok && t.CanList
}
