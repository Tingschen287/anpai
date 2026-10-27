package deckstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// n060 段 4：首次运行的默认清单里 Codex 改成 astra / 6.1-sol / 6-luna（owner 2026-10-16）。已有的设置文件一个字节都不改。

// 段 3 之前（cf8a8bc）首次运行写出的 Codex 清单原文。
const oldCodexBlock = `  - id: codex
    name: Codex
    tool: codex
    binary: codex
    models:
      - id: gpt-6.1-sol
        tier: frontier
      - id: gpt-5.6-terra
        tier: balanced
      - id: gpt-5.6-luna
        tier: economy
`

const oldAgentsYAML = `generation: 7
agents:
  - id: claude
    name: Claude Code
    tool: claude
    binary: claude
    models:
      - id: claude-opus-5-5
        tier: frontier
      - id: claude-sonnet-5-5
        tier: balanced
` + oldCodexBlock

const oldProjectsYAML = `generation: 7
projects:
  - id: ai
    name: ai
    path: /Users/o/ai
    agents: [claude, codex]
    models:
      frontier: [claude-opus-5-5, gpt-6.1-sol]
      balanced: [gpt-5.6-terra]
      economy: [gpt-5.6-luna]
    main_agent: {agent: codex, tier: frontier}
    envelope: {id: ai-default, version: 1, max_subagents: 3, max_frontier: 0, token_soft_cap: 2000000}
`

func codexDefaults(t *testing.T, as []AgentDef) []ModelDef {
	t.Helper()
	for _, a := range as {
		if a.ID == "codex" {
			return a.Models
		}
	}
	t.Fatal("没有 codex")
	return nil
}

// 新装：默认清单里 Codex 是 astra（最强）、6.1-sol（日常）、6-luna（便宜），没有 gpt-5.6-terra；写出的文件也是这三个。
func TestN060S4DefaultAgentsHaveTheNewCodexTiers(t *testing.T) {
	want := []ModelDef{{"gpt-6-astra", Frontier}, {"gpt-6.1-sol", Balanced}, {"gpt-6-luna", Economy}}
	got := codexDefaults(t, DefaultAgents())
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("Codex 默认三档：%+v", got)
	}
	d := testDirs(t)
	s, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	if g := codexDefaults(t, s.Settings().Agents); len(g) != 3 || g[0] != want[0] || g[1] != want[1] || g[2] != want[2] {
		t.Fatalf("首次运行生成的清单：%+v", g)
	}
	b, _ := os.ReadFile(filepath.Join(d.Config, "agents.yaml"))
	if !strings.Contains(string(b), "gpt-6-astra") || !strings.Contains(string(b), "gpt-6-luna") || strings.Contains(string(b), "gpt-5.6-terra") || strings.Contains(string(b), "gpt-5.6-luna") {
		t.Fatalf("写出的 agents.yaml：\n%s", b)
	}
	// Kimi 的默认档位本来就在清单里：k3 日常、for-coding 便宜
	for _, a := range DefaultAgents() {
		if a.ID == "kimi" && (len(a.Models) != 2 || a.Models[0] != (ModelDef{"kimi-code/k3", Balanced}) || a.Models[1] != (ModelDef{"kimi-for-coding", Economy}) || !a.Agent || a.Binary != "kimi") {
			t.Fatalf("Kimi 默认：%+v", a)
		}
	}
}

// 已有的设置文件不改：旧的 agents.yaml 带着 gpt-5.6-terra，各个读取、打开、检查的入口之后字节与修改时间都不变，读出来仍是旧清单。
func TestN060S4ExistingSettingsFilesAreNotChanged(t *testing.T) {
	d := testDirs(t)
	os.MkdirAll(d.Config, 0o700)
	ap, pp := filepath.Join(d.Config, "agents.yaml"), filepath.Join(d.Config, "projects.yaml")
	os.WriteFile(ap, []byte(oldAgentsYAML), 0o600)
	os.WriteFile(pp, []byte(oldProjectsYAML), 0o600)
	old := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	os.Chtimes(ap, old, old)
	os.Chtimes(pp, old, old)
	check := func(what string) {
		t.Helper()
		for p, want := range map[string]string{ap: oldAgentsYAML, pp: oldProjectsYAML} {
			b, _ := os.ReadFile(p)
			st, _ := os.Stat(p)
			if string(b) != want || !st.ModTime().Equal(old) {
				t.Fatalf("%s 之后 %s 变了：\n%s", what, filepath.Base(p), b)
			}
		}
	}
	for what, load := range map[string]func() ([]AgentDef, error){
		"LoadSettings":     func() ([]AgentDef, error) { s, err := LoadSettings(d.Config); return s.Agents, err },
		"LoadSettingsFor":  func() ([]AgentDef, error) { s, err := LoadSettingsFor(d.Config, "en"); return s.Agents, err },
		"Open":             func() ([]AgentDef, error) { s, err := Open(d); return agentsOf(s), err },
		"OpenWithLang(en)": func() ([]AgentDef, error) { s, err := OpenWithLang(d, "en"); return agentsOf(s), err },
	} {
		as, err := load()
		if err != nil {
			t.Fatalf("%s：%v", what, err)
		}
		cx := codexDefaults(t, as)
		if len(cx) != 3 || cx[0].ID != "gpt-6.1-sol" || cx[0].Tier != Frontier || cx[1].ID != "gpt-5.6-terra" || cx[2].ID != "gpt-5.6-luna" {
			t.Fatalf("%s：读出来应是文件里的旧清单：%+v", what, cx)
		}
		check(what)
	}
	if err := CheckSettings(d.Config); err != nil {
		t.Fatal(err)
	}
	check("CheckSettings")
	if ResolveUILang(d.Config, func(string) string { return "en_US.UTF-8" }) != "zh" {
		t.Fatal("老用户仍是中文")
	}
	check("ResolveUILang")
	// 老项目仍按旧清单解析：balanced 是 gpt-5.6-terra
	s, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := s.Settings().ResolveTier("ai", Balanced); err != nil || r.Model != "gpt-5.6-terra" || r.Agent.ID != "codex" {
		t.Fatalf("老项目的 balanced 仍是 gpt-5.6-terra：%+v %v", r, err)
	}
	check("ResolveTier")
}

func agentsOf(s *Store) []AgentDef {
	if s == nil {
		return nil
	}
	return s.Settings().Agents
}

// 只有 agents.yaml 不存在才用默认清单；agents.yaml 在、projects.yaml 不在，也不动已有的 agents.yaml。
func TestN060S4OnlyAMissingAgentsFileGetsTheNewDefaults(t *testing.T) {
	d := testDirs(t)
	os.MkdirAll(d.Config, 0o700)
	ap := filepath.Join(d.Config, "agents.yaml")
	gen0 := strings.Replace(oldAgentsYAML, "generation: 7", "generation: 0", 1) // 没有 projects.yaml 时代数按 0
	os.WriteFile(ap, []byte(gen0), 0o600)
	if _, err := Open(d); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(ap); string(b) != gen0 {
		t.Fatalf("只有 agents.yaml、没有 projects.yaml：agents.yaml 不能被默认清单覆盖：\n%s", b)
	}
}
