package deckd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/dashquota"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// n057：设置界面配置全局 agent 与模型、项目每档有序多选。deckd 这边：整份保存带代数、启动相关字段不许界面改、
// 新加 agent 只能取自工具目录、主 agent 那一档要有它的模型；按档位挑模型跳过额度确知用尽的；工具目录与列模型；
// grok、antigravity 经命令传模型、上游认错工具就停掉。

func getSettings(t *testing.T, f *fixture) deckstore.Settings {
	t.Helper()
	code, b := f.do("owner", "GET", "/v1/settings", "")
	if code != 200 {
		t.Fatalf("读设置：%d %s", code, b)
	}
	var s deckstore.Settings
	if err := json.Unmarshal([]byte(b), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func putSettings(f *fixture, s deckstore.Settings, gen *int) (int, string) {
	body := map[string]any{"agents": s.Agents, "projects": s.Projects}
	if gen != nil {
		body["generation"] = *gen
	}
	b, _ := json.Marshal(body)
	return f.do("owner", "PUT", "/v1/settings", string(b))
}

func TestN057NewEndpointsPermissions(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct {
		path string
		want map[string]int
	}{
		{"/v1/tools", map[string]int{"owner": 200, "main": 403, "sub": 403, "unknown": 403}},
		{"/v1/tools/models?tool=claude", map[string]int{"owner": 200, "main": 403, "sub": 403, "unknown": 403}},
		{"/v1/resolve?project=alpha&tier=frontier", map[string]int{"owner": 200, "main": 200, "sub": 403, "unknown": 403}},
	} {
		for as, want := range c.want {
			if got, b := f.do(as, "GET", c.path, ""); got != want {
				t.Errorf("%s 以 %s 调用：%d（期望 %d）%s", c.path, as, got, want, b)
			}
		}
	}
}

func TestPutSettingsNeedsCurrentGeneration(t *testing.T) {
	f := newFixture(t)
	s := getSettings(t, f)
	if code, b := putSettings(f, s, nil); code != 400 || !strings.Contains(b, "generation") {
		t.Fatalf("不带代数：400：%d %s", code, b)
	}
	g := s.Generation
	if code, b := putSettings(f, s, &g); code != 200 {
		t.Fatalf("带当前代数：200：%d %s", code, b)
	}
	if code, b := putSettings(f, s, &g); code != 409 || !strings.Contains(b, "被别处改过") {
		t.Fatalf("拿旧代数再存：409：%d %s", code, b)
	}
	if now := getSettings(t, f).Generation; now <= g { // rev 127 起按时间走，不是正好 +1
		t.Fatalf("代数应增大：%d → %d", g, now)
	}
}

func TestPutSettingsKeepsLaunchFieldsOutOfUI(t *testing.T) {
	f := newFixture(t)
	try := func(what string, edit func(*deckstore.Settings), wantCode int, wantText string) {
		t.Helper()
		s := getSettings(t, f)
		edit(&s)
		g := s.Generation
		if code, b := putSettings(f, s, &g); code != wantCode || !strings.Contains(b, wantText) {
			t.Errorf("%s：期望 %d 含 %q，实际 %d %s", what, wantCode, wantText, code, b)
		}
	}
	idx := func(s *deckstore.Settings, id string) int {
		for i, a := range s.Agents {
			if a.ID == id {
				return i
			}
		}
		t.Fatalf("没有 %s", id)
		return -1
	}
	try("改已有 agent 的命令", func(s *deckstore.Settings) { s.Agents[idx(s, "claude")].Binary = "/tmp/evil" }, 400, "binary")
	try("改启动额外参数", func(s *deckstore.Settings) {
		s.Agents[idx(s, "claude")].ExtraArgs = [][]string{{"--allowedTools", "Bash"}}
	}, 400, "extra_args")
	try("改 agent 标记", func(s *deckstore.Settings) { s.Agents[idx(s, "codex")].Agent = true }, 400, "agent 标记")
	try("改工具名", func(s *deckstore.Settings) { s.Agents[idx(s, "codex")].Tool = "claude" }, 400, "工具名")
	try("新加目录外的工具", func(s *deckstore.Settings) {
		s.Agents = append(s.Agents, deckstore.AgentDef{ID: "evil", Name: "E", Tool: "bash", Binary: "bash", Agent: true})
	}, 400, "不在安排 Anpai 认识的工具里")
	// 默认清单里没有 gemini：按目录加入，但命令不照目录
	try("新加的命令不照目录", func(s *deckstore.Settings) {
		s.Agents = append(s.Agents, deckstore.AgentDef{ID: "gemini", Name: "G", Tool: "gemini", Binary: "/tmp/gemini"})
	}, 400, "照工具目录")
	try("新加的带额外参数", func(s *deckstore.Settings) {
		s.Agents = append(s.Agents, deckstore.AgentDef{ID: "gemini", Name: "G", Tool: "gemini", Binary: "gemini", ExtraArgs: [][]string{{"--yolo"}}})
	}, 400, "额外参数")
	try("主 agent 那一档没有它的模型", func(s *deckstore.Settings) {
		s.Projects[0].Agents = []string{"claude", "codex"}
		s.Projects[0].MainAgent = deckstore.MainAgent{Agent: "codex", Tier: deckstore.Frontier}
	}, 400, "没有 codex 的模型")
	// 合规的：改显示名、加减模型与档位、按目录新加 gemini、项目每档多选
	try("合规的改动", func(s *deckstore.Settings) {
		c := &s.Agents[idx(s, "codex")]
		c.Name = "Codex（公司）"
		c.Models = append(c.Models, deckstore.ModelDef{ID: "gpt-6.1-sol-mini", Tier: deckstore.Balanced})
		s.Agents = append(s.Agents, deckstore.AgentDef{ID: "gemini", Name: "Gemini CLI", Tool: "gemini", Binary: "gemini",
			Models: []deckstore.ModelDef{{ID: "gemini-3.8-pro", Tier: deckstore.Frontier}}})
		s.Projects[0].Agents = []string{"claude", "codex", "gemini"}
		s.Projects[0].Models[deckstore.Frontier] = deckstore.ModelList{"claude-opus-5-5", "gemini-3.8-pro", "gpt-6-astra"}
	}, 200, "")
	got := getSettings(t, f)
	if strings.Join(got.Projects[0].Models[deckstore.Frontier], ",") != "claude-opus-5-5,gemini-3.8-pro,gpt-6-astra" {
		t.Fatalf("多选清单按顺序存下：%v", got.Projects[0].Models)
	}
}

// 按档位挑模型：清单第一个的 agent 额度用尽就取下一个；/v1/resolve 与派工同一个挑法（args_sha256 对得上）。
func TestTierListSkipsExhaustedAgent(t *testing.T) {
	w := newAgentWorld(t)
	set := w.store.Settings()
	for i := range set.Projects {
		set.Projects[i].Models[deckstore.Balanced] = deckstore.ModelList{"gpt-6.1-sol", "claude-sonnet-5-5"}
	}
	if err := w.store.ReplaceSettings(set); err != nil {
		t.Fatal(err)
	}
	w.srv.agents.quotaOf = func(_ context.Context, tool string) string {
		if tool == "codex" {
			return dashquota.Exhausted
		}
		return dashquota.Unknown // 额度未知不算用尽
	}
	code, b := w.do("main", "GET", "/v1/resolve?tier=balanced", "")
	var r deckstore.Resolved
	json.Unmarshal([]byte(b), &r)
	if code != 200 || r.Model != "claude-sonnet-5-5" || r.Agent.ID != "claude" {
		t.Fatalf("codex 用尽：取清单里的下一个：%d %s", code, b)
	}
	if code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "写测试", "t", "n57a", "/tmp/alpha", "", "claude", "claude-sonnet-5-5")); code != 201 {
		t.Fatalf("按 resolve 的结果算指纹，派工应开成：%d %s", code, b)
	}
	if l := w.up.launches[len(w.up.launches)-1]; l.Tool != "claude" || l.Model != "claude-sonnet-5-5" {
		t.Fatalf("实际起的：%+v", l)
	}
	// 清单都用尽：balanced 说明原因、不派
	w.srv.agents.quotaOf = func(context.Context, string) string { return dashquota.Exhausted }
	if code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "写测试", "t", "n57b", "/tmp/alpha", "", "claude", "claude-sonnet-5-5")); code != 400 || !strings.Contains(b, "都已用尽") {
		t.Fatalf("全部用尽：400 并说明：%d %s", code, b)
	}
	if code, b := w.do("main", "GET", "/v1/resolve?tier=balanced", ""); code != 400 || !strings.Contains(b, "都已用尽") {
		t.Fatalf("resolve 同样说明：%d %s", code, b)
	}
}

func TestToolsCatalogAndModelList(t *testing.T) {
	w := newAgentWorld(t)
	code, b := w.do("owner", "GET", "/v1/tools", "")
	var tools []ToolStatus
	json.Unmarshal([]byte(b), &tools)
	if code != 200 || len(tools) != len(Catalog) {
		t.Fatalf("工具目录：%d %s", code, b)
	}
	byTool := map[string]ToolStatus{}
	for _, x := range tools {
		byTool[x.Tool] = x
	}
	if !byTool["claude"].Added || byTool["antigravity"].Binary != "agy" || !byTool["grok"].CanList || byTool["claude"].CanList ||
		!byTool["opencode"].Installed || byTool["opencode"].Path != "/usr/bin/opencode" {
		t.Fatalf("目录项：%+v", byTool)
	}
	old := listModels
	t.Cleanup(func() { listModels = old })
	var ran []string
	listModels = func(_ context.Context, bin string, args []string) (string, error) {
		ran = append(ran, bin+" "+strings.Join(args, " "))
		return "You are using XAI_API_KEY.\n\nDefault model: grok-4.7\n\nAvailable models:\n  * grok-4.7 (default)\n  - grok-4.7-build-fast\n  - grok-4.5\n", nil
	}
	code, b = w.do("owner", "GET", "/v1/tools/models?tool=grok", "")
	if code != 200 || !strings.Contains(b, `["grok-4.7","grok-4.7-build-fast","grok-4.5"]`) || strings.Join(ran, ";") != "grok models" {
		t.Fatalf("grok models：%d %s %v", code, b, ran)
	}
	if code, b := w.do("owner", "GET", "/v1/tools/models?tool=claude", ""); code != 200 || !strings.Contains(b, "手填") || len(ran) != 1 {
		t.Fatalf("claude 没有列模型的命令：不跑任何东西：%d %s", code, b)
	}
	if code, _ := w.do("owner", "GET", "/v1/tools/models?tool=bash", ""); code != 404 {
		t.Fatalf("目录外的工具：404：%d", code)
	}
}

func TestModelParsers(t *testing.T) {
	agy := "Fetching available models...\ngemini-3.8-flash-high\tGemini 3.8 Flash (High)\nclaude-opus-4-6-thinking\tClaude Opus 4.6 (Thinking)\n"
	if got := strings.Join(parseTabbedModels(agy), ","); got != "gemini-3.8-flash-high,claude-opus-4-6-thinking" {
		t.Fatalf("agy：%s", got)
	}
	oc := "jwipc/glm-5.3\nopencode/claude-opus-5-5\nsome header line\n\n"
	if got := strings.Join(parsePlainModels(oc), ","); got != "jwipc/glm-5.3,opencode/claude-opus-5-5" {
		t.Fatalf("opencode：%s", got)
	}
	if got := parseBulletModels("  * $(rm -rf ~)\n  - ok-1\n"); strings.Join(got, ",") != "ok-1" {
		t.Fatalf("不合规的 id 丢掉：%v", got)
	}
}

func grokWorld(t *testing.T) *agentWorld {
	w := newAgentWorld(t)
	set := w.store.Settings()
	for i := range set.Projects {
		set.Projects[i].Agents = []string{"claude", "codex", "grok"}
		set.Projects[i].Models[deckstore.Balanced] = deckstore.ModelList{"grok-4.7-build-fast"}
	}
	if err := w.store.ReplaceSettings(set); err != nil {
		t.Fatal(err)
	}
	b := w.srv.agents.bypass
	b.NotCodex = func(tool string) bool { return tool == "grok" }
	w.srv.WithBypassChecks(b)
	return w
}

func TestGrokLaunchPassesModelAndChecksTool(t *testing.T) {
	w := grokWorld(t)
	if code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "试 grok", "t", "g57a", "/tmp/alpha", "", "grok", "grok-4.7-build-fast")); code != 201 {
		t.Fatalf("登记过、不是 Codex 兼容的 grok：不问 Codex 授权、直接开：%d %s", code, b)
	}
	l := w.up.launches[len(w.up.launches)-1]
	if l.Tool != "grok" || l.Model != "grok-4.7-build-fast" {
		t.Fatalf("启动请求：%+v", l)
	}
	// 上游把它起成了别的工具（没登记）：停掉、不送任务
	w.up.launchOut = &agentdeck.LaunchResult{SessionID: "ad-wrong", Success: true, Tool: "claude"}
	code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "试 grok", "t", "g57b", "/tmp/alpha", "", "grok", "grok-4.7-build-fast"))
	if code != 502 || !strings.Contains(b, "[tools.grok]") || !strings.Contains(strings.Join(w.up.stops, ","), "ad-wrong") {
		t.Fatalf("起错工具应停掉：%d %s stops=%v", code, b, w.up.stops)
	}
	// 没确认不是 Codex 兼容：照旧问 owner
	b2 := w.srv.agents.bypass
	b2.NotCodex = nil
	w.srv.WithBypassChecks(b2)
	w.up.launchOut = nil
	if code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "试 grok", "t", "g57c", "/tmp/alpha", "", "grok", "grok-4.7-build-fast")); code != 202 || !strings.Contains(b, "codex_trust") {
		t.Fatalf("确认不了时照旧上报 Codex 授权：%d %s", code, b)
	}
}

// 自查：generation 不能是负数；不合规的模型 id 保存时拒绝，手改进来的派工时拒绝；没登记的 grok 被上游起成 shell 也停掉。
func TestN057SelfCheckGuards(t *testing.T) {
	f := newFixture(t)
	s := getSettings(t, f)
	neg := -1
	if code, b := putSettings(f, s, &neg); code != 400 {
		t.Fatalf("generation=-1：400：%d %s", code, b)
	}
	for i := range s.Agents {
		if s.Agents[i].ID == "claude" {
			s.Agents[i].Models = append(s.Agents[i].Models, deckstore.ModelDef{ID: "x;touch${IFS}/tmp/pwned", Tier: deckstore.Economy})
		}
	}
	g := s.Generation
	if code, b := putSettings(f, s, &g); code != 400 || !strings.Contains(b, "不合规") {
		t.Fatalf("不合规的模型 id：400：%d %s", code, b)
	}

	w := grokWorld(t)
	set := w.store.Settings()
	for i := range set.Agents {
		if set.Agents[i].ID == "grok" {
			set.Agents[i].Models = append(set.Agents[i].Models, deckstore.ModelDef{ID: "{command}", Tier: deckstore.Economy})
		}
	}
	for i := range set.Projects {
		set.Projects[i].Models[deckstore.Economy] = deckstore.ModelList{"{command}"}
	}
	if err := w.store.ReplaceSettings(set); err != nil { // 手改进来（不经 PUT）
		t.Fatal(err)
	}
	n := len(w.up.launches)
	if code, b := w.do("main", "POST", "/v1/agents", spawnJSON("economy", "试", "t", "g57x", "/tmp/alpha", "", "grok", "{command}")); code != 400 || !strings.Contains(b, "不合规") || len(w.up.launches) != n {
		t.Fatalf("手改进来的不合规 id：派工前拒绝、不启动：%d %s", code, b)
	}
	w.up.launchOut = &agentdeck.LaunchResult{SessionID: "ad-shell", Success: true, Tool: "shell", Command: "grok --model 'grok-4.7-build-fast'"}
	code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "试 grok", "t", "g57y", "/tmp/alpha", "", "grok", "grok-4.7-build-fast"))
	if code != 502 || !strings.Contains(b, "[tools.grok]") || !strings.Contains(strings.Join(w.up.stops, ","), "ad-shell") {
		t.Fatalf("没登记时上游起成 shell：也停掉：%d %s stops=%v", code, b, w.up.stops)
	}
	// n060 段 4 起 kimi 在目录里了（Kimi Code 2.3.2 的 --model 已核实，模型经命令传，同 grok）；这一条原先断言「kimi 不在目录里」，
	// 现在改成断言它在、而且走和 grok 同一条路（见 TestN060S4CatalogDefaults、TestN060S4KimiSpawnPassesTheModelAndChecksTheTool）
	if _, ok := CatalogTool("kimi"); !ok {
		t.Fatal("kimi 应在可加入的目录里（n060 段 4）")
	}
}

// rev 127：PUT 回的是这次存下的那份与它的代数，界面用它接着存；另一个窗口紧接着存过，回的也不是那个窗口的代数。
func TestPutSettingsReturnsSavedGeneration(t *testing.T) {
	f := newFixture(t)
	s := getSettings(t, f)
	g := s.Generation
	code, b := putSettings(f, s, &g)
	if code != 200 {
		t.Fatalf("保存：%d %s", code, b)
	}
	var out deckstore.Settings
	if err := json.Unmarshal([]byte(b), &out); err != nil {
		t.Fatal(err)
	}
	if out.Generation <= g || out.Generation != getSettings(t, f).Generation {
		t.Fatalf("回的代数应是存下的：%d（读到 %d，当前 %d）", out.Generation, g, getSettings(t, f).Generation)
	}
	next := out.Generation
	if code, b := putSettings(f, s, &next); code != 200 {
		t.Fatalf("用回的代数接着存：%d %s", code, b)
	}
}

// 自查补测：PUT 落盘之后、回包之前另一处（另一个窗口、deck project add）接着存了，回包仍是这次存下的代数；
// 界面拿它再存应被拒（以前回的是保存后再读的当前设置，会拿到别人的代数、接着盖掉别人的保存）。
func TestPutSettingsResponseIsNotTheNextWindowsGeneration(t *testing.T) {
	f := newFixture(t)
	s := getSettings(t, f)
	g := s.Generation
	other, fired := 0, false
	f.store.OnChange(func(e deckstore.Event) {
		if e.Kind != "settings" || fired {
			return
		}
		fired = true // 这次保存本身也会触发 hook：只插一次
		cur := f.store.Settings()
		cur.Projects[0].Name = "OTHER"
		gen, err := f.store.ReplaceSettingsAtGen(cur, cur.Generation)
		if err != nil {
			t.Errorf("另一个窗口保存：%v", err)
		}
		other = gen
	})
	code, b := putSettings(f, s, &g)
	if code != 200 {
		t.Fatalf("保存：%d %s", code, b)
	}
	var out deckstore.Settings
	if err := json.Unmarshal([]byte(b), &out); err != nil {
		t.Fatal(err)
	}
	if other == 0 || out.Generation == other || out.Generation <= g {
		t.Fatalf("回包应是这次存下的代数，不是紧接着另一处存的：回 %d、另一处 %d、读到 %d", out.Generation, other, g)
	}
	f.store.OnChange(nil)
	next := out.Generation
	if code, b := putSettings(f, s, &next); code != 409 {
		t.Fatalf("另一处存过之后，拿回包的代数再存应 409：%d %s", code, b)
	}
}
