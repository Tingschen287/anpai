package deckd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/dashquota"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/lease"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

// n051：主 agent 开子 agent、看子 agent 的输出、给子 agent 发话；owner 起项目的主 agent（架构稿 §5、§14.3）。
//
// 开子 agent 的准入（POST /v1/agents），先做全部硬拒绝，再过两道可审批的门，最后启动：
//  1. 角色：本项目主 agent，或 owner 界面；
//  2. 硬拒绝（owner 也批不过）：工具与模型解析不出（档位或模型不在项目设置里）、不是 agent、绕过权限的参数、
//     工作目录不合规（规整的绝对路径、≤300 字节）、任务与启动参数的 sha256 对不上、请求 id 用过；
//  3. Codex trust 门：会改写 ~/.codex/config.toml 的工具，没有记住的授权就生成 codex_trust 决定（202），
//     owner 批准即记住（§14.3），之后同一项目、工具、目录、可执行文件路径与版本不再问；
//  4. 预算包络：在锁内检查并预留名额；超出就生成 cost 决定（202），批准后主 agent 带同一请求与决定 id 再来一次，
//     这一次 BeginExecute → Reserve 消费例外；
//  5. 启动，按实际命令行核对没有绕过参数；Graph 通道此时才 projectctl start 拿到 attempt（开不成就停掉刚起的会话），
//     普通通道不写 node/attempt；登记写入权、送第一条 brief、通知。
//
// 与架构稿 §5 的顺序不同：绕过参数等硬拒绝提前到两道门之前，免得先生成一个批了也没用的决定。

// Upstream 是 deckd 对 agent-deck 的调用；生产用 *agentdeck.Client，测试换成假的。
type Upstream interface {
	Launch(ctx context.Context, req agentdeck.LaunchRequest) (agentdeck.LaunchResult, error)
	ListSessions(ctx context.Context) ([]agentdeck.ListedSession, error)
	SendMessage(ctx context.Context, p lease.Permit, sessionID, message string) error
	SendToMain(ctx context.Context, p lease.Permit, sessionID, message string) error
	SendIfIdle(ctx context.Context, p lease.Permit, sessionID, message string) error
	PaneOutputPlain(ctx context.Context, sessionID string) (string, error)
	StopSession(ctx context.Context, p lease.Permit, sessionID string) error
}

// BypassChecks 是上游侧的绕过检查（n051 真模型端到端实测：agent-deck v1.15 默认给 Claude 加 --dangerously-skip-permissions）。
// Config 在启动前看上游配置（agentdeck.CheckUpstreamConfigNoBypass）；Processes 在启动后看 tmux 会话里实际在跑的命令行
// （agentdeck.CheckSessionArgs：CLI 进程的参数按白名单查，n051 attempt 3 R1-A）。没接上就不开（确认不了）。
type BypassChecks struct {
	Config    func(tool string) error
	Processes func(ctx context.Context, tmuxSession string, want agentdeck.LaunchArgs) error
	// Alive 看 tmux 会话还在不在（agentdeck.TmuxSessionAlive）。起主 agent 时用它认出已经退出的旧主 agent；没接上就不认。
	Alive func(ctx context.Context, tmuxSession string) (bool, error)
	// NotCodex 说自定义工具在上游配置里登记过、确定不是 Codex 兼容的（agentdeck.CustomToolNotCodex，n057）：
	// 这时 Codex trust 门不必问 owner。没接上就按「可能」处理（照旧问）。
	NotCodex func(tool string) bool
}

// GraphStartFunc 为 Graph 通道开一轮（projectctl start），返回账本给的 attempt。
type GraphStartFunc func(ctx context.Context, p deckstore.ProjectDef, node, actor, model string, route deckstore.Tier) (int, error)

// ToolInfoFunc 取一个 CLI 的可执行文件路径与版本（agentdeck.ToolInfo）。
type ToolInfoFunc func(ctx context.Context, binary string) (path, version string, err error)

// agentsDeps 是开子 agent 需要的外部依赖。都为 nil 时 /v1/agents 等接口返回 503。
type agentsDeps struct {
	upstream   Upstream
	leases     *lease.Store
	toolInfo   ToolInfoFunc
	graphStart GraphStartFunc
	bypass     BypassChecks
	// graphState 查 Graph 节点此刻的状态（projectctl brief，只读）；空则不看账本、不送账本提醒
	graphState func(ctx context.Context, p deckstore.ProjectDef, node string) (projectctl.State, error)
	// quotaOf 取某个工具的额度状态（dashquota 的 available/warn/exhausted/unknown）；空时用 Server 的看板客户端，测试可换
	quotaOf func(ctx context.Context, tool string) string
	// startup 是送第一条 brief 前看画面的节奏（n055）；零值用 defaultStartupTiming，测试换成很短的
	startup startupTiming
}

// WithAgents 接上开子 agent 需要的依赖（daemon 与测试调用）。
func (s *Server) WithAgents(up Upstream, leases *lease.Store, toolInfo ToolInfoFunc, graphStart GraphStartFunc) {
	s.agents = &agentsDeps{upstream: up, leases: leases, toolInfo: toolInfo, graphStart: graphStart}
}

// WithBypassChecks 接上上游侧的绕过检查（daemon 与测试调用；不接就不开任何 agent）。
func (s *Server) WithBypassChecks(b BypassChecks) { s.agents.bypass = b }

func (s *Server) notCodex(tool string) bool {
	return s.agents != nil && s.agents.bypass.NotCodex != nil && s.agents.bypass.NotCodex(tool)
}

// upstreamConfigNoBypass：上游配置会不会给这个工具加跳过权限的参数。
func (s *Server) upstreamConfigNoBypass(tool string) error {
	if s.agents.bypass.Config == nil {
		return i18n.E("err.deckd.serverupstreamconfignobypass.1")
	}
	return s.agents.bypass.Config(tool)
}

// maxTaskBytes 是一次开子 agent 的任务正文上限。
const maxTaskBytes = 64 << 10

// SpawnArgsSHA256 是启动参数的指纹：agent、档位、模型、标题、工作目录、Graph 节点按固定顺序编码后取 sha256。
// deck agent spawn 在客户端算，deckd 按同样的规则重算比对（README 的预留合同，n050 评审 4 建议）。
func SpawnArgsSHA256(agent, tier, model, title, dir, graphNode string) string {
	b, _ := json.Marshal([]string{"deck-spawn-v1", agent, tier, model, title, dir, graphNode})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// TaskSHA256 是任务正文的指纹。
func TaskSHA256(task string) string {
	h := sha256.Sum256([]byte(task))
	return hex.EncodeToString(h[:])
}

type spawnBody struct {
	Project    string `json:"project"` // owner 开子 agent 时必填；主 agent 不填（取自己的项目）
	Agent      string `json:"agent"`   // 设置里的 agent id；可空（按档位或模型解析）
	Tier       string `json:"tier"`
	Model      string `json:"model"`
	Title      string `json:"title"`
	Task       string `json:"task"`
	Dir        string `json:"dir"` // 工作目录；空 = 项目目录
	RequestID  string `json:"request_id"`
	TaskSHA256 string `json:"task_sha256"`
	ArgsSHA256 string `json:"args_sha256"`
	GraphNode  string `json:"graph_node"` // 走 Graph 通道时的节点；空 = 普通通道
	Exception  string `json:"exception"`  // 已批准的 cost 决定 id（超额例外，只用一次）
}

// spawnPlan 是通过硬拒绝之后的启动计划。
type spawnPlan struct {
	project deckstore.ProjectDef
	agent   deckstore.AgentDef
	x       deckstore.Session
	brief   string
}

// hardErr 是硬拒绝：直接 400，不生成决定。
type hardErr struct{ error }

func (s *Server) postAgents(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, true, RoleMain, RoleOwner) {
		return
	}
	if s.agents == nil {
		writeErr(w, http.StatusServiceUnavailable, i18n.E("err.deckd.serverpostagents.1"))
		return
	}
	if err := s.stopped(); err != nil {
		fail(w, http.StatusServiceUnavailable, "%v", err)
		return
	}
	var b spawnBody
	if err := readBody(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, i18n.E("err.deckd.serverpostagents.2", err))
		return
	}
	project := b.Project
	if c.Role == RoleMain {
		if project != "" && project != c.Project {
			writeErr(w, http.StatusForbidden, i18n.E("err.deckd.serverpostagents.3", c.Project))
			return
		}
		project = c.Project
	}
	plan, err := s.planSpawn(r.Context(), c, project, b)
	if err != nil {
		fail(w, http.StatusBadRequest, "%v", err)
		return
	}
	s.runSpawn(w, detached(r), c, plan, b.Exception, b.GraphNode)
}

// detached：启动与送 brief、给子 agent 发话不随调用方断开而中止（n051 端到端实测：真实 Claude Code 起来加送 brief 超过
// 客户端 30 秒，客户端一断，请求的 context 取消，正在进行的 session send 被杀，会话留下却没有 brief）。
// 每次调用 agent-deck 仍有自己的上限（agentdeck.Client.Timeout）。
func detached(r *http.Request) context.Context { return context.WithoutCancel(r.Context()) }

// planSpawn 解析 agent 与模型，做全部硬拒绝。
func (s *Server) planSpawn(ctx context.Context, c Caller, project string, b spawnBody) (spawnPlan, error) {
	var plan spawnPlan
	set := s.store.Settings()
	p, ok := set.Project(project)
	if !ok {
		return plan, i18n.E("err.deckd.serverplanspawn.1", project)
	}
	var res deckstore.Resolved
	var err error
	switch {
	case b.Model != "" && b.Tier != "":
		return plan, i18n.E("err.deckd.serverplanspawn.2")
	case b.Model != "":
		res, err = set.ResolveModel(project, b.Model)
	default:
		res, err = s.resolveTier(ctx, set, project, deckstore.Tier(b.Tier), b.Agent)
	}
	if err != nil {
		return plan, err
	}
	if b.Agent != "" && b.Agent != res.Agent.ID {
		return plan, i18n.E("err.deckd.serverplanspawn.3", res.Model, res.Agent.ID, b.Agent)
	}
	a := res.Agent
	if !deckstore.ValidModelID(res.Model) {
		return plan, i18n.E("err.deckd.serverplanspawn.4", res.Model)
	}
	if err := agentdeck.CheckNoBypass(a.Tool, a.Binary, res.Model, b.Title); err != nil {
		return plan, err
	}
	if err := agentdeck.CheckExtraArgs(a.Tool, a.ExtraArgs); err != nil {
		return plan, err
	}
	if err := s.upstreamConfigNoBypass(a.Tool); err != nil {
		return plan, err
	}
	if err := agentdeck.CheckToolModel(a.Tool, false); err != nil {
		return plan, err
	}
	if err := agentdeck.CheckTakesTaskText(a.Tool, a.Agent); err != nil {
		return plan, err
	}
	if strings.TrimSpace(b.Task) == "" {
		return plan, i18n.E("err.deckd.serverplanspawn.5")
	}
	if len(b.Task) > maxTaskBytes {
		return plan, i18n.E("err.deckd.serverplanspawn.6", maxTaskBytes)
	}
	dir := b.Dir
	if dir == "" {
		dir = p.Path
	}
	if err := deckstore.CheckPath(dir); err != nil {
		return plan, i18n.E("err.deckd.serverplanspawn.7", err)
	}
	if b.TaskSHA256 != TaskSHA256(b.Task) {
		return plan, i18n.E("err.deckd.serverplanspawn.8")
	}
	if want := SpawnArgsSHA256(res.Agent.ID, string(res.Tier), res.Model, b.Title, dir, b.GraphNode); b.ArgsSHA256 != want {
		return plan, i18n.E("err.deckd.serverplanspawn.9")
	}
	x := deckstore.Session{Project: project, Role: "sub", Parent: c.Session, Agent: res.Agent.ID, Tool: a.Tool, Model: res.Model,
		Tier: res.Tier, Title: b.Title, CreatedBy: creatorOf(c), RequestID: b.RequestID, Dir: dir,
		TaskSHA256: b.TaskSHA256, ArgsSHA256: b.ArgsSHA256}
	if b.GraphNode != "" {
		if p.GraphRoot == "" {
			return plan, i18n.E("err.deckd.serverplanspawn.10", project)
		}
		x.Graph = &deckstore.GraphLink{Node: b.GraphNode}
	}
	return spawnPlan{project: p, agent: a, x: x, brief: subBrief(p, x, b.Task)}, nil
}

func creatorOf(c Caller) string {
	if c.Role == RoleOwner {
		return "owner"
	}
	return c.Session
}

// runSpawn 过两道门、预留、启动。plan.x.Role 决定是开子 agent 还是主 agent。
func (s *Server) runSpawn(w http.ResponseWriter, ctx context.Context, c Caller, plan spawnPlan, exception, graphNode string) {
	x, p := plan.x, plan.project
	// Codex trust 门（§14.3）
	if trust := agentdeck.CheckCodexTrust(x.Tool); trust != agentdeck.CodexTrustNo && !(trust == agentdeck.CodexTrustMaybe && s.notCodex(x.Tool)) {
		path, version, err := s.agents.toolInfo(ctx, plan.agent.Binary)
		if err != nil {
			writeErr(w, http.StatusBadRequest, i18n.E("err.deckd.serverrunspawn.1", plan.agent.Binary, err))
			return
		}
		if !s.store.TrustValid(x.Project, x.Tool, p.Path, x.Dir, path, version) {
			d, err := s.store.CreateDecision(deckstore.NewDecision{Project: x.Project, Kind: deckstore.KindCodexTrust,
				SummaryErr: i18n.E("err.deckd.serverrunspawn.2", plan.agent.Name, x.Dir),
				Scope: map[string]string{"action": "codex_trust", "project": x.Project, "tool": x.Tool, "project_dir": p.Path, "dir": x.Dir,
					"tool_path": path, "tool_version": version, "request_id": x.RequestID},
				Recommend: deckstore.OptApprove, CreatedBy: creatorOf(c)})
			if err != nil {
				writeErr(w, http.StatusConflict, i18n.E("err.deckd.serverrunspawn.3", err))
				return
			}
			writeJSON(w, http.StatusAccepted, map[string]any{"pending": deckstore.KindCodexTrust, "decision": d,
				"next": i18n.E("err.deckd.serverrunspawn.28").Error()})
			return
		}
		s.store.NotifyCoded(x.Project, "spawn", i18n.E("err.deckd.serverrunspawn.4", plan.agent.Name), "deckd")
	}
	// 主 agent：每个项目同时只能有一个。旧主 agent 的 tmux 会话已经不在（退出或被关掉）就记为已结束、放行
	// （n051 自查：此前没有任何路径结束主 agent，它一退出，这个项目就再也起不了主 agent）。
	if x.Role == "main" {
		for _, y := range s.store.Sessions(x.Project) {
			if y.Role == "main" && (y.Status == deckstore.StatusLaunching || y.Status == deckstore.StatusRunning || y.Status == deckstore.StatusUncertain) {
				if s.mainGone(ctx, y) {
					s.store.SetSessionStatus(y.Key, deckstore.StatusStopped, i18n.E("err.deckd.serverrunspawn.29", y.TmuxSession).Error())
					s.store.NotifyCoded(x.Project, "spawn", i18n.E("err.deckd.serverrunspawn.5", y.TitleArg()), "deckd")
					continue
				}
				writeErr(w, http.StatusConflict, i18n.E("err.deckd.serverrunspawn.6", x.Project, y.Key, y.Status))
				return
			}
		}
	}
	// 额度未知（看板取不到、数据过期、没有来源）或已用尽时启动 frontier 子 agent：一律上报（架构稿 §5、O5；n051 评审 1 B2）。
	// 批准绑定本次请求（SpawnScope），主 agent 带同一请求与决定 id 再来一次即放行。主 agent 由 owner 自己起，不在此列。
	quotaGate := x.Role == "sub" && x.Tier == deckstore.Frontier && s.quotaUnsure(ctx, x.Tool)
	if quotaGate && exception == "" {
		// 同一个批准随后也会被预留当作超额例外用掉：包络也超了就在同一条决定里写明，owner 一次看清两个原因
		summary := i18n.E("err.deckd.serverrunspawn.7", plan.agent.Name, x.Tier, x.Model, x.TitleArg())
		if u := s.store.Slots(x.Project); u.Subagents >= p.Envelope.MaxSubagents || u.Frontier >= p.Envelope.MaxFrontier {
			summary = i18n.E("err.deckd.serverrunspawn.8", u.Subagents, p.Envelope.MaxSubagents, u.Frontier, p.Envelope.MaxFrontier, plan.agent.Name, x.Tier, x.Model, x.TitleArg())
		}
		d, derr := s.store.CreateDecision(deckstore.NewDecision{Project: x.Project, Kind: deckstore.KindCost,
			SummaryErr: summary,
			Scope:      deckstore.SpawnScope(x, p), Recommend: deckstore.OptReject, CreatedBy: creatorOf(c)})
		if derr != nil {
			writeErr(w, http.StatusConflict, i18n.E("err.deckd.serverrunspawn.9", derr))
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"pending": deckstore.KindCost, "decision": d,
			"next": i18n.E("err.deckd.serverrunspawn.30", d.ID).Error()})
		return
	}
	// 预算包络：预留名额；超出生成 cost 决定
	if exception != "" {
		if _, err := s.store.BeginExecute(exception, deckstore.SpawnScope(x, p)); err != nil {
			writeErr(w, http.StatusConflict, i18n.E("err.deckd.serverrunspawn.10", err))
			return
		}
	}
	res, err := s.store.Reserve(x, exception)
	var over *deckstore.ErrOverEnvelope
	if errors.As(err, &over) {
		whyOver := any(over.Reason)
		if over.Why != nil {
			whyOver = over.Why
		}
		d, derr := s.store.CreateDecision(deckstore.NewDecision{Project: x.Project, Kind: deckstore.KindCost,
			SummaryErr: i18n.E("err.deckd.serverrunspawn.11", whyOver, plan.agent.Name, x.Tier, x.Model, x.TitleArg()),
			Scope:      deckstore.SpawnScope(x, p),
			Recommend:  deckstore.OptReject, CreatedBy: creatorOf(c)})
		if derr != nil {
			writeErr(w, http.StatusConflict, i18n.E("err.deckd.serverrunspawn.12", err, derr))
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"pending": deckstore.KindCost, "decision": d,
			"next": i18n.E("err.deckd.serverrunspawn.30", d.ID).Error()})
		return
	}
	if err != nil {
		if exception != "" {
			s.store.FinishExecute(exception, deckstore.StateFailed, i18n.E("err.deckd.serverrunspawn.31", err).Error())
		}
		writeErr(w, http.StatusConflict, i18n.E("err.deckd.serverrunspawn.13", err))
		return
	}
	key := res.Key
	if exception != "" {
		// 批准后名额又够了：Reserve 没消耗例外，它会一直停在 executing（n051 自查）。收尾为已执行、作废，不能再用
		if d, ok := s.store.Decision(exception); ok && d.State == deckstore.StateExecuting {
			why := i18n.E("err.deckd.serverrunspawn.32").Error()
			if quotaGate {
				why = i18n.E("err.deckd.serverrunspawn.33").Error()
			}
			s.store.FinishExecute(exception, deckstore.StateApplied, why)
		}
	}
	if graphNode != "" && s.agents.graphStart == nil {
		s.store.LaunchFailed(key, false, i18n.E("err.deckd.serverrunspawn.34").Error())
		writeErr(w, http.StatusServiceUnavailable, i18n.E("err.deckd.serverrunspawn.14"))
		return
	}
	// 启动
	lr := agentdeck.LaunchRequest{ProjectPath: x.Dir, Title: i18n.PresentZH(x.Title), // 给上游命令行的标题：中性标记换成中文记号
		Group: x.Project, Tool: x.Tool, Model: x.Model, NoWait: true,
		Extra: plan.agent.ExtraArgs} // owner 声明的额外参数随启动传上去（n055）；启动后照旧按白名单核对实际命令行
	out, lerr := s.agents.upstream.Launch(ctx, lr)
	id, uncertain, why := launchOutcome(out, lerr)
	if id == "" {
		s.store.LaunchFailed(key, uncertain, textOf(why))
		code := http.StatusBadGateway
		failErr := i18n.E("err.deckd.serverrunspawn.35", why)
		wcode, wargs, _ := i18n.Wire(failErr)
		writeJSON(w, code, map[string]any{"error": failErr.Error(), "code": wcode, "args": wargs, "session": key, "uncertain": uncertain})
		return
	}
	// 模型经命令传的自定义工具（grok、antigravity）：上游没登记这个工具时会把它认成别的工具起（认不出就是 shell，n057），
	// 那样起的不是 owner 选的 agent，停掉、不送任务
	if agentdeck.ToolsModelViaCommand[x.Tool] != "" && out.Tool != x.Tool {
		why := i18n.E("err.deckd.serverrunspawn.15", x.Tool, out.Tool, x.Tool)
		s.stopLaunched(ctx, key, id, x, i18n.E("err.deckd.serverrunspawn.36", why), false, i18n.E("err.deckd.serverrunspawn.37"))
		writeErr(w, http.StatusBadGateway, i18n.E("err.deckd.serverrunspawn.16", id, why))
		return
	}
	if err := agentdeck.CheckLaunchedTakesTaskText(out); err != nil {
		s.store.LaunchFailed(key, true, i18n.E("err.deckd.serverrunspawn.38", err).Error())
		writeErr(w, http.StatusBadGateway, i18n.E("err.deckd.serverrunspawn.17", err, id))
		return
	}
	tmux := ""
	if list, err := s.agents.upstream.ListSessions(ctx); err == nil {
		tmux, _ = agentdeck.TmuxSessionOf(list, id)
	}
	// 启动后按实际在跑的命令行再核一次：上游自己加了绕过参数（或核对不了）就停掉会话、不送 brief
	if err := s.processesNoBypass(ctx, tmux, plan.agent); err != nil {
		s.stopLaunched(ctx, key, id, x, i18n.E("err.deckd.serverrunspawn.36", err), false, i18n.E("err.deckd.serverrunspawn.39"))
		writeErr(w, http.StatusBadGateway, i18n.E("err.deckd.serverrunspawn.18", id, err))
		return
	}
	// Graph 通道：会话起来并核过之后才开一轮（n051 自查：先开一轮再启动，启动失败时账本里会留下没人干活的 attempt）。
	// 开不成就停掉刚起的会话；projectctl 结果不明（可能已经开了一轮）时继续占名额，请 owner 核对账本。
	if graphNode != "" {
		attempt, err := s.agents.graphStart(ctx, p, graphNode, "deck:"+key, x.Model, x.Tier)
		if err != nil {
			var notStarted *projectctl.ErrStartNotStarted
			unknown := !errors.As(err, &notStarted)
			what := i18n.E("err.deckd.serverrunspawn.40", graphNode)
			if unknown {
				what = i18n.E("err.deckd.serverrunspawn.41", graphNode)
			}
			s.stopLaunched(ctx, key, id, x, i18n.E("err.deckd.serverrunspawn.projectctl", err), unknown, what)
			writeErr(w, http.StatusConflict, i18n.E("err.deckd.serverrunspawn.19", graphNode, err))
			return
		}
		if err := s.store.SetGraphAttempt(key, graphNode, attempt); err != nil {
			s.stopLaunched(ctx, key, id, x, i18n.E("err.deckd.serverrunspawn.20", graphNode, attempt, err), true,
				i18n.E("err.deckd.serverrunspawn.21", graphNode, attempt))
			writeErr(w, http.StatusServiceUnavailable, i18n.E("err.deckd.serverrunspawn.22", err))
			return
		}
	}
	// 先记「启动期」再让会话对外可见：登记成 running 之后、看完画面之前，推送与发话都不能往它打字（n055 评审 1 B2）
	if err := s.store.MarkStarting(key); err != nil {
		// 记不下启动期就不能保证这期间没有别的写入：停掉刚起的会话、不送任务（attempt 2 自查）
		s.stopLaunched(ctx, key, id, x, i18n.E("err.deckd.serverrunspawn.42", err), false, i18n.E("err.deckd.serverrunspawn.43"))
		writeErr(w, http.StatusServiceUnavailable, i18n.E("err.deckd.serverrunspawn.23", id, err))
		return
	}
	if err := s.store.LaunchSucceeded(key, id, tmux); err != nil {
		writeErr(w, http.StatusServiceUnavailable, i18n.E("err.deckd.serverrunspawn.24", id, err))
		return
	}
	// 写入权与第一条 brief：子 agent 登记给工作台（自动派送）；主 agent 由 owner 持有，用 owner 许可
	var permit lease.Permit
	if x.Role == "sub" {
		if err := s.agents.leases.Grant(id, i18n.E("err.deckd.serverrunspawn.44", creatorOf(c)).Error()); err != nil {
			s.store.SetSessionStatus(key, deckstore.StatusRunning, i18n.E("err.deckd.serverrunspawn.45", err).Error())
		}
		permit, err = s.agents.leases.Automatic(id)
	} else {
		permit, err = s.agents.leases.Manual(id)
	}
	if x.Role == "main" {
		// 接管已结束主 agent 留下的子 agent 与待送汇报（旧主 agent 退出后由 owner 起的新主 agent）
		if subs, reps, err := s.store.AdoptOrphans(x.Project, key); err != nil {
			s.store.NotifyCoded(x.Project, "spawn", i18n.E("err.deckd.serverrunspawn.46", deckstore.Arg(err, 160)), "deckd")
		} else if subs > 0 || reps > 0 {
			s.store.NotifyCoded(x.Project, "spawn", i18n.E("err.deckd.serverrunspawn.25", subs, reps), "deckd")
		}
	}
	delivered := false
	var noteErr *i18n.Error
	var dialog *i18n.Error
	if err == nil {
		// 先看画面：CLI 停在启动确认（目录信任等）上、或认不出输入框，就不送，在后台等（n055）
		dialog = s.settleScreen(ctx, id, x.Tool)
	}
	if err != nil {
		noteErr = i18n.E("err.deckd.serverrunspawn.47", err)
		s.store.FinishStart(key)
	} else if dialog != nil {
		got, _ := s.store.Session(key)
		noteErr = s.holdForOwner(got, key, id, dialog, plan.brief, permit)
	} else if derr := s.sendFirstMessage(ctx, key, permit, id, plan.brief); derr != nil {
		noteErr = i18n.E("err.deckd.serverrunspawn.brief", deckstore.Arg(derr, 200))
		// 上游送完了但证实不了提交（Codex 实测 delivery=unverified）：打字已经结束，不会与 owner 的输入交错，
		// 主 agent 照常放开输入；记下并请 owner 看一眼（没提交的话，brief 留在输入框里看得见）
		if x.Role == "main" && agentdeck.IsDeliveryUnconfirmed(derr) {
			s.store.ConfirmBrief(key)
			noteErr = i18n.E("err.deckd.serverrunspawn.48", deckstore.Arg(derr, 160))
			s.store.NotifyCoded(x.Project, "spawn", i18n.E("err.deckd.serverrunspawn.26", x.TitleArg()), "deckd")
		} else {
			s.store.FinishStart(key)
		}
	} else {
		delivered = true
		if x.Role == "main" {
			s.store.ConfirmBrief(key)
		} else {
			s.store.FinishStart(key)
		}
	}
	note := ""
	if noteErr != nil {
		note = noteErr.Error()
	}
	if note != "" {
		s.store.SetSessionStatus(key, deckstore.StatusRunning, note)
	}
	if x.Role == "main" && s.view != nil {
		s.view.OpenMainInput(id) // 主 agent 若已显示在中间：按最新状态放开输入（原先要等下一次切换）
	}
	var who any = "owner"
	if c.Role == RoleMain {
		who = i18n.E("err.deckd.serverrunspawn.49")
	}
	var what any = i18n.E("err.deckd.serverrunspawn.50")
	if x.Role == "main" {
		what = i18n.E("err.deckd.serverrunspawn.49")
	}
	s.store.NotifyCoded(x.Project, "spawn", i18n.E("err.deckd.serverrunspawn.27", who, what, x.TitleArg(), plan.agent.Name, x.Model), creatorOf(c))
	got, _ := s.store.Session(key)
	body := map[string]any{"session": got, "brief_delivered": delivered, "note": note}
	if noteErr != nil {
		if c, a, ok := i18n.Wire(noteErr); ok {
			body["note_code"] = c
			body["note_args"] = deckstore.CleanArgs(a, 300)
		}
	}
	writeJSON(w, http.StatusCreated, body)
}

// quotaUnsure：这个工具的额度是否未知或已用尽（取不到看板也算未知）。
func (s *Server) quotaUnsure(ctx context.Context, tool string) bool {
	status := s.quotaStatus(ctx, tool)
	return status == dashquota.Unknown || status == dashquota.Exhausted
}

func (s *Server) quotaStatus(ctx context.Context, tool string) string {
	switch {
	case s.agents != nil && s.agents.quotaOf != nil:
		return s.agents.quotaOf(ctx, tool)
	case s.quota != nil:
		return s.quota.Tool(ctx, tool).Status
	}
	return dashquota.Unknown
}

// resolveTier 是派子 agent 时按档位挑模型（GET /v1/resolve 用同一个，算出的 args_sha256 才对得上）：
// 清单顺序、跳过看板确知用尽的（n057）；点了 agent 就只在它的模型里挑。全都用尽时：frontier 照用第一个，
// 交给原有的额度门上报 owner（不回退 n051 的语义）；其余档位说明原因、不派。
func (s *Server) resolveTier(ctx context.Context, set deckstore.Settings, project string, tier deckstore.Tier, agent string) (deckstore.Resolved, error) {
	res, err := set.ResolveTierFor(project, tier, agent, s.exhaustedFor(ctx))
	var all *deckstore.ErrAllExhausted
	if errors.As(err, &all) && tier == deckstore.Frontier {
		return all.First, nil
	}
	return res, err
}

// exhaustedFor：按档位挑模型时，看板确知额度用尽的 agent 跳过，取清单里的下一个（n057，owner：「排第一的优先，额度用尽就换下一个」）。
// 额度未知不算用尽：照常按顺序取（frontier 子 agent 的额度门另外照旧把关）。用的是 Client.Tool（只认看板真实窗口），不是面板的估算。
func (s *Server) exhaustedFor(ctx context.Context) func(deckstore.AgentDef) bool {
	return func(a deckstore.AgentDef) bool { return s.quotaStatus(ctx, a.Tool) == dashquota.Exhausted }
}

// mainGone：旧主 agent 已经起来过（有 tmux 会话名），且那个 tmux 会话确实不在了。还在启动中、查不了、出错都按「还在」算。
func (s *Server) mainGone(ctx context.Context, y deckstore.Session) bool {
	if y.Status == deckstore.StatusLaunching || y.TmuxSession == "" || s.agents.bypass.Alive == nil {
		return false
	}
	alive, err := s.agents.bypass.Alive(ctx, y.TmuxSession)
	return err == nil && !alive
}

func (s *Server) processesNoBypass(ctx context.Context, tmux string, a deckstore.AgentDef) error {
	if s.agents.bypass.Processes == nil {
		return i18n.E("err.deckd.serverprocessesnobypass.1")
	}
	return s.agents.bypass.Processes(ctx, tmux, agentdeck.LaunchArgs{Tool: a.Tool, Binary: a.Binary, Extra: a.ExtraArgs})
}

// stopLaunched 停掉刚起、但不能交任务的会话（带绕过参数、Graph 开不成一轮等）：先把写入权登记给工作台，用自动许可停。
// 停成了且 keep 为假，释放名额；没停成或 keep 为真（例如账本结果不明），标结果不明、继续占名额，交给 owner 处理。都通知 owner。
func (s *Server) stopLaunched(ctx context.Context, key, id string, x deckstore.Session, why any, keep bool, what any) {
	stopErr := s.agents.leases.Grant(id, i18n.E("err.deckd.serverstoplaunched.3").Error())
	if stopErr == nil {
		var permit lease.Permit
		if permit, stopErr = s.agents.leases.Automatic(id); stopErr == nil {
			stopErr = s.agents.upstream.StopSession(ctx, permit, id)
		}
	}
	note := i18n.PresentZH(deckstore.Clean(textOf(why), 300)) // 记到会话上的中文，不带中性标记
	if stopErr != nil {
		note += i18n.E("err.deckd.serverstoplaunched.4", deckstore.Arg(stopErr, 200)).Error()
		s.store.LaunchFailed(key, true, note)
		s.store.NotifyCoded(x.Project, "spawn", i18n.E("err.deckd.serverstoplaunched.1", x.TitleArg(), what, deckstore.Arg(stopErr, 120), id), "deckd")
		return
	}
	s.store.LaunchFailed(key, keep, note)
	s.store.NotifyCoded(x.Project, "spawn", i18n.E("err.deckd.serverstoplaunched.2", x.TitleArg(), what, nestOrText(why, 200)), "deckd")
}

// textOf 取错误的中文原文。普通字符串原样返回。
func textOf(v any) string {
	if v == nil {
		return ""
	}
	if e, ok := v.(interface{ Error() string }); ok {
		return e.Error()
	}
	return fmt.Sprint(v)
}

// nestOrText 把停止原因收成参数。中文 Error() 仍是截断后的原文。
// 超长时也留下码，英文不再显示截断后的中文。
func nestOrText(v any, max int) any {
	switch x := v.(type) {
	case nil:
		return ""
	case error:
		return deckstore.Arg(x, max)
	case string:
		return deckstore.ArgText(x, max)
	}
	return v
}

// launchOutcome 判断启动结果：拿到会话号就算起来了；上游明确报错（有错误码或错误文字）是失败，释放名额；
// 其余（超时、回执读不懂）是结果不明，继续占着名额，交给 owner 核对。
func launchOutcome(out agentdeck.LaunchResult, err error) (id string, uncertain bool, why any) {
	id = out.SessionID
	if id == "" {
		id = out.ID
	}
	if err == nil {
		if id == "" {
			return "", true, i18n.E("err.deckd.launchoutcome.nosession")
		}
		return id, false, ""
	}
	// 有码就留下码。没有码的上游原文清洗后仍是字符串。
	why = deckstore.Arg(err, 300)
	if out.Error != "" || out.Code != "" {
		return "", false, why
	}
	return "", true, why
}

// DeckCommand 是 brief 里写给 agent 的 deck 命令（daemon 换成 deckd 自己的可执行文件绝对路径）。
// n051 Codex 端到端实测：Codex 用登录 shell 跑命令，PATH 按 owner 的 profile 重设，只写 deck 会 command not found。
var DeckCommand = "deck"

// withDeckCommand 把 brief 模板里的「deck agent …」换成 DeckCommand（只用于模板，不碰任务正文：正文按原文算过 sha256）。
// DeckCommand 里的 % 要转义，模板随后还要过 Sprintf。
func withDeckCommand(brief string) string {
	if DeckCommand == "deck" {
		return brief
	}
	return strings.ReplaceAll(brief, "deck agent ", strings.ReplaceAll(DeckCommand, "%", "%%")+" agent ")
}

// subBrief 是送给子 agent 的第一条消息：任务正文，加上收尾时必须上报的约定（§14.4）。
func subBrief(p deckstore.ProjectDef, x deckstore.Session, task string) string {
	return fmt.Sprintf(withDeckCommand(`你是项目「%s」的子 agent（%s），由主 agent 通过「安排 Anpai」派出。

任务：
%s

约定：
- 只做这一件事；需要 owner 决定的事（花额度、改电脑设置、推送代码、和别人意见不一、偏离 owner 的决定）先停下，在汇报里写明，由主 agent 上报。
- 不要使用跳过权限确认的参数。
- 做完（或做不下去）时运行：deck agent report --done --text "<结论、改了什么、证据在哪、还有什么没做>"
`), p.Name, i18n.PresentZH(x.Title), task) // 给模型读：标题里的中性标记换成中文记号
}

// MainBrief 是送给主 agent 的第一条消息：项目、工具用法与规矩（§5）。
func MainBrief(p deckstore.ProjectDef) string {
	env := p.Envelope
	return fmt.Sprintf(withDeckCommand(`你是项目「%s」（%s，目录 %s）的主 agent，通过「安排 Anpai」工作。

规矩：
- 只和 owner 对接。方案先与 owner 确认，再开子 agent。
- 开子 agent：deck agent spawn --tier <economy|balanced|frontier> --title <标题> --task-file <任务文件>（deck 会自动通知 owner）。
- 看子 agent：deck agent list / deck agent output <id>；给子 agent 发话：deck agent send <id> --text <内容>（只能发给已经停下的子 agent；它还在做上一件时会被拒，等它的汇报转告到你这里再发）。
- 五类事项必须上报、等 owner 决定：花额度（cost）、改电脑设置（config）、推送代码（push）、协作者意见不一（disagree）、偏离 owner 决策（deviation）：
  deck agent escalate --kind <类别> --summary <说明> --options <选项,…> --recommend <推荐>
- owner 已作的决定：deck agent decisions（用来判断是否偏离）。
- 不使用任何跳过权限确认的参数。子 agent 做完会自动转告你，标明「主动报告」；
  「推断提醒」（它停下了：可能做完，也可能在等输入或权限确认）与「账本提醒」（账本显示它的节点已提交）都只是提醒、不算完成，请用 deck agent output 查看。
- 开完子 agent 就结束这一轮回复、停下来等：汇报只在你空闲时送进来。不要用 sleep 或循环等待（你一直在忙，汇报就送不进来）。
  确实需要在一个命令里等，就运行 deck agent wait --timeout 2m（可以重复运行）：有汇报就打印出来并自动确认收到（之后不会再转告一次），没有就到时返回。

预算包络 %s v%d：同时最多 %d 个子 agent，frontier 子 agent 最多 %d 个；超出 deck 会生成决定等 owner 批准。
`), p.Name, p.ID, p.Path, env.ID, env.Version, env.MaxSubagents, env.MaxFrontier)
}

// postMain：owner 起项目的主 agent（POST /v1/projects/{id}/main）。按项目设置的主 agent 与档位，走同一套准入。
func (s *Server) postMain(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, true, RoleOwner) {
		return
	}
	if s.agents == nil {
		writeErr(w, http.StatusServiceUnavailable, i18n.E("err.deckd.serverpostmain.1"))
		return
	}
	if err := s.stopped(); err != nil {
		fail(w, http.StatusServiceUnavailable, "%v", err)
		return
	}
	var b struct {
		RequestID string `json:"request_id"`
	}
	if err := readBody(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, i18n.E("err.deckd.serverpostagents.2", err))
		return
	}
	set := s.store.Settings()
	p, ok := set.Project(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, i18n.E("err.deckd.serverplanspawn.1", r.PathValue("id")))
		return
	}
	// 主 agent：在这一档清单里取它自己的、额度没确知用尽的第一个模型（n057）；全都用尽时 owner 亲自起，照用第一个
	res, err := set.ResolveTierFor(p.ID, p.MainAgent.Tier, p.MainAgent.Agent, s.exhaustedFor(r.Context()))
	var all *deckstore.ErrAllExhausted
	if errors.As(err, &all) {
		res, err = all.First, nil
	}
	if err != nil || res.Agent.ID != p.MainAgent.Agent {
		writeErr(w, http.StatusBadRequest, i18n.E("err.deckd.serverpostmain.2", p.ID, err))
		return
	}
	a := res.Agent
	if !deckstore.ValidModelID(res.Model) {
		writeErr(w, http.StatusBadRequest, i18n.E("err.deckd.serverpostmain.3", res.Model))
		return
	}
	for _, check := range []error{agentdeck.CheckNoBypass(a.Tool, a.Binary, res.Model), agentdeck.CheckExtraArgs(a.Tool, a.ExtraArgs), s.upstreamConfigNoBypass(a.Tool), agentdeck.CheckToolModel(a.Tool, false),
		agentdeck.CheckTakesTaskText(a.Tool, a.Agent), deckstore.CheckPath(p.Path)} {
		if check != nil {
			fail(w, http.StatusBadRequest, "%v", check)
			return
		}
	}
	reqID := b.RequestID
	if reqID == "" {
		reqID = fmt.Sprintf("main-%s-%d", p.ID, time.Now().UnixNano())
	}
	brief := MainBrief(p)
	titleErr := i18n.E("err.deckd.serverpostmain.4", p.Name)
	title := titleErr.Error()
	titleCode, titleArgs, _ := i18n.Wire(titleErr) // 标题是本项目拼的，英文界面按码翻译
	x := deckstore.Session{Project: p.ID, Role: "main", Agent: res.Agent.ID, Tool: a.Tool, Model: res.Model, Tier: res.Tier,
		Title: title, TitleCode: titleCode, TitleArgs: i18n.Args(titleArgs), CreatedBy: "owner", RequestID: reqID, Dir: p.Path, TaskSHA256: TaskSHA256(brief),
		ArgsSHA256: SpawnArgsSHA256(res.Agent.ID, string(res.Tier), res.Model, title, p.Path, "")}
	s.runSpawn(w, detached(r), c, spawnPlan{project: p, agent: a, x: x, brief: brief}, "", "")
}

// agentTarget 取 {id} 指的会话，并检查调用者能不能对它操作：主 agent 只能对本项目的子 agent；owner 任意。
func (s *Server) agentTarget(w http.ResponseWriter, c Caller, id string) (deckstore.Session, bool) {
	x, ok := s.store.Session(id)
	if !ok {
		writeErr(w, http.StatusNotFound, i18n.E("err.deckd.serveragenttarget.1", id))
		return x, false
	}
	if c.Role == RoleMain && x.Project != c.Project {
		writeErr(w, http.StatusForbidden, i18n.E("err.deckd.serveragenttarget.2"))
		return x, false
	}
	if x.AgentDeckID == "" {
		writeErr(w, http.StatusConflict, i18n.E("err.deckd.serveragenttarget.3", x.Key, x.Status))
		return x, false
	}
	return x, true
}

// getAgentOutput：看子 agent 画面上的输出（已去掉控制序列、打码）。
func (s *Server) getAgentOutput(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, false, RoleMain, RoleOwner) {
		return
	}
	if s.agents == nil {
		writeErr(w, http.StatusServiceUnavailable, i18n.E("err.deckd.servergetagentoutput.1"))
		return
	}
	x, ok := s.agentTarget(w, c, r.PathValue("id"))
	if !ok {
		return
	}
	out, err := s.agents.upstream.PaneOutputPlain(r.Context(), x.AgentDeckID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, i18n.E("err.deckd.servergetagentoutput.2", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": x.Key, "output": deckstore.Clean(out, 20000)})
}

// postAgentSend：给子 agent 发话。不能发给主 agent（硬拒绝，架构稿 §4）；owner 接管着的会话不能送（写入权在 owner 手上）。
func (s *Server) postAgentSend(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, true, RoleMain, RoleOwner) {
		return
	}
	if s.agents == nil {
		writeErr(w, http.StatusServiceUnavailable, i18n.E("err.deckd.servergetagentoutput.1"))
		return
	}
	if err := s.stopped(); err != nil {
		fail(w, http.StatusServiceUnavailable, "%v", err)
		return
	}
	var b struct {
		Text string `json:"text"`
	}
	if err := readBody(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, i18n.E("err.deckd.serverpostagents.2", err))
		return
	}
	x, ok := s.agentTarget(w, c, r.PathValue("id"))
	if !ok {
		return
	}
	if x.Role != "sub" {
		writeErr(w, http.StatusForbidden, i18n.E("err.deckd.serverpostagentsend.1"))
		return
	}
	if strings.TrimSpace(b.Text) == "" || len(b.Text) > maxTaskBytes {
		writeErr(w, http.StatusBadRequest, i18n.E("err.deckd.serverpostagentsend.2"))
		return
	}
	// 同一个子 agent 的发话一次只走一条（attempt 3 自查：并发发话各自加代次、各自撤回，代次会错位）
	mu, _ := s.sendLocks.LoadOrStore(x.Key, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	// 只给停下来的子 agent 发新的话（owner 在 attempt 3 设计复审选 A'）：它还在做上一件时追加的话，与上一件的完成分不清先后，
	// 「哪条报告属于哪件任务」就会错（attempt 3 自查 S1）。这里先按缓存的状态挡一下；真正的判断由上游在打字前一刻做（SendIfIdle）
	if s.activity.of(x.TmuxSession) == "running" {
		writeErr(w, http.StatusConflict, i18n.E("err.deckd.busysubtext"))
		return
	}
	if cur, ok := s.store.Session(x.Key); ok && cur.StartPending() && cur.BriefSending {
		// deckd 重启时它的任务正在送：送没送到不确定，deck 不会补送（rev 117），等它出现空输入框前不往它写（评审 3 B1）。
		// 不能说「会自动送」：没送到时主 agent 照提示不重发就会一直空等（attempt 5 自查）
		writeErr(w, http.StatusConflict, i18n.E("err.deckd.serverpostagentsend.3", x.Key))
		return
	}
	if cur, ok := s.store.Session(x.Key); ok && cur.StartPending() {
		// 它还在启动期，或 CLI 停在启动确认（目录信任等）上：确认只归 owner，打进去的字加回车就替 owner 选了（n055 自查 B1、评审 1 B2）。
		// deck 会在 CLI 就绪、owner 处理完确认后自动送它的任务，不要重发
		writeErr(w, http.StatusConflict, i18n.E("err.deckd.serverpostagentsend.4", x.Key))
		return
	}
	permit, err := s.agents.leases.Automatic(x.AgentDeckID)
	if err != nil {
		writeErr(w, http.StatusConflict, i18n.E("err.deckd.serverpostagentsend.5", err))
		return
	}
	// 发之前把任务代次加 1 并落盘：上游判定它停着、打进新的话之后，它的报告都属于新任务。记不下代次就不发
	gen, err := s.store.BumpTaskGen(x.Key)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, i18n.E("err.deckd.serverpostagentsend.6", err))
		return
	}
	err = s.agents.upstream.SendIfIdle(detached(r), permit, x.AgentDeckID, b.Text)
	if err == nil || agentdeck.IsDeliveryUnconfirmed(err) {
		// 发话返回：之后到的完成都属于新的一代（发的途中到的，按上一代有没有完成来归，见 deckstore EnqueueReport）
		if eerr := s.store.EndSend(x.Key); eerr != nil {
			s.store.NotifyCoded(x.Project, "report", i18n.E("err.deckd.serverpostagentsend.8", deckstore.Arg(eerr, 160)), "deckd")
		}
	}
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"session": x.Key, "delivered": true})
	case agentdeck.IsDeliveryUnconfirmed(err):
		// 话可能已打进去、只是没证实提交（Codex 总是这样）：新的一代保留
		writeJSON(w, http.StatusAccepted, withNote(map[string]any{"session": x.Key, "delivered": false}, err, 300))
	default:
		// 没送成（还在跑，或上游投递失败）：撤回这一代。撤回前已经收到这一代的完成就不撤——那是上一件在这一刻做完的报告，
		// 照常转告；下次发话再开新的一代
		if _, rerr := s.store.RevertTaskGen(x.Key, gen); rerr != nil {
			s.store.NotifyCoded(x.Project, "report", i18n.E("err.deckd.serverpostagentsend.9", deckstore.Arg(rerr, 160)), "deckd")
		}
		if errors.Is(err, agentdeck.ErrTargetBusy) {
			writeErr(w, http.StatusConflict, i18n.E("err.deckd.busysubtext"))
			return
		}
		writeErr(w, http.StatusBadGateway, i18n.E("err.deckd.serverpostagentsend.7", err))
	}
}

// busySubText 是子 agent 还在跑时发话被拒的说明（主 agent 看得到）。
const busySubText = "子 agent 正在做上一件，这次没发：等它报告完成（转告会送到你这里）或停下后再发。owner 可以直接在它的窗格里说话"

// executeCodexTrust：owner 批准 codex_trust 决定后记住授权（§14.3），用完即 applied。主 agent 用新的请求 id 再开一次即可。
func (s *Server) executeCodexTrust(d deckstore.Decision) error {
	if d.Kind != deckstore.KindCodexTrust || d.State != deckstore.StateApproved {
		return nil
	}
	if _, err := s.store.BeginExecute(d.ID, d.Scope); err != nil {
		return err
	}
	sc := d.Scope
	err := s.store.GrantTrust(deckstore.TrustGrant{Project: sc["project"], Tool: sc["tool"], ProjectDir: sc["project_dir"], Dir: sc["dir"],
		ToolPath: sc["tool_path"], ToolVersion: sc["tool_version"], ApprovedAt: time.Now()})
	if err != nil {
		s.store.FinishExecute(d.ID, deckstore.StateFailed, i18n.E("err.deckd.serverexecutecodextrust.2", err).Error())
		return err
	}
	s.store.FinishExecute(d.ID, deckstore.StateApplied, i18n.E("err.deckd.serverexecutecodextrust.3").Error())
	s.store.NotifyCoded(sc["project"], "trust", i18n.E("err.deckd.serverexecutecodextrust.1", sc["tool"], sc["dir"]), "owner")
	return nil
}

// projectctlGraphStart 是生产用的 Graph 通道开工：在项目的 Graph 根目录跑 projectctl start。
func projectctlGraphStart(script string) GraphStartFunc {
	return func(ctx context.Context, p deckstore.ProjectDef, node, actor, model string, route deckstore.Tier) (int, error) {
		c := projectctl.NewClient(p.GraphRoot, script)
		res, err := c.Start(ctx, projectctl.StartRequest{NodeID: node, Actor: actor, Role: "workbench_executor",
			RouteClass: string(route), Model: model, Reason: i18n.E("err.deckd.projectctlgraphstart.1").Error()})
		if err != nil {
			return 0, err
		}
		return res.Attempt, nil
	}
}
