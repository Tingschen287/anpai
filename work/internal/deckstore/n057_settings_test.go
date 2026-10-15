package deckstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// n057：每档有序多选（旧的一档一个照读）、按清单与额度挑模型、主 agent 那一档要有它的模型、整份保存核对代数。

func TestModelListReadsOldScalarAndNewList(t *testing.T) {
	var p ProjectDef
	if err := yaml.Unmarshal([]byte("models:\n  frontier: claude-opus-5-5\n  balanced: [a, b]\n  economy: \"\"\n"), &p); err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Models[Frontier], ",") != "claude-opus-5-5" || strings.Join(p.Models[Balanced], ",") != "a,b" || len(p.Models[Economy]) != 0 {
		t.Fatalf("YAML：旧单值应读成一个元素的清单、新列表照读、空串当没设：%v", p.Models)
	}
	var q ProjectDef
	if err := json.Unmarshal([]byte(`{"models":{"frontier":"x","balanced":["y","z"]}}`), &q); err != nil {
		t.Fatal(err)
	}
	if q.Models[Frontier].First() != "x" || strings.Join(q.Models[Balanced], ",") != "y,z" {
		t.Fatalf("JSON 同样兼容：%v", q.Models)
	}
	out, _ := yaml.Marshal(ProjectDef{Models: map[Tier]ModelList{Frontier: {"a"}}})
	if !strings.Contains(string(out), "frontier:\n        - a") {
		t.Fatalf("写出时总是列表：\n%s", out)
	}
}

func TestOldProjectsFileStillLoads(t *testing.T) {
	d := testDirs(t)
	os.MkdirAll(d.Config, 0o700)
	ag, _ := yaml.Marshal(agentsFile{Generation: 4, Agents: DefaultAgents()})
	os.WriteFile(filepath.Join(d.Config, "agents.yaml"), ag, 0o600)
	os.WriteFile(filepath.Join(d.Config, "projects.yaml"), []byte(`generation: 4
projects:
    - id: ai
      name: ai
      path: /home/linux/ai
      agents: [claude, codex]
      models:
        balanced: claude-sonnet-5-5
        economy: claude-haiku-5-5
        frontier: claude-opus-5-5
      main_agent: {agent: claude, tier: frontier}
      envelope: {id: ai-default, version: 1, max_subagents: 3, max_frontier: 0, token_soft_cap: 2000000}
`), 0o600)
	s, err := LoadSettings(d.Config)
	if err != nil {
		t.Fatalf("n057 之前写的 projects.yaml 应照常能读：%v", err)
	}
	if s.Generation != 4 {
		t.Fatalf("应带上文件的代数：%d", s.Generation)
	}
	r, err := s.ResolveTier("ai", Frontier)
	if err != nil || r.Model != "claude-opus-5-5" || r.Agent.ID != "claude" {
		t.Fatalf("旧格式按原样解析：%+v %v", r, err)
	}
}

func multiSettings() Settings {
	return Settings{Agents: DefaultAgents(), Projects: []ProjectDef{{ID: "p", Name: "P", Path: "/tmp/p",
		Agents:    []string{"claude", "codex", "grok"},
		Models:    map[Tier]ModelList{Frontier: {"claude-opus-5-5", "gpt-6.1-sol", "grok-4.7"}, Balanced: {"claude-sonnet-5-5"}},
		MainAgent: MainAgent{Agent: "codex", Tier: Frontier}, Envelope: DefaultEnvelope("p")}}}
}

func TestResolveTierOrderAgentAndExhausted(t *testing.T) {
	s := multiSettings()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	none := func(AgentDef) bool { return false }
	if r, _ := s.ResolveTierFor("p", Frontier, "", none); r.Model != "claude-opus-5-5" {
		t.Fatalf("按清单顺序取第一个：%+v", r)
	}
	if r, _ := s.ResolveTierFor("p", Frontier, "codex", none); r.Model != "gpt-6.1-sol" || r.Agent.ID != "codex" {
		t.Fatalf("点了 agent 就只看它的模型：%+v", r)
	}
	asked := map[string]int{}
	claudeOut := func(a AgentDef) bool { asked[a.ID]++; return a.ID == "claude" }
	if r, _ := s.ResolveTierFor("p", Frontier, "", claudeOut); r.Model != "gpt-6.1-sol" {
		t.Fatalf("claude 额度用尽：取下一个：%+v", r)
	}
	if asked["claude"] != 1 || asked["grok"] != 0 {
		t.Fatalf("每个 agent 只问一次额度，取到就停：%v", asked)
	}
	allOut := func(AgentDef) bool { return true }
	_, err := s.ResolveTierFor("p", Frontier, "", allOut)
	var all *ErrAllExhausted
	if !errors.As(err, &all) || all.First.Model != "claude-opus-5-5" || len(all.Skipped) != 3 || !strings.Contains(err.Error(), "都已用尽") {
		t.Fatalf("全部用尽：说明原因并带上第一个：%v", err)
	}
	if _, err := s.ResolveTierFor("p", Balanced, "codex", none); err == nil || !strings.Contains(err.Error(), "没有 codex 的模型") {
		t.Fatalf("清单里没有这个 agent 的模型：%v", err)
	}
	if _, err := s.ResolveTierFor("p", Economy, "", none); err == nil {
		t.Fatal("没设的档位应报错")
	}
}

func TestValidateRejectsDuplicateAndWrongTierInList(t *testing.T) {
	s := multiSettings()
	s.Projects[0].Models[Frontier] = ModelList{"claude-opus-5-5", "claude-opus-5-5"}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("同一档里重复：%v", err)
	}
	s = multiSettings()
	s.Projects[0].Models[Frontier] = ModelList{"claude-opus-5-5", "claude-sonnet-5-5"}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "放进了") {
		t.Fatalf("清单里混进别的档位的模型：%v", err)
	}
	s = multiSettings()
	s.Projects[0].Models[Frontier] = ModelList{"claude-opus-5-5", "kimi-code/k3"}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "不属于") {
		t.Fatalf("清单里有没勾选的 agent 的模型：%v", err)
	}
}

func TestCheckMainAgentsOnlyOnSave(t *testing.T) {
	s := multiSettings()
	if err := s.CheckMainAgents(); err != nil {
		t.Fatal(err)
	}
	s.Projects[0].MainAgent = MainAgent{Agent: "grok", Tier: Balanced}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate 不查这一条（读取旧设置不被挡住）：%v", err)
	}
	if err := s.CheckMainAgents(); err == nil || !strings.Contains(err.Error(), "没有 grok 的模型") {
		t.Fatalf("主 agent 那一档清单里没有它的模型：保存时拒绝：%v", err)
	}
}

func TestReplaceSettingsAtChecksGeneration(t *testing.T) {
	d := testDirs(t)
	s, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	set := s.Settings()
	g0 := set.Generation
	if err := s.ReplaceSettingsAt(set, g0); err != nil {
		t.Fatal(err)
	}
	if got := s.Settings().Generation; got != g0+1 {
		t.Fatalf("保存后代数加一：%d → %d", g0, got)
	}
	err = s.ReplaceSettingsAt(set, g0) // 拿着旧代数再存
	var changed *ErrSettingsChanged
	if !errors.As(err, &changed) || !strings.Contains(err.Error(), "被别处改过") {
		t.Fatalf("旧代数应拒绝：%v", err)
	}
	if err := s.ReplaceSettingsAt(s.Settings(), s.Settings().Generation); err != nil {
		t.Fatalf("用当前代数可以存：%v", err)
	}
	re, _ := LoadSettings(d.Config)
	if re.Generation != s.Settings().Generation {
		t.Fatalf("内存里的代数与文件一致：%d %d", re.Generation, s.Settings().Generation)
	}
}

// 自查 B2：deckd 运行时手改了设置文件，界面保存不盖掉它：拒绝并读入改过的那份，按新代数再存才行。
func TestHandEditIsNotOverwritten(t *testing.T) {
	d := testDirs(t)
	s, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	set := s.Settings()
	set.Projects = []ProjectDef{{ID: "p", Name: "UI", Path: "/tmp/p", Agents: []string{"claude"},
		Models: map[Tier]ModelList{Frontier: {"claude-opus-5-5"}}, Envelope: DefaultEnvelope("p")}}
	if err := s.ReplaceSettingsAt(set, set.Generation); err != nil {
		t.Fatal(err)
	}
	ui := s.Settings()
	pf := filepath.Join(d.Config, "projects.yaml")
	b, _ := os.ReadFile(pf)
	os.WriteFile(pf, []byte(strings.Replace(string(b), "name: UI", "name: HAND", 1)), 0o600) // 照 README 手改，代数不动
	ui.Projects[0].Name = "UI2"
	err = s.ReplaceSettingsAt(ui, ui.Generation)
	var changed *ErrSettingsChanged
	if !errors.As(err, &changed) || !changed.HandEdited || !strings.Contains(err.Error(), "手改") {
		t.Fatalf("手改后界面保存应拒绝：%v", err)
	}
	if got := s.Settings().Projects[0].Name; got != "HAND" {
		t.Fatalf("deckd 应读入手改的那份：%s", got)
	}
	if b, _ := os.ReadFile(pf); !strings.Contains(string(b), "name: HAND") {
		t.Fatal("手改的文件不能被盖掉")
	}
	again := s.Settings()
	again.Projects[0].Name = "UI3"
	if err := s.ReplaceSettingsAt(again, again.Generation); err != nil {
		t.Fatalf("读入后按新代数可以存：%v", err)
	}
	if err := s.ReplaceSettingsAt(again, -1); err != nil {
		t.Fatalf("-1 只给 deckd 内部（不核对）：%v", err)
	}
}

func TestModelIDCheck(t *testing.T) {
	for _, id := range []string{"claude-opus-5-5", "opencode/muse-spark-1.3-contributor-free", "kimi-code/k3", "a:b@c+d"} {
		if !ValidModelID(id) {
			t.Errorf("%q 应合规", id)
		}
	}
	for _, id := range []string{"", "a b", "x;id", "{command}", "$(id)", "a'b", "-m", "a\nb", strings.Repeat("a", 129)} {
		if ValidModelID(id) {
			t.Errorf("%q 应拒绝", id)
		}
	}
	s := multiSettings()
	s.Agents[0].Models = append(s.Agents[0].Models, ModelDef{ID: "x;touch /tmp/p", Tier: Economy})
	if err := s.CheckModelIDs(); err == nil {
		t.Fatal("保存时拒绝不合规的模型 id")
	}
}

// 评审 1 B1：读入手改后，所有拿着旧代数的窗口都不能再存（同一个旧窗口再存、另一个旧窗口存），重新读取后才行；
// 重新读取的窗口存过之后，另一个读到「推过的代数」的窗口也不能覆盖。
func TestHandEditInvalidatesEveryStaleWindow(t *testing.T) {
	d := testDirs(t)
	s, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	set := s.Settings()
	set.Projects = []ProjectDef{{ID: "p", Name: "UI", Path: "/tmp/p", Agents: []string{"claude"},
		Models: map[Tier]ModelList{Frontier: {"claude-opus-5-5"}}, Envelope: DefaultEnvelope("p")}}
	if err := s.ReplaceSettingsAt(set, set.Generation); err != nil {
		t.Fatal(err)
	}
	winA, winB := s.Settings(), s.Settings() // 两个设置窗口，读到同一代
	pf := filepath.Join(d.Config, "projects.yaml")
	b, _ := os.ReadFile(pf)
	os.WriteFile(pf, []byte(strings.Replace(string(b), "name: UI", "name: HAND", 1)), 0o600)
	winA.Projects[0].Name = "A"
	winB.Projects[0].Name = "B"
	for i, w := range []Settings{winA, winA, winB} {
		var changed *ErrSettingsChanged
		if err := s.ReplaceSettingsAt(w, w.Generation); !errors.As(err, &changed) {
			t.Fatalf("第 %d 次旧窗口保存应拒绝：%v", i+1, err)
		}
	}
	if b, _ := os.ReadFile(pf); !strings.Contains(string(b), "name: HAND") {
		t.Fatal("手改不能被任何旧窗口盖掉")
	}
	reA, reB := s.Settings(), s.Settings() // 两个窗口都按 R 重新读取
	reA.Projects[0].Name = "A2"
	if err := s.ReplaceSettingsAt(reA, reA.Generation); err != nil {
		t.Fatalf("重新读取后可以存：%v", err)
	}
	reB.Projects[0].Name = "B2"
	if err := s.ReplaceSettingsAt(reB, reB.Generation); err == nil {
		t.Fatal("A 存过之后，B 拿着同一代再存应拒绝")
	}
	if re, _ := LoadSettings(d.Config); re.Projects[0].Name != "A2" || re.Generation != s.Settings().Generation {
		t.Fatalf("文件是 A2、代数与内存一致：%s %d %d", re.Projects[0].Name, re.Generation, s.Settings().Generation)
	}
}

// attempt 2 自查：代数不倒退。读入手改时前推的代数、deckd 停着时的手改，重启后旧窗口照样对不上；deck project add 不会被按过 R 的窗口盖掉。
func TestGenerationSurvivesRestart(t *testing.T) {
	d := testDirs(t)
	open := func() *Store {
		t.Helper()
		s, err := Open(d)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := open()
	set := s.Settings()
	set.Projects = []ProjectDef{{ID: "p", Name: "UI", Path: "/tmp/p", Agents: []string{"claude"},
		Models: map[Tier]ModelList{Frontier: {"claude-opus-5-5"}}, Envelope: DefaultEnvelope("p")}}
	if err := s.ReplaceSettingsAt(set, set.Generation); err != nil {
		t.Fatal(err)
	}
	stale := s.Settings()
	pf := filepath.Join(d.Config, "projects.yaml")
	edit := func(from, to string) {
		b, _ := os.ReadFile(pf)
		os.WriteFile(pf, []byte(strings.Replace(string(b), from, to, 1)), 0o600)
	}
	// 1) 运行时手改、被读入（代数前推），然后 deckd 重启：按过 R 的窗口与 deck project add 不撞号
	edit("name: UI", "name: HAND")
	s.ReplaceSettingsAt(stale, stale.Generation)
	reread := s.Settings()
	s = open()
	if s.Settings().Generation < reread.Generation {
		t.Fatalf("重启后代数倒退：%d < %d", s.Settings().Generation, reread.Generation)
	}
	add := s.Settings()
	add.Projects = append(add.Projects, ProjectDef{ID: "added", Name: "ADDED", Path: "/tmp/added", Agents: []string{"claude"},
		Models: map[Tier]ModelList{Frontier: {"claude-opus-5-5"}}, Envelope: DefaultEnvelope("added")})
	if err := s.ReplaceSettingsAt(add, add.Generation); err != nil {
		t.Fatalf("deck project add：%v", err)
	}
	for _, w := range []Settings{reread, stale} {
		if err := s.ReplaceSettingsAt(w, w.Generation); err == nil {
			t.Fatalf("重启前的窗口（第 %d 代）不能盖掉 deck project add", w.Generation)
		}
	}
	// 2) deckd 停着时手改（把代数改小也一样），重启后重启前打开的窗口对不上
	win := s.Settings()
	b, _ := os.ReadFile(pf)
	re := strings.NewReplacer(fmt.Sprintf("generation: %d", win.Generation), "generation: 0")
	os.WriteFile(pf, []byte(strings.Replace(re.Replace(string(b)), "name: HAND", "name: OFFLINE", 1)), 0o600)
	af := filepath.Join(d.Config, "agents.yaml")
	ab, _ := os.ReadFile(af)
	os.WriteFile(af, []byte(re.Replace(string(ab))), 0o600)
	s = open()
	win.Projects[0].Name = "STALE"
	if err := s.ReplaceSettingsAt(win, win.Generation); err == nil {
		t.Fatal("deckd 停着时的手改：重启前打开的窗口不能盖掉")
	}
	if got, _ := s.Settings().Project("p"); got.Name != "OFFLINE" {
		t.Fatalf("应是停着时手改的内容：%s", got.Name)
	}
}
