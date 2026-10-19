package deckstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

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
	ID        string             `yaml:"id" json:"id"`
	Name      string             `yaml:"name" json:"name"`
	Path      string             `yaml:"path" json:"path"`
	GraphRoot string             `yaml:"graph_root,omitempty" json:"graph_root,omitempty"`
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

var modelIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@+-]{0,127}$`)

// ValidModelID 说明一个模型 id 能不能用（n057 自查）：只许字母数字与 . _ / : @ + -，不超过 128。
// 模型 id 会进 CLI 的命令行：上游给 gemini、opencode 拼命令时不加引号，自定义工具的 {command} 会被上游当占位符替换。
func ValidModelID(id string) bool { return modelIDRe.MatchString(id) }

// CheckModelIDs：全局清单里的模型 id 都合规（保存时查；派工时 deckd 对选中的模型再查一次，手改的也拦得住）。
func (s Settings) CheckModelIDs() error {
	for _, a := range s.Agents {
		for _, m := range a.Models {
			if !ValidModelID(m.ID) {
				return i18n.E("err.deckstore.settingscheckmodelids.1", a.ID, m.ID)
			}
		}
	}
	return nil
}

// ValidID 说明一个 agent 或项目 id 合不合规（小写字母数字、-、_，不超过 64）。
func ValidID(s string) bool { return idRe.MatchString(s) }

// Validate 检查设置自洽。任何一条不过，整份设置不用（不猜）。
func (s Settings) Validate() error {
	agents := map[string]AgentDef{}
	for _, a := range s.Agents {
		if !idRe.MatchString(a.ID) {
			return i18n.E("err.deckstore.settingsvalidate.1", a.ID)
		}
		if _, dup := agents[a.ID]; dup {
			return i18n.E("err.deckstore.settingsvalidate.2", a.ID)
		}
		if a.Tool == "" {
			return i18n.E("err.deckstore.settingsvalidate.3", a.ID)
		}
		seen := map[string]bool{}
		for _, m := range a.Models {
			if m.ID == "" || !validTier(m.Tier) {
				return i18n.E("err.deckstore.settingsvalidate.4", a.ID, m.ID, m.Tier)
			}
			if seen[m.ID] {
				return i18n.E("err.deckstore.settingsvalidate.5", a.ID, m.ID)
			}
			seen[m.ID] = true
		}
		agents[a.ID] = a
	}
	projects := map[string]bool{}
	for _, p := range s.Projects {
		if !idRe.MatchString(p.ID) {
			return i18n.E("err.deckstore.settingsvalidate.6", p.ID)
		}
		if projects[p.ID] {
			return i18n.E("err.deckstore.settingsvalidate.7", p.ID)
		}
		projects[p.ID] = true
		// 项目目录会原样进批准范围（scope），长度上限与 scope 的值一致
		if err := CheckPath(p.Path); err != nil {
			return i18n.E("err.deckstore.settingsvalidate.8", p.ID, err)
		}
		selected := map[string]bool{}
		for _, id := range p.Agents {
			if _, ok := agents[id]; !ok {
				return i18n.E("err.deckstore.settingsvalidate.9", p.ID, id)
			}
			selected[id] = true
		}
		for tier, models := range p.Models {
			if !validTier(tier) {
				return i18n.E("err.deckstore.settingsvalidate.10", p.ID, tier)
			}
			seen := map[string]bool{}
			for _, model := range models {
				if seen[model] {
					return i18n.E("err.deckstore.settingsvalidate.11", p.ID, tier, model)
				}
				seen[model] = true
				owner, mtier, ok := s.findModel(model, selected)
				if !ok {
					return i18n.E("err.deckstore.settingsvalidate.12", p.ID, tier, model)
				}
				if mtier != tier {
					return i18n.E("err.deckstore.settingsvalidate.13", p.ID, owner, model, mtier, tier)
				}
			}
		}
		if p.MainAgent.Agent != "" {
			if !selected[p.MainAgent.Agent] {
				return i18n.E("err.deckstore.settingsvalidate.14", p.ID, p.MainAgent.Agent)
			}
			if !validTier(p.MainAgent.Tier) {
				return i18n.E("err.deckstore.settingsvalidate.15", p.ID, p.MainAgent.Tier)
			}
		}
		e := p.Envelope
		if e.ID == "" || e.Version < 1 || e.MaxSubagents < 0 || e.MaxFrontier < 0 || e.TokenSoftCap < 0 {
			return i18n.E("err.deckstore.settingsvalidate.16", p.ID)
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
		return Resolved{}, i18n.E("err.deckstore.storecreatedecision.2", projectID)
	}
	return s.pick(p, tier, agentID, exhausted)
}

func (s Settings) pick(p ProjectDef, tier Tier, agentID string, exhausted func(AgentDef) bool) (Resolved, error) {
	models := p.Models[tier]
	if len(models) == 0 {
		return Resolved{}, i18n.E("err.deckstore.settingspick.1", p.ID, tier)
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
		return Resolved{}, i18n.E("err.deckstore.settingspick.2", p.ID, tier, agentID)
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

func (e *ErrAllExhausted) Error() string { return e.I18n().Error() }

func (e *ErrAllExhausted) I18n() *i18n.Error {
	return i18n.E("err.deckstore.errallexhausted", e.Project, e.Tier, strings.Join(e.Skipped, i18n.MustTextZH("sep.list")))
}

// CheckMainAgents：设了主 agent 的项目，主 agent 那一档清单里得有它自己的模型（n057）。
// 只在保存新设置时查（deckd），不放进 Validate：读取时不因此挡住已有的设置。
func (s Settings) CheckMainAgents() error {
	for _, p := range s.Projects {
		if p.MainAgent.Agent == "" {
			continue
		}
		if _, err := s.pick(p, p.MainAgent.Tier, p.MainAgent.Agent, nil); err != nil {
			return i18n.E("err.deckstore.settingscheckmainagents.1", p.ID, p.MainAgent.Agent, p.MainAgent.Tier, err)
		}
	}
	return nil
}

// ResolveModel 检查模型在本项目选用范围内，并给出它的 agent 与档位。不在范围内就是错误（调用方硬拒绝）。
func (s Settings) ResolveModel(projectID, model string) (Resolved, error) {
	p, ok := s.Project(projectID)
	if !ok {
		return Resolved{}, i18n.E("err.deckstore.storecreatedecision.2", projectID)
	}
	selected := map[string]bool{}
	for _, id := range p.Agents {
		selected[id] = true
	}
	agentID, tier, ok := s.findModel(model, selected)
	if !ok {
		return Resolved{}, i18n.E("err.deckstore.settingsresolvemodel.1", model, projectID)
	}
	a, _ := s.Agent(agentID)
	return Resolved{Agent: a, Model: model, Tier: tier}, nil
}

// LoadSettings 从配置目录读 agents.yaml 与 projects.yaml。agents.yaml 不存在时用 DefaultAgents 并写出来，
// 方便 owner 在设置界面或文件里改；projects.yaml 不存在就是没有项目。
// 两份设置文件的代际标记（n049，承接 n050 评审建议）：每次保存两份文件都写同一个 generation（比上一代大，按时间走，见 nextGen），
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

func (e *ErrMixedSettings) Error() string { return e.I18n().Error() }

func (e *ErrMixedSettings) projErr() *i18n.Error {
	if e.ProjMissing {
		return i18n.E("err.deckstore.errmixed.missing")
	}
	return i18n.E("err.deckstore.errmixed.proj", e.ProjGen)
}

func (e *ErrMixedSettings) I18n() *i18n.Error {
	return i18n.E("err.deckstore.errmixed.body", e.Dir, e.AgentsGen, e.projErr())
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
			return a, p, false, i18n.E("err.deckstore.readsettings.1", err)
		}
	}
	data, err = os.ReadFile(filepath.Join(cfgDir, "projects.yaml"))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return a, p, false, err
	default:
		if err := yaml.Unmarshal(data, &p); err != nil {
			return a, p, false, i18n.E("err.deckstore.readsettings.2", err)
		}
	}
	// 代数范围（n057 attempt 3 自查）：手改写了负数或很大的数（加一会溢出成负数，之后每次保存都被拒），
	// 明确报错、deckd 不启动，免得悄悄存不了
	for _, g := range []struct {
		name string
		gen  int
	}{{"agents.yaml", a.Generation}, {"projects.yaml", p.Generation}} {
		if g.gen < 0 || g.gen > maxGeneration {
			return a, p, false, i18n.E("err.deckstore.readsettings.3", g.name, g.gen, maxGeneration)
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
		return Settings{}, i18n.E("err.deckstore.loadsettings.1", cfgDir, err)
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
	// 新代数比文件里的、调用方手上的（deckd 内存里的，读入手改后可能更大）都大：拿着旧代数的窗口一律对不上（n057 评审 1 B1）；
	// 也不小于当前时间（rev 127，nextGen）
	gen := nextGen(max(a.Generation, s.Generation))
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

// ErrSettingsChanged 表示整份保存时带的代数不是当前的：读到之后别处（另一个设置窗口、deck project add）又保存过；
// HandEdited：两份文件在 deckd 之外被改过（手改），deckd 已读入改过的那份。
type ErrSettingsChanged struct {
	Want, Have int
	HandEdited bool
}

func (e *ErrSettingsChanged) Error() string { return e.I18n().Error() }

func (e *ErrSettingsChanged) I18n() *i18n.Error {
	// 不写代数：按时间前推后是十几位的数（n057 rev 127），对 owner 没有意义
	if e.HandEdited {
		return i18n.E("err.deckstore.errsettings.hand")
	}
	return i18n.E("err.deckstore.errsettings.else")
}

// settingsFilesHash 是两份设置文件当前内容的指纹（不存在的按空）。
func settingsFilesHash(cfgDir string) string {
	h := sha256.New()
	for _, name := range []string{"agents.yaml", "projects.yaml"} {
		b, _ := os.ReadFile(filepath.Join(cfgDir, name))
		fmt.Fprintf(h, "%d:", len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
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

// 设置代数按时间走（n057 rev 127，owner 选定）：deckd 打开设置、保存、读入手改时都用 nextGen 取新代数。
// 不记状态文件，没有可损坏、可丢失的东西（attempt 2 的 settings-generation，评审 2 R1）。系统时钟回拨不在此列。
//
// genClock 是当前时间（微秒；十六位数，JSON 里按浮点读也不失真）。测试可以换掉它。
var genClock = func() int { return int(time.Now().UnixMicro()) }

// maxGeneration 是文件里代数的上限（2^53：JSON 按浮点读也不失真，离 int 溢出很远；微秒时钟要到约 2255 年才到）。
const maxGeneration = 1 << 53

// nextGen 返回比 floor 大、也不小于当前时间的新代数，并等时钟走到它再返回（最多等 2 毫秒）：
// 发出去的代数都不超过发出时的时钟，deckd 重启时取的「当前时间 +1」一定比重启前发出的都大，与重启多快无关
// （评审 2 探针：同一毫秒里读入手改又重启，按毫秒取会撞号）。floor 远在未来（手改写了比现在还大的代数，或写入后时钟往回拨）时
// 不等，照样 +1，这时重启不再保证旧窗口作废（README 写明，与时钟回拨同列）。
func nextGen(floor int) int {
	g := max(floor, genClock()) + 1
	for deadline := time.Now().Add(2 * time.Millisecond); g > genClock() && time.Now().Before(deadline); {
		time.Sleep(time.Microsecond)
	}
	return g
}
