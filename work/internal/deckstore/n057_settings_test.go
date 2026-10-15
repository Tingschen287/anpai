package deckstore

import (
	"encoding/json"
	"errors"
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
		Agents: []string{"claude", "codex", "grok"},
		Models: map[Tier]ModelList{Frontier: {"claude-opus-5-5", "gpt-6.1-sol", "grok-4.7"}, Balanced: {"claude-sonnet-5-5"}},
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
