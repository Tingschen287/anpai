package deckd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	// graphState 查 Graph 节点此刻的状态（projectctl brief，只读）；空则不看账本提交信号
	graphState func(ctx context.Context, p deckstore.ProjectDef, node string) (projectctl.State, error)
	// quotaOf 取某个工具的额度状态（dashquota 的 available/warn/exhausted/unknown）；空时用 Server 的看板客户端，测试可换
	quotaOf func(ctx context.Context, tool string) string
}

// WithAgents 接上开子 agent 需要的依赖（daemon 与测试调用）。
func (s *Server) WithAgents(up Upstream, leases *lease.Store, toolInfo ToolInfoFunc, graphStart GraphStartFunc) {
	s.agents = &agentsDeps{upstream: up, leases: leases, toolInfo: toolInfo, graphStart: graphStart}
}

// WithBypassChecks 接上上游侧的绕过检查（daemon 与测试调用；不接就不开任何 agent）。
func (s *Server) WithBypassChecks(b BypassChecks) { s.agents.bypass = b }

// upstreamConfigNoBypass：上游配置会不会给这个工具加跳过权限的参数。
func (s *Server) upstreamConfigNoBypass(tool string) error {
	if s.agents.bypass.Config == nil {
		return fmt.Errorf("deckd 没接上上游配置检查，确认不了 agent-deck 不会加跳过权限的参数，不开")
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
		fail(w, http.StatusServiceUnavailable, "deckd 没接上 agent-deck，不能开子 agent")
		return
	}
	if err := s.stopped(); err != nil {
		fail(w, http.StatusServiceUnavailable, "%v", err)
		return
	}
	var b spawnBody
	if err := readBody(r, &b); err != nil {
		fail(w, http.StatusBadRequest, "请求体不对：%v", err)
		return
	}
	project := b.Project
	if c.Role == RoleMain {
		if project != "" && project != c.Project {
			fail(w, http.StatusForbidden, "主 agent 只能在自己的项目 %s 里开子 agent", c.Project)
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
		return plan, fmt.Errorf("没有项目 %q", project)
	}
	var res deckstore.Resolved
	var err error
	switch {
	case b.Model != "" && b.Tier != "":
		return plan, fmt.Errorf("档位与模型只给一个")
	case b.Model != "":
		res, err = set.ResolveModel(project, b.Model)
	default:
		res, err = set.ResolveTier(project, deckstore.Tier(b.Tier))
	}
	if err != nil {
		return plan, err
	}
	if b.Agent != "" && b.Agent != res.Agent.ID {
		return plan, fmt.Errorf("模型 %s 属于 agent %s，不是请求里的 %s", res.Model, res.Agent.ID, b.Agent)
	}
	a := res.Agent
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
		return plan, fmt.Errorf("任务是空的：说不清要子 agent 做什么，就别开")
	}
	if len(b.Task) > maxTaskBytes {
		return plan, fmt.Errorf("任务正文超过 %d 字节", maxTaskBytes)
	}
	dir := b.Dir
	if dir == "" {
		dir = p.Path
	}
	if err := deckstore.CheckPath(dir); err != nil {
		return plan, fmt.Errorf("工作目录：%w", err)
	}
	if b.TaskSHA256 != TaskSHA256(b.Task) {
		return plan, fmt.Errorf("task_sha256 与任务正文对不上")
	}
	if want := SpawnArgsSHA256(res.Agent.ID, string(res.Tier), res.Model, b.Title, dir, b.GraphNode); b.ArgsSHA256 != want {
		return plan, fmt.Errorf("args_sha256 与启动参数对不上（agent、档位、模型、标题、目录、Graph 节点按 deckd 解析的结果算）")
	}
	x := deckstore.Session{Project: project, Role: "sub", Parent: c.Session, Agent: res.Agent.ID, Tool: a.Tool, Model: res.Model,
		Tier: res.Tier, Title: b.Title, CreatedBy: creatorOf(c), RequestID: b.RequestID, Dir: dir,
		TaskSHA256: b.TaskSHA256, ArgsSHA256: b.ArgsSHA256}
	if b.GraphNode != "" {
		if p.GraphRoot == "" {
			return plan, fmt.Errorf("项目 %s 没有配置 Graph 根目录，不能走 Graph 通道", project)
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
	if trust := agentdeck.CheckCodexTrust(x.Tool); trust != agentdeck.CodexTrustNo {
		path, version, err := s.agents.toolInfo(ctx, plan.agent.Binary)
		if err != nil {
			fail(w, http.StatusBadRequest, "确认不了 %s 的可执行文件与版本（Codex trust 门要按它们记住授权）：%v", plan.agent.Binary, err)
			return
		}
		if !s.store.TrustValid(x.Project, x.Tool, p.Path, x.Dir, path, version) {
			d, err := s.store.CreateDecision(deckstore.NewDecision{Project: x.Project, Kind: deckstore.KindCodexTrust,
				Summary: fmt.Sprintf("启动 %s 会让上游整份改写 ~/.codex/config.toml（加入 %s 的信任）。批准后记住：同一项目、工具、目录、可执行文件与版本以后不再问", plan.agent.Name, x.Dir),
				Scope: map[string]string{"action": "codex_trust", "project": x.Project, "tool": x.Tool, "project_dir": p.Path, "dir": x.Dir,
					"tool_path": path, "tool_version": version, "request_id": x.RequestID},
				Recommend: deckstore.OptApprove, CreatedBy: creatorOf(c)})
			if err != nil {
				fail(w, http.StatusConflict, "生成 Codex trust 决定失败：%v", err)
				return
			}
			writeJSON(w, http.StatusAccepted, map[string]any{"pending": deckstore.KindCodexTrust, "decision": d,
				"next": "owner 批准后用新的 request_id 再开一次"})
			return
		}
		s.store.Notify(x.Project, "spawn", fmt.Sprintf("按已记住的授权启动 %s，~/.codex/config.toml 会被上游改写", plan.agent.Name), "deckd")
	}
	// 主 agent：每个项目同时只能有一个。旧主 agent 的 tmux 会话已经不在（退出或被关掉）就记为已结束、放行
	// （n051 自查：此前没有任何路径结束主 agent，它一退出，这个项目就再也起不了主 agent）。
	if x.Role == "main" {
		for _, y := range s.store.Sessions(x.Project) {
			if y.Role == "main" && (y.Status == deckstore.StatusLaunching || y.Status == deckstore.StatusRunning || y.Status == deckstore.StatusUncertain) {
				if s.mainGone(ctx, y) {
					s.store.SetSessionStatus(y.Key, deckstore.StatusStopped, "tmux 会话 "+y.TmuxSession+" 已不在，deck 记为已结束")
					s.store.Notify(x.Project, "spawn", fmt.Sprintf("旧主 agent「%s」的会话已不在，记为已结束", y.Title), "deckd")
					continue
				}
				fail(w, http.StatusConflict, "项目 %s 已有主 agent %s（%s），它的会话还在或确认不了；先在 agent-deck 里停掉它再起", x.Project, y.Key, y.Status)
				return
			}
		}
	}
	// 额度未知（看板取不到、数据过期、没有来源）或已用尽时启动 frontier 子 agent：一律上报（架构稿 §5、O5；n051 评审 1 B2）。
	// 批准绑定本次请求（SpawnScope），主 agent 带同一请求与决定 id 再来一次即放行。主 agent 由 owner 自己起，不在此列。
	quotaGate := x.Role == "sub" && x.Tier == deckstore.Frontier && s.quotaUnsure(ctx, x.Tool)
	if quotaGate && exception == "" {
		// 同一个批准随后也会被预留当作超额例外用掉：包络也超了就在同一条决定里写明，owner 一次看清两个原因
		summary := fmt.Sprintf("额度未知或已用尽时启动 frontier：请求开 %s（%s · %s）「%s」", plan.agent.Name, x.Tier, x.Model, x.Title)
		if u := s.store.Slots(x.Project); u.Subagents >= p.Envelope.MaxSubagents || u.Frontier >= p.Envelope.MaxFrontier {
			summary = fmt.Sprintf("额度未知或已用尽，且超出预算包络（已用 %d/%d 个子 agent、%d/%d 个 frontier）：请求开 %s（%s · %s）「%s」",
				u.Subagents, p.Envelope.MaxSubagents, u.Frontier, p.Envelope.MaxFrontier, plan.agent.Name, x.Tier, x.Model, x.Title)
		}
		d, derr := s.store.CreateDecision(deckstore.NewDecision{Project: x.Project, Kind: deckstore.KindCost,
			Summary: summary,
			Scope:   deckstore.SpawnScope(x, p), Recommend: deckstore.OptReject, CreatedBy: creatorOf(c)})
		if derr != nil {
			fail(w, http.StatusConflict, "额度未知时启动 frontier 要 owner 批准，生成 cost 决定失败：%v", derr)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"pending": deckstore.KindCost, "decision": d,
			"next": "owner 批准后带同一个请求（同一 request_id、任务、参数）与 exception=" + d.ID + " 再开一次"})
		return
	}
	// 预算包络：预留名额；超出生成 cost 决定
	if exception != "" {
		if _, err := s.store.BeginExecute(exception, deckstore.SpawnScope(x, p)); err != nil {
			fail(w, http.StatusConflict, "超额例外不能用：%v", err)
			return
		}
	}
	res, err := s.store.Reserve(x, exception)
	var over *deckstore.ErrOverEnvelope
	if errors.As(err, &over) {
		d, derr := s.store.CreateDecision(deckstore.NewDecision{Project: x.Project, Kind: deckstore.KindCost,
			Summary:   fmt.Sprintf("超出预算包络：%s。请求开 %s（%s · %s）「%s」", over.Reason, plan.agent.Name, x.Tier, x.Model, x.Title),
			Scope:     deckstore.SpawnScope(x, p),
			Recommend: deckstore.OptReject, CreatedBy: creatorOf(c)})
		if derr != nil {
			fail(w, http.StatusConflict, "%v；生成 cost 决定也失败：%v", err, derr)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"pending": deckstore.KindCost, "decision": d,
			"next": "owner 批准后带同一个请求（同一 request_id、任务、参数）与 exception=" + d.ID + " 再开一次"})
		return
	}
	if err != nil {
		if exception != "" {
			s.store.FinishExecute(exception, deckstore.StateFailed, "预留失败："+err.Error())
		}
		fail(w, http.StatusConflict, "预留名额失败：%v", err)
		return
	}
	key := res.Key
	if exception != "" {
		// 批准后名额又够了：Reserve 没消耗例外，它会一直停在 executing（n051 自查）。收尾为已执行、作废，不能再用
		if d, ok := s.store.Decision(exception); ok && d.State == deckstore.StateExecuting {
			why := "名额已够用，本次没有消耗这个例外；它已作废，不能再用"
			if quotaGate {
				why = "已按批准在额度未知时启动 frontier；这个例外已用掉，不能再用"
			}
			s.store.FinishExecute(exception, deckstore.StateApplied, why)
		}
	}
	if graphNode != "" && s.agents.graphStart == nil {
		s.store.LaunchFailed(key, false, "deckd 没配置 projectctl")
		fail(w, http.StatusServiceUnavailable, "deckd 没配置 projectctl，不能走 Graph 通道")
		return
	}
	// 启动
	lr := agentdeck.LaunchRequest{ProjectPath: x.Dir, Title: x.Title, Group: x.Project, Tool: x.Tool, Model: x.Model, NoWait: true}
	out, lerr := s.agents.upstream.Launch(ctx, lr)
	id, uncertain, why := launchOutcome(out, lerr)
	if id == "" {
		s.store.LaunchFailed(key, uncertain, why)
		code := http.StatusBadGateway
		writeJSON(w, code, map[string]any{"error": "启动失败：" + why, "session": key, "uncertain": uncertain})
		return
	}
	if err := agentdeck.CheckLaunchedTakesTaskText(out); err != nil {
		s.store.LaunchFailed(key, true, "上游起的不是 agent："+err.Error())
		fail(w, http.StatusBadGateway, "上游起的不是 agent，不送任务：%v（会话 %s 留在 agent-deck 里，请 owner 处理）", err, id)
		return
	}
	tmux := ""
	if list, err := s.agents.upstream.ListSessions(ctx); err == nil {
		tmux, _ = agentdeck.TmuxSessionOf(list, id)
	}
	// 启动后按实际在跑的命令行再核一次：上游自己加了绕过参数（或核对不了）就停掉会话、不送 brief
	if err := s.processesNoBypass(ctx, tmux, plan.agent); err != nil {
		s.stopLaunched(ctx, key, id, x, "启动后核对："+err.Error(), false, "起来后带着跳过权限的参数（或核对不了）")
		fail(w, http.StatusBadGateway, "会话 %s 起来后带着跳过权限的参数（或核对不了），已停掉、不送任务：%v", id, err)
		return
	}
	// Graph 通道：会话起来并核过之后才开一轮（n051 自查：先开一轮再启动，启动失败时账本里会留下没人干活的 attempt）。
	// 开不成就停掉刚起的会话；projectctl 结果不明（可能已经开了一轮）时继续占名额，请 owner 核对账本。
	if graphNode != "" {
		attempt, err := s.agents.graphStart(ctx, p, graphNode, "deck:"+key, x.Model, x.Tier)
		if err != nil {
			var notStarted *projectctl.ErrStartNotStarted
			unknown := !errors.As(err, &notStarted)
			what := "Graph 节点 " + graphNode + " 没开成一轮"
			if unknown {
				what = "Graph 节点 " + graphNode + " 开没开成一轮不确定，请核对账本"
			}
			s.stopLaunched(ctx, key, id, x, "projectctl start："+err.Error(), unknown, what)
			fail(w, http.StatusConflict, "Graph 节点 %s 开工失败，刚起的会话已停掉、不送任务：%v", graphNode, err)
			return
		}
		if err := s.store.SetGraphAttempt(key, graphNode, attempt); err != nil {
			s.stopLaunched(ctx, key, id, x, fmt.Sprintf("记不下 Graph attempt %s#%d：%v", graphNode, attempt, err), true,
				fmt.Sprintf("Graph 节点 %s 已开第 %d 轮，但 deck 记不下，请核对账本", graphNode, attempt))
			fail(w, http.StatusServiceUnavailable, "记不下 Graph attempt：%v", err)
			return
		}
	}
	if err := s.store.LaunchSucceeded(key, id, tmux); err != nil {
		fail(w, http.StatusServiceUnavailable, "会话 %s 起来了，但记不下：%v", id, err)
		return
	}
	// 写入权与第一条 brief：子 agent 登记给工作台（自动派送）；主 agent 由 owner 持有，用 owner 许可
	var permit lease.Permit
	if x.Role == "sub" {
		if err := s.agents.leases.Grant(id, "deck 派出（"+creatorOf(c)+"）"); err != nil {
			s.store.SetSessionStatus(key, deckstore.StatusRunning, "登记写入权失败："+err.Error())
		}
		permit, err = s.agents.leases.Automatic(id)
	} else {
		permit, err = s.agents.leases.Manual(id)
	}
	if x.Role == "main" {
		// 接管已结束主 agent 留下的子 agent 与待送汇报（旧主 agent 退出后由 owner 起的新主 agent）
		if subs, reps, err := s.store.AdoptOrphans(x.Project, key); err != nil {
			s.store.Notify(x.Project, "spawn", "新主 agent 接管旧主 agent 的子 agent 失败："+deckstore.Clean(err.Error(), 160), "deckd")
		} else if subs > 0 || reps > 0 {
			s.store.Notify(x.Project, "spawn", fmt.Sprintf("新主 agent 接管了旧主 agent 留下的 %d 个子 agent、%d 条待送汇报", subs, reps), "deckd")
		}
	}
	delivered := false
	note := ""
	if err != nil {
		note = "不能送 brief：" + err.Error()
	} else if derr := s.agents.upstream.SendMessage(ctx, permit, id, plan.brief); derr != nil {
		note = "brief " + deckstore.Clean(derr.Error(), 200)
		// 上游送完了但证实不了提交（Codex 实测 delivery=unverified）：打字已经结束，不会与 owner 的输入交错，
		// 主 agent 照常放开输入；记下并请 owner 看一眼（没提交的话，brief 留在输入框里看得见）
		if x.Role == "main" && agentdeck.IsDeliveryUnconfirmed(derr) {
			s.store.ConfirmBrief(key)
			note = "brief 已送出，上游没证实提交，请 owner 在中间窗格看一眼：" + deckstore.Clean(derr.Error(), 160)
			s.store.Notify(x.Project, "spawn", fmt.Sprintf("「%s」的第一条 brief 已送出，但上游没证实提交，请在中间窗格看一眼", x.Title), "deckd")
		}
	} else {
		delivered = true
		if x.Role == "main" {
			s.store.ConfirmBrief(key)
		}
	}
	if note != "" {
		s.store.SetSessionStatus(key, deckstore.StatusRunning, note)
	}
	who := "owner"
	if c.Role == RoleMain {
		who = "主 agent"
	}
	what := "子 agent"
	if x.Role == "main" {
		what = "主 agent"
	}
	s.store.Notify(x.Project, "spawn", fmt.Sprintf("%s开了%s「%s」（%s · %s）", who, what, x.Title, plan.agent.Name, x.Model), creatorOf(c))
	got, _ := s.store.Session(key)
	writeJSON(w, http.StatusCreated, map[string]any{"session": got, "brief_delivered": delivered, "note": note})
}

// quotaUnsure：这个工具的额度是否未知或已用尽（取不到看板也算未知）。
func (s *Server) quotaUnsure(ctx context.Context, tool string) bool {
	status := dashquota.Unknown
	switch {
	case s.agents.quotaOf != nil:
		status = s.agents.quotaOf(ctx, tool)
	case s.quota != nil:
		status = s.quota.Tool(ctx, tool).Status
	}
	return status == dashquota.Unknown || status == dashquota.Exhausted
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
		return fmt.Errorf("deckd 没接上启动后的命令行核对")
	}
	return s.agents.bypass.Processes(ctx, tmux, agentdeck.LaunchArgs{Tool: a.Tool, Binary: a.Binary, Extra: a.ExtraArgs})
}

// stopLaunched 停掉刚起、但不能交任务的会话（带绕过参数、Graph 开不成一轮等）：先把写入权登记给工作台，用自动许可停。
// 停成了且 keep 为假，释放名额；没停成或 keep 为真（例如账本结果不明），标结果不明、继续占名额，交给 owner 处理。都通知 owner。
func (s *Server) stopLaunched(ctx context.Context, key, id string, x deckstore.Session, why string, keep bool, what string) {
	stopErr := s.agents.leases.Grant(id, "deck 停掉不能交任务的会话")
	if stopErr == nil {
		var permit lease.Permit
		if permit, stopErr = s.agents.leases.Automatic(id); stopErr == nil {
			stopErr = s.agents.upstream.StopSession(ctx, permit, id)
		}
	}
	note := deckstore.Clean(why, 300)
	if stopErr != nil {
		note += "；停会话失败：" + deckstore.Clean(stopErr.Error(), 200)
		s.store.LaunchFailed(key, true, note)
		s.store.Notify(x.Project, "spawn", fmt.Sprintf("「%s」%s，deck 没能停掉它（%s），请 owner 立即处理 agent-deck 会话 %s", x.Title, what, deckstore.Clean(stopErr.Error(), 120), id), "deckd")
		return
	}
	s.store.LaunchFailed(key, keep, note)
	s.store.Notify(x.Project, "spawn", fmt.Sprintf("「%s」%s，deck 已停掉它、没送任务：%s", x.Title, what, deckstore.Clean(why, 200)), "deckd")
}

// launchOutcome 判断启动结果：拿到会话号就算起来了；上游明确报错（有错误码或错误文字）是失败，释放名额；
// 其余（超时、回执读不懂）是结果不明，继续占着名额，交给 owner 核对。
func launchOutcome(out agentdeck.LaunchResult, err error) (id string, uncertain bool, why string) {
	id = out.SessionID
	if id == "" {
		id = out.ID
	}
	if err == nil {
		if id == "" {
			return "", true, "上游没回会话号"
		}
		return id, false, ""
	}
	if out.Error != "" || out.Code != "" {
		return "", false, deckstore.Clean(err.Error(), 300)
	}
	return "", true, deckstore.Clean(err.Error(), 300)
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
	return fmt.Sprintf(withDeckCommand(`你是项目「%s」的子 agent（%s），由主 agent 通过 Agent Deck 派出。

任务：
%s

约定：
- 只做这一件事；需要 owner 决定的事（花额度、改电脑设置、推送代码、和别人意见不一、偏离 owner 的决定）先停下，在汇报里写明，由主 agent 上报。
- 不要使用跳过权限确认的参数。
- 做完（或做不下去）时运行：deck agent report --done --text "<结论、改了什么、证据在哪、还有什么没做>"
`), p.Name, x.Title, task)
}

// MainBrief 是送给主 agent 的第一条消息：项目、工具用法与规矩（§5）。
func MainBrief(p deckstore.ProjectDef) string {
	env := p.Envelope
	return fmt.Sprintf(withDeckCommand(`你是项目「%s」（%s，目录 %s）的主 agent，通过 Agent Deck 工作。

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
		fail(w, http.StatusServiceUnavailable, "deckd 没接上 agent-deck，不能起主 agent")
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
		fail(w, http.StatusBadRequest, "请求体不对：%v", err)
		return
	}
	set := s.store.Settings()
	p, ok := set.Project(r.PathValue("id"))
	if !ok {
		fail(w, http.StatusNotFound, "没有项目 %q", r.PathValue("id"))
		return
	}
	res, err := set.ResolveTier(p.ID, p.MainAgent.Tier)
	if err != nil || res.Agent.ID != p.MainAgent.Agent {
		fail(w, http.StatusBadRequest, "项目 %s 的主 agent 设置解析不出（%v）", p.ID, err)
		return
	}
	a := res.Agent
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
	title := "主 agent · " + p.Name
	x := deckstore.Session{Project: p.ID, Role: "main", Agent: res.Agent.ID, Tool: a.Tool, Model: res.Model, Tier: res.Tier,
		Title: title, CreatedBy: "owner", RequestID: reqID, Dir: p.Path, TaskSHA256: TaskSHA256(brief),
		ArgsSHA256: SpawnArgsSHA256(res.Agent.ID, string(res.Tier), res.Model, title, p.Path, "")}
	s.runSpawn(w, detached(r), c, spawnPlan{project: p, agent: a, x: x, brief: brief}, "", "")
}

// agentTarget 取 {id} 指的会话，并检查调用者能不能对它操作：主 agent 只能对本项目的子 agent；owner 任意。
func (s *Server) agentTarget(w http.ResponseWriter, c Caller, id string) (deckstore.Session, bool) {
	x, ok := s.store.Session(id)
	if !ok {
		fail(w, http.StatusNotFound, "没有会话 %q", id)
		return x, false
	}
	if c.Role == RoleMain && x.Project != c.Project {
		fail(w, http.StatusForbidden, "主 agent 不能操作其他项目的会话")
		return x, false
	}
	if x.AgentDeckID == "" {
		fail(w, http.StatusConflict, "会话 %s 还没起来（%s）", x.Key, x.Status)
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
		fail(w, http.StatusServiceUnavailable, "deckd 没接上 agent-deck")
		return
	}
	x, ok := s.agentTarget(w, c, r.PathValue("id"))
	if !ok {
		return
	}
	out, err := s.agents.upstream.PaneOutputPlain(r.Context(), x.AgentDeckID)
	if err != nil {
		fail(w, http.StatusBadGateway, "读不到输出：%v", err)
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
		fail(w, http.StatusServiceUnavailable, "deckd 没接上 agent-deck")
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
		fail(w, http.StatusBadRequest, "请求体不对：%v", err)
		return
	}
	x, ok := s.agentTarget(w, c, r.PathValue("id"))
	if !ok {
		return
	}
	if x.Role != "sub" {
		fail(w, http.StatusForbidden, "不能向主 agent 发话（硬拒绝：主 agent 只和 owner 对接）")
		return
	}
	if strings.TrimSpace(b.Text) == "" || len(b.Text) > maxTaskBytes {
		fail(w, http.StatusBadRequest, "消息为空或太长")
		return
	}
	// 同一个子 agent 的发话一次只走一条（attempt 3 自查：并发发话各自加代次、各自撤回，代次会错位）
	mu, _ := s.sendLocks.LoadOrStore(x.Key, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	// 只给停下来的子 agent 发新的话（owner 在 attempt 3 设计复审选 A'）：它还在做上一件时追加的话，与上一件的完成分不清先后，
	// 「哪条报告属于哪件任务」就会错（attempt 3 自查 S1）。这里先按缓存的状态挡一下；真正的判断由上游在打字前一刻做（SendIfIdle）
	if s.activity.of(x.TmuxSession) == "running" {
		fail(w, http.StatusConflict, "%s", busySubText)
		return
	}
	permit, err := s.agents.leases.Automatic(x.AgentDeckID)
	if err != nil {
		fail(w, http.StatusConflict, "现在不能给它发话：%v", err)
		return
	}
	// 发之前把任务代次加 1 并落盘：上游判定它停着、打进新的话之后，它的报告都属于新任务。记不下代次就不发
	gen, err := s.store.BumpTaskGen(x.Key)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "记不下子 agent 的任务代次，没有发：%v", err)
		return
	}
	err = s.agents.upstream.SendIfIdle(detached(r), permit, x.AgentDeckID, b.Text)
	if err == nil || agentdeck.IsDeliveryUnconfirmed(err) {
		// 发话返回：之后到的完成都属于新的一代（发的途中到的，按上一代有没有完成来归，见 deckstore EnqueueReport）
		if eerr := s.store.EndSend(x.Key); eerr != nil {
			s.store.Notify(x.Project, "report", "记不下发话已结束："+deckstore.Clean(eerr.Error(), 160), "deckd")
		}
	}
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"session": x.Key, "delivered": true})
	case agentdeck.IsDeliveryUnconfirmed(err):
		// 话可能已打进去、只是没证实提交（Codex 总是这样）：新的一代保留
		writeJSON(w, http.StatusAccepted, map[string]any{"session": x.Key, "delivered": false, "note": deckstore.Clean(err.Error(), 300)})
	default:
		// 没送成（还在跑，或上游投递失败）：撤回这一代。撤回前已经收到这一代的完成就不撤——那是上一件在这一刻做完的报告，
		// 照常转告；下次发话再开新的一代
		if _, rerr := s.store.RevertTaskGen(x.Key, gen); rerr != nil {
			s.store.Notify(x.Project, "report", "发话没送成后撤回子 agent 的任务代次失败："+deckstore.Clean(rerr.Error(), 160), "deckd")
		}
		if errors.Is(err, agentdeck.ErrTargetBusy) {
			fail(w, http.StatusConflict, "%s", busySubText)
			return
		}
		fail(w, http.StatusBadGateway, "发话失败：%v", err)
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
		s.store.FinishExecute(d.ID, deckstore.StateFailed, "记不下授权："+err.Error())
		return err
	}
	s.store.FinishExecute(d.ID, deckstore.StateApplied, "已记住授权")
	s.store.Notify(sc["project"], "trust", fmt.Sprintf("已记住 %s 在 %s 的 Codex 授权；可以重新开子 agent", sc["tool"], sc["dir"]), "owner")
	return nil
}

// projectctlGraphStart 是生产用的 Graph 通道开工：在项目的 Graph 根目录跑 projectctl start。
func projectctlGraphStart(script string) GraphStartFunc {
	return func(ctx context.Context, p deckstore.ProjectDef, node, actor, model string, route deckstore.Tier) (int, error) {
		c := projectctl.NewClient(p.GraphRoot, script)
		res, err := c.Start(ctx, projectctl.StartRequest{NodeID: node, Actor: actor, Role: "workbench_executor",
			RouteClass: string(route), Model: model, Reason: "Agent Deck 主 agent 派出"})
		if err != nil {
			return 0, err
		}
		return res.Attempt, nil
	}
}
