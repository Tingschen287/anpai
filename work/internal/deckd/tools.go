package deckd

import (
	"bufio"
	"net/http"
	"regexp"
	"strings"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// 工具目录（n057）：设置界面「加入本机的 agent」从这里挑。每一项是 Agent Deck 认识、能经 agent-deck 起会话的终端 agent CLI：
// 上游内置的（claude、codex、opencode、gemini），以及在 agent-deck 配置里登记成自定义工具的（grok、antigravity、kimi）。
// binary 与 agent 标记固定在这里，界面不能改（checkAgentEdits）；模型清单只是加入时的初值，加入后可在界面里改。

// CatalogEntry 是工具目录的一项。
type CatalogEntry struct {
	Tool   string               `json:"tool"`   // agent-deck 的工具名
	Name   string               `json:"name"`   // 显示名
	Binary string               `json:"binary"` // 命令
	Agent  bool                 `json:"agent"`  // 不是上游内置工具的自定义 agent（AgentDef.Agent）
	Models []deckstore.ModelDef `json:"models"` // 加入时的默认模型与档位（owner 可改）
	// listArgs：列出该 CLI 可用模型的子命令；空 = 没有（claude、codex 只能手填）。parse 把输出解析成模型 id。
	listArgs []string
	parse    func(string) []string
}

func md(id string, t deckstore.Tier) deckstore.ModelDef { return deckstore.ModelDef{ID: id, Tier: t} }

const (
	fr = deckstore.Frontier
	ba = deckstore.Balanced
	ec = deckstore.Economy
)

// Catalog 是 Agent Deck 认识的工具。默认模型取自 2026-10-15 各 CLI 的 models 输出与 owner 现用的配置。
var Catalog = []CatalogEntry{
	{Tool: "claude", Name: "Claude Code", Binary: "claude",
		Models: []deckstore.ModelDef{md("claude-opus-5-5", fr), md("claude-fable-5-1", fr), md("claude-sonnet-5-5", ba), md("claude-haiku-5-5", ec)}},
	{Tool: "codex", Name: "Codex", Binary: "codex",
		Models: []deckstore.ModelDef{md("gpt-6.1-sol", fr), md("gpt-5.6-terra", ba), md("gpt-5.6-luna", ec)}},
	{Tool: "opencode", Name: "OpenCode", Binary: "opencode",
		Models:   []deckstore.ModelDef{md("opencode/muse-spark-1.3-contributor-free", fr)},
		listArgs: []string{"models"}, parse: parsePlainModels},
	{Tool: "grok", Name: "Grok Build", Binary: "grok", Agent: true,
		Models:   []deckstore.ModelDef{md("grok-4.7", fr), md("grok-4.7-build-fast", ba), md("grok-4.5", ec)},
		listArgs: []string{"models"}, parse: parseBulletModels},
	{Tool: "antigravity", Name: "Antigravity", Binary: "agy", Agent: true,
		Models:   []deckstore.ModelDef{md("claude-opus-4-6-thinking", fr), md("gemini-3.8-flash-high", ba), md("gemini-3.8-flash-low", ec)},
		listArgs: []string{"models"}, parse: parseTabbedModels},
	{Tool: "kimi", Name: "Kimi Code", Binary: "kimi", Agent: true,
		Models: []deckstore.ModelDef{md("kimi-code/k3", ba), md("kimi-for-coding", ec)}},
	{Tool: "gemini", Name: "Gemini CLI", Binary: "gemini"},
}

// CatalogTool 按工具名找目录项。
func CatalogTool(tool string) (CatalogEntry, bool) {
	for _, e := range Catalog {
		if e.Tool == tool {
			return e, true
		}
	}
	return CatalogEntry{}, false
}

var modelIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@+-]{0,127}$`)

func keepModel(out []string, id string) []string {
	if modelIDRe.MatchString(id) {
		return append(out, id)
	}
	return out
}

// parseBulletModels：grok models 的「  * grok-4.7 (default)」「  - grok-4.6」。
func parseBulletModels(s string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if rest, ok := strings.CutPrefix(l, "* "); ok {
			l = rest
		} else if rest, ok := strings.CutPrefix(l, "- "); ok {
			l = rest
		} else {
			continue
		}
		if f := strings.Fields(l); len(f) > 0 {
			out = keepModel(out, f[0])
		}
	}
	return out
}

// parseTabbedModels：agy models 的「gemini-3.8-flash-high<TAB>Gemini 3.8 Flash (High)」。
func parseTabbedModels(s string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		if id, _, ok := strings.Cut(sc.Text(), "\t"); ok {
			out = keepModel(out, strings.TrimSpace(id))
		}
	}
	return out
}

// parsePlainModels：opencode models 每行一个「provider/model」。
func parsePlainModels(s string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if strings.Contains(l, "/") && !strings.ContainsAny(l, " \t") {
			out = keepModel(out, l)
		}
	}
	return out
}

// ToolStatus 是工具目录一项在本机的情况。
type ToolStatus struct {
	CatalogEntry
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	Installed bool   `json:"installed"`
	Error     string `json:"error,omitempty"`
	Added     bool   `json:"added"`      // 已在全局可用清单里（agents.yaml 有用这个工具的 agent）
	CanList   bool   `json:"can_list"`   // 能列出可用模型
}

// getTools 列工具目录与本机情况（owner 界面）：命令在不在 PATH、版本（binary --version，与 Codex 授权核对同一个取法）。
func (s *Server) getTools(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, false, RoleOwner) {
		return
	}
	set := s.store.Settings()
	var info ToolInfoFunc
	if s.agents != nil {
		info = s.agents.toolInfo
	}
	out := make([]ToolStatus, 0, len(Catalog))
	for _, e := range Catalog {
		st := ToolStatus{CatalogEntry: e, CanList: len(e.listArgs) > 0}
		for _, a := range set.Agents {
			if a.Tool == e.Tool {
				st.Added = true
			}
		}
		if info != nil {
			path, ver, err := info(r.Context(), e.Binary)
			st.Path, st.Version, st.Installed = path, deckstore.Clean(ver, 80), path != ""
			if err != nil && path == "" {
				st.Error = "没装（PATH 里找不到 " + e.Binary + "）"
			}
		}
		out = append(out, st)
	}
	writeJSON(w, http.StatusOK, out)
}

// listModels 跑「<binary> models」（只对目录里声明了的工具），20 秒为限（agentdeck.ListModels）。测试可换。
var listModels = agentdeck.ListModels

// getToolModels 列某个工具可选的模型（owner 界面「加模型」时用）。
func (s *Server) getToolModels(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, false, RoleOwner) {
		return
	}
	e, ok := CatalogTool(r.URL.Query().Get("tool"))
	if !ok {
		fail(w, http.StatusNotFound, "不认识的工具 %q", r.URL.Query().Get("tool"))
		return
	}
	if len(e.listArgs) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"models": []string{}, "note": e.Name + " 没有列模型的命令：手填模型 id"})
		return
	}
	text, err := listModels(r.Context(), e.Binary, e.listArgs)
	if err != nil {
		fail(w, http.StatusBadGateway, "%s %s 没有成功：%v（可以手填模型 id）", e.Binary, strings.Join(e.listArgs, " "), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": e.parse(text)})
}
