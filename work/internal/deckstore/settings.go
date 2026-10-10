package deckstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"
)

// agent 设置（owner 在 n048 验收时要求，架构稿 §14.2）：
//   - 全局可用清单 agents.yaml：本机能用的 agent、它对应的 agent-deck 工具名、可用模型及每个模型的档位；
//   - 项目选用 projects.yaml：每个项目勾选用全局里的哪些 agent，再给三档各指定一个模型。
// 主 agent 开子 agent 时只说要哪一档，deckd 按项目设置换成具体模型；不在本项目选用范围内的模型硬拒绝。
// 用 YAML 而不是架构稿里写的 TOML：仓库已依赖 yaml.v3，不为一份配置新增依赖。

// Tier 是模型档位。
type Tier string

const (
	Economy  Tier = "economy"
	Balanced Tier = "balanced"
	Frontier Tier = "frontier"
)

// Tiers 是三档，由低到高。
var Tiers = []Tier{Economy, Balanced, Frontier}

func validTier(t Tier) bool { return t == Economy || t == Balanced || t == Frontier }

// ModelDef 是一个可用模型。
type ModelDef struct {
	ID   string `yaml:"id" json:"id"`
	Tier Tier   `yaml:"tier" json:"tier"`
}

// AgentDef 是全局清单里的一个 agent。
type AgentDef struct {
	ID     string     `yaml:"id" json:"id"`         // 例如 claude
	Name   string     `yaml:"name" json:"name"`     // 例如 Claude Code
	Tool   string     `yaml:"tool" json:"tool"`     // agent-deck 的工具名
	Binary string     `yaml:"binary" json:"binary"` // 用来检查是否已安装、取版本
	Models []ModelDef `yaml:"models" json:"models"`
}

// Envelope 是预算包络（架构稿 §5、O5）。
type Envelope struct {
	ID           string `yaml:"id" json:"id"`
	Version      int    `yaml:"version" json:"version"`
	MaxSubagents int    `yaml:"max_subagents" json:"max_subagents"`
	MaxFrontier  int    `yaml:"max_frontier" json:"max_frontier"`
	TokenSoftCap int64  `yaml:"token_soft_cap" json:"token_soft_cap"` // 只用于提醒与上报，不是硬性成本保证
}

// MainAgent 是项目主 agent 用哪个 agent、哪一档。
type MainAgent struct {
	Agent string `yaml:"agent" json:"agent"`
	Tier  Tier   `yaml:"tier" json:"tier"`
}

// ProjectDef 是一个项目的选用。
type ProjectDef struct {
	ID        string          `yaml:"id" json:"id"`
	Name      string          `yaml:"name" json:"name"`
	Path      string          `yaml:"path" json:"path"`
	GraphRoot string          `yaml:"graph_root,omitempty" json:"graph_root,omitempty"`
	Agents    []string        `yaml:"agents" json:"agents"`
	Models    map[Tier]string `yaml:"models" json:"models"` // 三档各一个模型
	MainAgent MainAgent       `yaml:"main_agent" json:"main_agent"`
	Envelope  Envelope        `yaml:"envelope" json:"envelope"`
}

// Settings 是全部 agent 设置。
type Settings struct {
	Agents   []AgentDef   `yaml:"agents" json:"agents"`
	Projects []ProjectDef `yaml:"projects" json:"projects"`
}

// DefaultEnvelope 是 owner 批准的推荐默认值（O5）。
func DefaultEnvelope(projectID string) Envelope {
	return Envelope{ID: projectID + "-default", Version: 1, MaxSubagents: 3, MaxFrontier: 0, TokenSoftCap: 2_000_000}
}

// DefaultAgents 是首次运行时生成的全局清单。档位初值取 1.design/model-routing.md，owner 可改。
func DefaultAgents() []AgentDef {
	return []AgentDef{
		{ID: "claude", Name: "Claude Code", Tool: "claude", Binary: "claude", Models: []ModelDef{
			{"claude-opus-5-5", Frontier}, {"claude-fable-5-1", Frontier}, {"claude-sonnet-5", Balanced}, {"claude-haiku-4-5", Economy}}},
		{ID: "codex", Name: "Codex", Tool: "codex", Binary: "codex", Models: []ModelDef{
			{"gpt-6.1-sol", Frontier}, {"gpt-5.6-terra", Balanced}, {"gpt-5.6-luna", Economy}}},
		{ID: "grok", Name: "Grok Build", Tool: "grok", Binary: "grok", Models: []ModelDef{
			{"grok-4.6", Balanced}, {"grok-4.5", Economy}}},
		{ID: "opencode", Name: "OpenCode", Tool: "opencode", Binary: "opencode", Models: []ModelDef{
			{"opencode/muse-spark-1.3-contributor-free", Frontier}}},
		{ID: "kimi", Name: "Kimi Code", Tool: "kimi", Binary: "kimi", Models: []ModelDef{
			{"kimi-code/k3", Balanced}, {"kimi-for-coding", Economy}}},
	}
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Validate 检查设置自洽。任何一条不过，整份设置不用（不猜）。
func (s Settings) Validate() error {
	agents := map[string]AgentDef{}
	for _, a := range s.Agents {
		if !idRe.MatchString(a.ID) {
			return fmt.Errorf("agent id %q 不合规（小写字母数字、-、_）", a.ID)
		}
		if _, dup := agents[a.ID]; dup {
			return fmt.Errorf("agent %s 重复", a.ID)
		}
		if a.Tool == "" {
			return fmt.Errorf("agent %s 没写 tool", a.ID)
		}
		seen := map[string]bool{}
		for _, m := range a.Models {
			if m.ID == "" || !validTier(m.Tier) {
				return fmt.Errorf("agent %s 的模型 %q 档位 %q 不合规", a.ID, m.ID, m.Tier)
			}
			if seen[m.ID] {
				return fmt.Errorf("agent %s 的模型 %s 重复", a.ID, m.ID)
			}
			seen[m.ID] = true
		}
		agents[a.ID] = a
	}
	projects := map[string]bool{}
	for _, p := range s.Projects {
		if !idRe.MatchString(p.ID) {
			return fmt.Errorf("项目 id %q 不合规", p.ID)
		}
		if projects[p.ID] {
			return fmt.Errorf("项目 %s 重复", p.ID)
		}
		projects[p.ID] = true
		// 项目目录会原样进批准范围（scope），长度上限与 scope 的值一致
		if p.Path == "" || !filepath.IsAbs(p.Path) || len(p.Path) > maxScopeValue || Clean(p.Path, 0) != p.Path {
			return fmt.Errorf("项目 %s 的 path 必须是绝对路径，最长 %d 字节、不含控制字符", p.ID, maxScopeValue)
		}
		selected := map[string]bool{}
		for _, id := range p.Agents {
			if _, ok := agents[id]; !ok {
				return fmt.Errorf("项目 %s 选用了不存在的 agent %s", p.ID, id)
			}
			selected[id] = true
		}
		for tier, model := range p.Models {
			if !validTier(tier) {
				return fmt.Errorf("项目 %s 的档位 %q 不合规", p.ID, tier)
			}
			owner, mtier, ok := s.findModel(model, selected)
			if !ok {
				return fmt.Errorf("项目 %s 的 %s 档模型 %s 不属于本项目选用的 agent", p.ID, tier, model)
			}
			if mtier != tier {
				return fmt.Errorf("项目 %s 把 %s 的 %s（全局登记为 %s 档）放进了 %s 档", p.ID, owner, model, mtier, tier)
			}
		}
		if p.MainAgent.Agent != "" {
			if !selected[p.MainAgent.Agent] {
				return fmt.Errorf("项目 %s 的主 agent %s 不在本项目选用范围内", p.ID, p.MainAgent.Agent)
			}
			if !validTier(p.MainAgent.Tier) {
				return fmt.Errorf("项目 %s 的主 agent 档位 %q 不合规", p.ID, p.MainAgent.Tier)
			}
		}
		e := p.Envelope
		if e.ID == "" || e.Version < 1 || e.MaxSubagents < 0 || e.MaxFrontier < 0 || e.TokenSoftCap < 0 {
			return fmt.Errorf("项目 %s 的预算包络不完整", p.ID)
		}
	}
	return nil
}

// findModel 在选用的 agent 里找模型，返回所属 agent 与登记档位。
func (s Settings) findModel(model string, selected map[string]bool) (agent string, tier Tier, ok bool) {
	for _, a := range s.Agents {
		if selected != nil && !selected[a.ID] {
			continue
		}
		for _, m := range a.Models {
			if m.ID == model {
				return a.ID, m.Tier, true
			}
		}
	}
	return "", "", false
}

// Project 按 id 找项目。
func (s Settings) Project(id string) (ProjectDef, bool) {
	for _, p := range s.Projects {
		if p.ID == id {
			return p, true
		}
	}
	return ProjectDef{}, false
}

// Agent 按 id 找 agent。
func (s Settings) Agent(id string) (AgentDef, bool) {
	for _, a := range s.Agents {
		if a.ID == id {
			return a, true
		}
	}
	return AgentDef{}, false
}

// Resolved 是一次档位解析的结果。
type Resolved struct {
	Agent AgentDef
	Model string
	Tier  Tier
}

// ResolveTier 把「本项目某一档」换成具体的 agent 与模型。
func (s Settings) ResolveTier(projectID string, tier Tier) (Resolved, error) {
	p, ok := s.Project(projectID)
	if !ok {
		return Resolved{}, fmt.Errorf("没有项目 %s", projectID)
	}
	model, ok := p.Models[tier]
	if !ok || model == "" {
		return Resolved{}, fmt.Errorf("项目 %s 没有为 %s 档指定模型", projectID, tier)
	}
	return s.ResolveModel(projectID, model)
}

// ResolveModel 检查模型在本项目选用范围内，并给出它的 agent 与档位。不在范围内就是错误（调用方硬拒绝）。
func (s Settings) ResolveModel(projectID, model string) (Resolved, error) {
	p, ok := s.Project(projectID)
	if !ok {
		return Resolved{}, fmt.Errorf("没有项目 %s", projectID)
	}
	selected := map[string]bool{}
	for _, id := range p.Agents {
		selected[id] = true
	}
	agentID, tier, ok := s.findModel(model, selected)
	if !ok {
		return Resolved{}, fmt.Errorf("模型 %s 不在项目 %s 选用的 agent 里", model, projectID)
	}
	a, _ := s.Agent(agentID)
	return Resolved{Agent: a, Model: model, Tier: tier}, nil
}

// LoadSettings 从配置目录读 agents.yaml 与 projects.yaml。agents.yaml 不存在时用 DefaultAgents 并写出来，
// 方便 owner 在设置界面或文件里改；projects.yaml 不存在就是没有项目。
func LoadSettings(cfgDir string) (Settings, error) {
	var s Settings
	agentsPath := filepath.Join(cfgDir, "agents.yaml")
	data, err := os.ReadFile(agentsPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		s.Agents = DefaultAgents()
		if err := saveYAML(agentsPath, struct {
			Agents []AgentDef `yaml:"agents"`
		}{s.Agents}); err != nil {
			return Settings{}, err
		}
	case err != nil:
		return Settings{}, err
	default:
		var f struct {
			Agents []AgentDef `yaml:"agents"`
		}
		if err := yaml.Unmarshal(data, &f); err != nil {
			return Settings{}, fmt.Errorf("agents.yaml 解析不了：%w", err)
		}
		s.Agents = f.Agents
	}
	data, err = os.ReadFile(filepath.Join(cfgDir, "projects.yaml"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Settings{}, err
	}
	if err == nil {
		var f struct {
			Projects []ProjectDef `yaml:"projects"`
		}
		if err := yaml.Unmarshal(data, &f); err != nil {
			return Settings{}, fmt.Errorf("projects.yaml 解析不了：%w", err)
		}
		s.Projects = f.Projects
	}
	sort.SliceStable(s.Projects, func(i, j int) bool { return s.Projects[i].ID < s.Projects[j].ID })
	return s, s.Validate()
}

// SaveSettings 写回两份文件（只由 deckd 调）。写之前先校验。
func SaveSettings(cfgDir string, s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if err := saveYAML(filepath.Join(cfgDir, "agents.yaml"), struct {
		Agents []AgentDef `yaml:"agents"`
	}{s.Agents}); err != nil {
		return err
	}
	return saveYAML(filepath.Join(cfgDir, "projects.yaml"), struct {
		Projects []ProjectDef `yaml:"projects"`
	}{s.Projects})
}

func saveYAML(path string, v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	return writeAtomic(path, data)
}
