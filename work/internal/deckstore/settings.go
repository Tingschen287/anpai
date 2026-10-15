package deckstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

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
	// Agent：不是 agent-deck 内置工具的自定义 agent（例如 grok、kimi）要显式声明 agent: true，
	// 才确认它会把任务说明当任务读，而不是当命令执行（agentdeck.CheckTakesTaskText，n051）。
	Agent bool `yaml:"agent,omitempty" json:"agent,omitempty"`
	// ExtraArgs：owner 逐字声明、允许出现在这个 CLI 进程参数里的额外参数组（n051 attempt 3，owner 选 R1-A：启动后按参数白名单核对，
	// 白名单 = 上游按启动请求会加的参数 + 这里声明的）。例如 [["--allowedTools", "Read,Write"]]。明确的绕过参数声明不进来（agentdeck.CheckExtraArgs）。
	ExtraArgs [][]string `yaml:"extra_args,omitempty" json:"extra_args,omitempty"`
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
	Agents    []string           `yaml:"agents" json:"agents"`
	Models    map[Tier]ModelList `yaml:"models" json:"models"` // 三档各一个有顺序的模型清单（n057）
	MainAgent MainAgent          `yaml:"main_agent" json:"main_agent"`
	Envelope  Envelope           `yaml:"envelope" json:"envelope"`
}

// ModelList 是项目某一档的模型清单，排前的优先（n057，owner：「排第一的优先，额度用尽就换下一个」）。
// 读旧格式时一个字符串当作只有一个模型的清单；写出时总是列表。
type ModelList []string

func (l *ModelList) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var s string
		if err := n.Decode(&s); err != nil {
			return err
		}
		*l = nil
		if s != "" {
			*l = ModelList{s}
		}
		return nil
	}
	var xs []string
	if err := n.Decode(&xs); err != nil {
		return err
	}
	*l = xs
	return nil
}

func (l *ModelList) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*l = nil
		if s != "" {
			*l = ModelList{s}
		}
		return nil
	}
	var xs []string
	if err := json.Unmarshal(b, &xs); err != nil {
		return err
	}
	*l = xs
	return nil
}

// First 是清单里的第一个模型（不设时为空）。
func (l ModelList) First() string {
	if len(l) == 0 {
		return ""
	}
	return l[0]
}

// Has 说明清单里有没有这个模型。
func (l ModelList) Has(id string) bool {
	for _, x := range l {
		if x == id {
			return true
		}
	}
	return false
}

// Settings 是全部 agent 设置。
type Settings struct {
	Agents   []AgentDef   `yaml:"agents" json:"agents"`
	Projects []ProjectDef `yaml:"projects" json:"projects"`
	// Generation 是两份设置文件当前的代数（n057）：整份保存时要带上读到的代数，对不上就拒绝（别处在这期间改过）。
	Generation int `yaml:"-" json:"generation"`
}

// DefaultEnvelope 是 owner 批准的推荐默认值（O5）。
func DefaultEnvelope(projectID string) Envelope {
	return Envelope{ID: projectID + "-default", Version: 1, MaxSubagents: 3, MaxFrontier: 0, TokenSoftCap: 2_000_000}
}

// DefaultAgents 是首次运行时生成的全局清单。档位初值取 1.design/model-routing.md，owner 可改。
func DefaultAgents() []AgentDef {
	return []AgentDef{
		{ID: "claude", Name: "Claude Code", Tool: "claude", Binary: "claude", Models: []ModelDef{
			{"claude-opus-5-5", Frontier}, {"claude-fable-5-1", Frontier}, {"claude-sonnet-5-5", Balanced}, {"claude-haiku-5-5", Economy}}},
		{ID: "codex", Name: "Codex", Tool: "codex", Binary: "codex", Models: []ModelDef{
			{"gpt-6.1-sol", Frontier}, {"gpt-5.6-terra", Balanced}, {"gpt-5.6-luna", Economy}}},
		{ID: "grok", Name: "Grok Build", Tool: "grok", Binary: "grok", Agent: true, Models: []ModelDef{
			{"grok-4.7", Frontier}, {"grok-4.7-build-fast", Balanced}, {"grok-4.5", Economy}}},
		{ID: "antigravity", Name: "Antigravity", Tool: "antigravity", Binary: "agy", Agent: true, Models: []ModelDef{
			{"claude-opus-4-6-thinking", Frontier}, {"gemini-3.8-flash-high", Balanced}, {"gemini-3.8-flash-low", Economy}}},
		{ID: "opencode", Name: "OpenCode", Tool: "opencode", Binary: "opencode", Models: []ModelDef{
			{"opencode/muse-spark-1.3-contributor-free", Frontier}}},
		{ID: "kimi", Name: "Kimi Code", Tool: "kimi", Binary: "kimi", Agent: true, Models: []ModelDef{
			{"kimi-code/k3", Balanced}, {"kimi-for-coding", Economy}}},
	}
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidID 说明一个 agent 或项目 id 合不合规（小写字母数字、-、_，不超过 64）。
func ValidID(s string) bool { return idRe.MatchString(s) }

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
		if err := CheckPath(p.Path); err != nil {
			return fmt.Errorf("项目 %s 的 path %w", p.ID, err)
		}
		selected := map[string]bool{}
		for _, id := range p.Agents {
			if _, ok := agents[id]; !ok {
				return fmt.Errorf("项目 %s 选用了不存在的 agent %s", p.ID, id)
			}
			selected[id] = true
		}
		for tier, models := range p.Models {
			if !validTier(tier) {
				return fmt.Errorf("项目 %s 的档位 %q 不合规", p.ID, tier)
			}
			seen := map[string]bool{}
			for _, model := range models {
				if seen[model] {
					return fmt.Errorf("项目 %s 的 %s 档里模型 %s 重复", p.ID, tier, model)
				}
				seen[model] = true
				owner, mtier, ok := s.findModel(model, selected)
				if !ok {
					return fmt.Errorf("项目 %s 的 %s 档模型 %s 不属于本项目选用的 agent", p.ID, tier, model)
				}
				if mtier != tier {
					return fmt.Errorf("项目 %s 把 %s 的 %s（全局登记为 %s 档）放进了 %s 档", p.ID, owner, model, mtier, tier)
				}
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
	Agent AgentDef `json:"agent"`
	Model string   `json:"model"`
	Tier  Tier     `json:"tier"`
}

// ResolveTier 把「本项目某一档」换成具体的 agent 与模型。
// 不看额度：取这一档清单里排第一个。
func (s Settings) ResolveTier(projectID string, tier Tier) (Resolved, error) {
	return s.ResolveTierFor(projectID, tier, "", nil)
}

// ResolveTierFor 按这一档清单的顺序挑模型（n057）：agentID 不为空时只看这个 agent 的模型（主 agent、点了 agent 的派工）；
// exhausted 说某个 agent 的额度已用尽时跳过它的模型，取下一个。额度未知不算用尽（由调用方的 exhausted 决定）。
// 全部被跳过时报错并写明跳过了哪些，不派。
func (s Settings) ResolveTierFor(projectID string, tier Tier, agentID string, exhausted func(AgentDef) bool) (Resolved, error) {
	p, ok := s.Project(projectID)
	if !ok {
		return Resolved{}, fmt.Errorf("没有项目 %s", projectID)
	}
	return s.pick(p, tier, agentID, exhausted)
}

func (s Settings) pick(p ProjectDef, tier Tier, agentID string, exhausted func(AgentDef) bool) (Resolved, error) {
	models := p.Models[tier]
	if len(models) == 0 {
		return Resolved{}, fmt.Errorf("项目 %s 没有为 %s 档指定模型", p.ID, tier)
	}
	var skipped []string
	known := map[string]bool{}
	for _, model := range models {
		res, err := s.ResolveModel(p.ID, model)
		if err != nil {
			return Resolved{}, err
		}
		if agentID != "" && res.Agent.ID != agentID {
			continue
		}
		if exhausted != nil {
			ex, ok := known[res.Agent.ID]
			if !ok {
				ex = exhausted(res.Agent)
				known[res.Agent.ID] = ex
			}
			if ex {
				skipped = append(skipped, model)
				continue
			}
		}
		return res, nil
	}
	if len(skipped) == 0 {
		return Resolved{}, fmt.Errorf("项目 %s 的 %s 档清单里没有 %s 的模型", p.ID, tier, agentID)
	}
	first, err := s.ResolveModel(p.ID, skipped[0])
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{}, &ErrAllExhausted{Project: p.ID, Tier: tier, Skipped: skipped, First: first}
}

// ErrAllExhausted：这一档清单里能挑的模型，看板都说额度已用尽。First 是排第一的那个（调用方决定要不要照用：
// frontier 子 agent 交给额度门上报 owner、主 agent 由 owner 亲自起照用；其余不派）。
type ErrAllExhausted struct {
	Project string
	Tier    Tier
	Skipped []string
	First   Resolved
}

func (e *ErrAllExhausted) Error() string {
	return fmt.Sprintf("项目 %s 的 %s 档清单里能用的模型额度都已用尽（%s）：不派；等额度恢复，或在设置里给这一档加别家的模型",
		e.Project, e.Tier, strings.Join(e.Skipped, "、"))
}

// CheckMainAgents：设了主 agent 的项目，主 agent 那一档清单里得有它自己的模型（n057）。
// 只在保存新设置时查（deckd），不放进 Validate：读取时不因此挡住已有的设置。
func (s Settings) CheckMainAgents() error {
	for _, p := range s.Projects {
		if p.MainAgent.Agent == "" {
			continue
		}
		if _, err := s.pick(p, p.MainAgent.Tier, p.MainAgent.Agent, nil); err != nil {
			return fmt.Errorf("项目 %s 的主 agent %s · %s：%w", p.ID, p.MainAgent.Agent, p.MainAgent.Tier, err)
		}
	}
	return nil
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
// 两份设置文件的代际标记（n049，承接 n050 评审建议）：每次保存两份文件都写同一个 generation（上一代 +1），
// 并先把上一代留成 .prev。保存在两次写之间失败，会留下一新一旧两份：读取时代数不一致就拒绝，
// 不会把「各自合法、组合起来新旧混合」的配置当成有效配置用上。手改时保持两份的 generation 一样即可。

type agentsFile struct {
	Generation int        `yaml:"generation"`
	Agents     []AgentDef `yaml:"agents"`
}

type projectsFile struct {
	Generation int          `yaml:"generation"`
	Projects   []ProjectDef `yaml:"projects"`
}

// ErrMixedSettings 表示两份设置文件来自不同的保存（上一次保存只写成一半）。
type ErrMixedSettings struct {
	Dir                string
	AgentsGen, ProjGen int
	ProjMissing        bool // projects.yaml 不存在（第一次保存只写成了 agents.yaml）
}

func (e *ErrMixedSettings) Error() string {
	proj := fmt.Sprintf("projects.yaml（第 %d 代）", e.ProjGen)
	if e.ProjMissing {
		proj = "projects.yaml（不存在，按第 0 代）"
	}
	return fmt.Sprintf("%s 下的 agents.yaml（第 %d 代）与 %s来自不同的保存，上一次保存只写成了一半；"+
		"不用这份新旧混合的配置。恢复：把代数较新的那份换回它的 .prev（上一代；没有 .prev 说明它是第一次写，删掉即回到默认），"+
		"或核对内容后把两份的 generation 改成一样（缺的那份补上）",
		e.Dir, e.AgentsGen, proj)
}

// readSettings 只读两份文件（不存在的按空的第 0 代处理），返回内容与各自的代数。
// agents.yaml 总是先写：它比 projects.yaml 新（包括 projects.yaml 还不存在）就是保存只写成了一半。
func readSettings(cfgDir string) (agentsFile, projectsFile, bool, error) {
	var a agentsFile
	var p projectsFile
	data, err := os.ReadFile(filepath.Join(cfgDir, "agents.yaml"))
	haveAgents := err == nil
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return a, p, false, err
	default:
		if err := yaml.Unmarshal(data, &a); err != nil {
			return a, p, false, fmt.Errorf("agents.yaml 解析不了：%w", err)
		}
	}
	data, err = os.ReadFile(filepath.Join(cfgDir, "projects.yaml"))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return a, p, false, err
	default:
		if err := yaml.Unmarshal(data, &p); err != nil {
			return a, p, false, fmt.Errorf("projects.yaml 解析不了：%w", err)
		}
	}
	if haveAgents && a.Generation != p.Generation {
		return a, p, false, &ErrMixedSettings{Dir: cfgDir, AgentsGen: a.Generation, ProjGen: p.Generation,
			ProjMissing: errors.Is(err, os.ErrNotExist)}
	}
	return a, p, haveAgents, nil
}

func LoadSettings(cfgDir string) (Settings, error) {
	a, p, haveAgents, err := readSettings(cfgDir)
	if err != nil {
		return Settings{}, err
	}
	s := Settings{Agents: a.Agents, Projects: p.Projects, Generation: p.Generation}
	if !haveAgents {
		s.Agents = DefaultAgents()
		if err := saveYAML(filepath.Join(cfgDir, "agents.yaml"), agentsFile{Generation: p.Generation, Agents: s.Agents}); err != nil {
			return Settings{}, err
		}
	}
	sort.SliceStable(s.Projects, func(i, j int) bool { return s.Projects[i].ID < s.Projects[j].ID })
	if err := s.Validate(); err != nil {
		return Settings{}, fmt.Errorf("%s 下的设置不合规：%w（改好后再启动；可先运行 deck settings -check 自查）", cfgDir, err)
	}
	return s, nil
}

// CheckSettings 只读检查设置文件，不写任何东西（升级前自查用：deck settings -check）。
func CheckSettings(cfgDir string) error {
	a, p, haveAgents, err := readSettings(cfgDir)
	if err != nil {
		return err
	}
	s := Settings{Agents: a.Agents, Projects: p.Projects}
	if !haveAgents {
		s.Agents = DefaultAgents()
	}
	return s.Validate()
}

// SaveSettings 写回两份文件（只由 deckd 调）。写之前先校验；两份写同一个新代数，并先留上一代为 .prev。
func SaveSettings(cfgDir string, s Settings) error {
	_, err := saveSettings(cfgDir, s)
	return err
}

// saveSettings 同 SaveSettings，并返回写下的代数。
func saveSettings(cfgDir string, s Settings) (int, error) {
	if err := s.Validate(); err != nil {
		return 0, err
	}
	a, _, _, err := readSettings(cfgDir)
	if err != nil {
		var mixed *ErrMixedSettings
		if !errors.As(err, &mixed) {
			return 0, err
		}
		a.Generation = max(mixed.AgentsGen, mixed.ProjGen)
	}
	gen := a.Generation + 1
	for _, name := range []string{"agents.yaml", "projects.yaml"} {
		if data, err := os.ReadFile(filepath.Join(cfgDir, name)); err == nil {
			if err := writeAtomic(filepath.Join(cfgDir, name+".prev"), data); err != nil {
				return 0, err
			}
		}
	}
	if err := saveFaultBefore("agents.yaml"); err != nil {
		return 0, err
	}
	if err := saveYAML(filepath.Join(cfgDir, "agents.yaml"), agentsFile{Generation: gen, Agents: s.Agents}); err != nil {
		return 0, err
	}
	if err := saveFaultBefore("projects.yaml"); err != nil {
		return 0, err
	}
	return gen, saveYAML(filepath.Join(cfgDir, "projects.yaml"), projectsFile{Generation: gen, Projects: s.Projects})
}

// ErrSettingsChanged 表示整份保存时带的代数不是当前的：读到之后别处（另一个设置窗口、deck project add、手改）又保存过。
type ErrSettingsChanged struct{ Want, Have int }

func (e *ErrSettingsChanged) Error() string {
	return fmt.Sprintf("设置在你打开之后被别处改过（你读到的是第 %d 代，现在是第 %d 代）：没有保存；重新打开设置再改", e.Want, e.Have)
}

// saveFault 是故障注入点：只有测试会设置它，模拟「写到一半失败」。
var saveFault func(name string) error

func saveFaultBefore(name string) error {
	if saveFault != nil {
		return saveFault(name)
	}
	return nil
}

func saveYAML(path string, v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	return writeAtomic(path, data)
}
