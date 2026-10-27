package deckd

import (
	"bufio"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"net/http"
	"strings"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/sessionenv"
)

// 工具目录（n057）：设置界面「加入本机的 agent」从这里挑。每一项是安排 Anpai 认识、能经 agent-deck 起会话的终端 agent CLI：
// 上游内置的（claude、codex、opencode、gemini），以及在 agent-deck 配置里登记成自定义工具、模型经命令传的（grok、antigravity、kimi）。
// kimi（n060 段 4 加入）：Kimi Code 2.3.2 的 `-m, --model <别名>` 仍在，与 grok、antigravity 同一方式——上游配置里要有 [tools.kimi]
// （只写 command 与 icon），模型经 `-c "kimi --model <模型>"` 接在命令末尾（agentdeck.ToolsModelViaCommand），上游 launch 不接受自定义工具的 --model。
// 没有列模型的命令，模型手填。
// binary 与 agent 标记固定在这里，界面不能改（checkAgentEdits）；模型清单只是加入时的初值，加入后可在界面里改。

// CatalogEntry 是工具目录的一项。
type CatalogEntry struct {
	Tool   string               `json:"tool"`   // agent-deck 的工具名
	Name   string               `json:"name"`   // 显示名
	Binary string               `json:"binary"` // 命令
	Agent  bool                 `json:"agent"`  // 不是上游内置工具的自定义 agent（AgentDef.Agent）
	Models []deckstore.ModelDef `json:"models"` // 加入时的默认模型与档位（owner 可改）
	// listArgs：列出该 CLI 可用模型的子命令；空 = 没有（claude、kimi、gemini 只能手填）。parse 把输出解析成模型 id。
	listArgs []string
	parse    func(string) []string
	// parseErr 与 parse 二选一：输出可能不是预期格式、要报错退回手填的（codex debug models 是 JSON）。有它时用它。
	parseErr func(string) ([]string, error)
}

func md(id string, t deckstore.Tier) deckstore.ModelDef { return deckstore.ModelDef{ID: id, Tier: t} }

const (
	fr = deckstore.Frontier
	ba = deckstore.Balanced
	ec = deckstore.Economy
)

// Catalog 是安排 Anpai 认识的工具。默认模型取自 2026-10-15 各 CLI 的 models 输出与 owner 现用的配置。
var Catalog = []CatalogEntry{
	{Tool: "claude", Name: "Claude Code", Binary: "claude",
		Models: []deckstore.ModelDef{md("claude-opus-5-5", fr), md("claude-fable-5-1", fr), md("claude-sonnet-5-5", ba), md("claude-haiku-5-5", ec)}},
	{Tool: "codex", Name: "Codex", Binary: "codex",
		// 默认三档（owner 2026-10-16）：astra 最强、6.1-sol 日常、luna 便宜；候选经 `codex debug models` 自动列（n060 段 4）
		Models:   []deckstore.ModelDef{md("gpt-6-astra", fr), md("gpt-6.1-sol", ba), md("gpt-6-luna", ec)},
		listArgs: []string{"debug", "models"}, parseErr: parseCodexModels},
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

func keepModel(out []string, id string) []string {
	if deckstore.ValidModelID(id) {
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

// codexModelsOut 是 `codex debug models` 输出里用到的字段（其余字段，包括很长的说明文字，不读）。
type codexModelsOut struct {
	Models []struct {
		Slug       string `json:"slug"`
		Visibility string `json:"visibility"`
	} `json:"models"`
}

// maxCodexModels 是一次最多收的候选个数（真实输出十个左右，远超当异常）。
const maxCodexModels = 200

// parseCodexModels：codex debug models 的 JSON，顶层 models[]，取 slug，只要 visibility == "list"（hide 等不列）。
// 不是 JSON、没有 models、一个都没有时报错，设置界面退回手填并给出提示（n060 段 4）。
func parseCodexModels(s string) ([]string, error) {
	var v codexModelsOut
	if err := i18n.UnmarshalExternal([]byte(s), &v); err != nil { // 解码后中和：输出是外部原文
		return nil, i18n.E("err.deckd.codexmodels.badjson", err)
	}
	var out []string
	seen := map[string]bool{}
	for _, m := range v.Models {
		if m.Visibility != "list" || seen[m.Slug] {
			continue
		}
		seen[m.Slug] = true
		out = keepModel(out, m.Slug)
		if len(out) >= maxCodexModels {
			break
		}
	}
	if len(out) == 0 {
		return nil, i18n.E("err.deckd.codexmodels.empty")
	}
	return out, nil
}

// ToolStatus 是工具目录一项在本机的情况。
type ToolStatus struct {
	CatalogEntry
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	Installed bool   `json:"installed"`
	Error     string `json:"error,omitempty"`
	// Code、Args 是 Error 的码与参数（兜底中文 Error 之外，给界面按语言翻译；界面现在不显示 Error，装没装看 Installed）
	Code    string `json:"code,omitempty"`
	Args    []any  `json:"args,omitempty"`
	Added   bool   `json:"added"`    // 已在全局可用清单里（agents.yaml 有用这个工具的 agent）
	CanList bool   `json:"can_list"` // 能列出可用模型
	// PathSource 说明上面的路径与版本是按哪份 PATH 查的：login-shell（会话 PATH）、process（没取到登录 shell 的 PATH，退回 deckd 自己的）。
	// 没有开会话 PATH 解析时为空。回退时 Note、NoteCode、NoteArgs 说明原因（可译），界面提示 owner。
	PathSource string `json:"path_source,omitempty"`
	Note       string `json:"note,omitempty"`
	NoteCode   string `json:"note_code,omitempty"`
	NoteArgs   []any  `json:"note_args,omitempty"`
	// PathRefreshing：有一次读会话 PATH 正在后台跑（启动时那次、或 owner 按「刷新会话 PATH」键触发的），上面的路径与版本是上一份快照的结果。
	// Note* 在两种情形下带原因：启动时读失败、退回 deckd 自己的 PATH（err.deckd.servergettools.nopath）；
	// 或最近一次按键刷新失败、仍用上一份好快照（err.deckd.servergettools.refreshfail）。读成功后消失。
	PathRefreshing bool `json:"path_refreshing,omitempty"`
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
	// 找 CLI 用会话 PATH（与 agent-deck 起会话时用的同一份）：deckd 自己的 PATH 不读 rc 文件，会找不到或找到旧版（n060 段 4）。
	// 这里只读当前快照，不会运行登录 shell（快照启动时读一次，之后 owner 按刷新键才重读）。
	ctx := r.Context()
	scoped := s.cfg.SessionEnv != nil
	var snap sessionenv.Snapshot
	var note map[string]any
	var refreshing bool
	if scoped {
		st := s.cfg.SessionEnv.State(ctx) // 同一次读：快照与「在不在刷新、上次刷新有没有失败」出自同一个时刻
		snap, refreshing = st.Snapshot, st.Refreshing
		ctx = agentdeck.WithSessionPath(ctx, snap.Path)
		if snap.Fallback != nil {
			note = withNote(nil, i18n.E("err.deckd.servergettools.nopath", snap.Fallback), 300)
		}
		if note == nil && st.RefreshErr != nil { // 快照是好的，只是最近一次按键刷新失败了
			note = withNote(nil, i18n.E("err.deckd.servergettools.refreshfail", st.RefreshErr), 300)
		}
	}
	out := make([]ToolStatus, 0, len(Catalog))
	for _, e := range Catalog {
		st := ToolStatus{CatalogEntry: e, CanList: len(e.listArgs) > 0}
		if scoped {
			st.PathSource = string(snap.Source)
			st.PathRefreshing = refreshing
			if note != nil {
				st.Note, _ = note["note"].(string)
				st.NoteCode, _ = note["note_code"].(string)
				st.NoteArgs, _ = note["note_args"].([]any)
			}
		}
		for _, a := range set.Agents {
			if a.Tool == e.Tool {
				st.Added = true
			}
		}
		if info != nil {
			path, ver, err := info(ctx, e.Binary)
			st.Path, st.Version, st.Installed = path, deckstore.Clean(ver, 80), path != ""
			if err != nil && path == "" {
				miss := i18n.E("err.deckd.servergettools.1", e.Binary)
				st.Error = miss.Error()
				st.Code, st.Args, _ = i18n.Wire(miss)
			}
		}
		out = append(out, st)
	}
	writeJSON(w, http.StatusOK, out)
}

// listModels 跑目录里声明的列模型命令（grok models、codex debug models 等；只对目录里声明了的工具），20 秒为限；
// 生产里是 scopedListModels：用会话 PATH 上解出的可执行文件跑，与设置里显示的、Codex trust 门记下的是同一个（n060 段 4）。测试可换。
var listModels = scopedListModels

// getToolModels 列某个工具可选的模型（owner 界面「加模型」时用）。
func (s *Server) getToolModels(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, false, RoleOwner) {
		return
	}
	e, ok := CatalogTool(r.URL.Query().Get("tool"))
	if !ok {
		writeErr(w, http.StatusNotFound, i18n.E("err.deckd.servergettoolmodels.1", r.URL.Query().Get("tool")))
		return
	}
	if len(e.listArgs) == 0 {
		writeJSON(w, http.StatusOK, withNote(map[string]any{"models": []string{}}, i18n.E("err.deckd.servergettoolmodels.3", e.Name), 300))
		return
	}
	// 用会话 PATH 找到的那个可执行文件跑（与设置里显示的、Codex trust 门记下的是同一个，n060 段 4）
	ctx, _, _ := s.sessionScope(r.Context())
	text, err := listModels(ctx, e.Binary, e.listArgs)
	if err != nil {
		writeErr(w, http.StatusBadGateway, i18n.E("err.deckd.servergettoolmodels.2", e.Binary, strings.Join(e.listArgs, " "), err))
		return
	}
	if e.parseErr != nil {
		models, perr := e.parseErr(text)
		if perr != nil {
			writeErr(w, http.StatusBadGateway, i18n.E("err.deckd.servergettoolmodels.2", e.Binary, strings.Join(e.listArgs, " "), perr))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"models": models})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": e.parse(text)})
}
