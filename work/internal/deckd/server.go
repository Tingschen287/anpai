// Package deckd 是「安排 Anpai」的后台服务（n050，架构稿 §3）：unix socket 上的 HTTP + JSON，另有一条 SSE。
//
// 它是全部 deck 状态的唯一写入者，按调用者身份（identity 包）分配权限。界面（侧栏、标签条、面板）与主 agent 的
// deck agent 命令都通过它读写。Graph 写入边界不变：这里不调任何 projectctl 写命令。
package deckd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/dashquota"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/identity"
	"github.com/Tingschen287/terminal-deck/work/internal/sessionenv"
	"github.com/Tingschen287/terminal-deck/work/internal/viewstate"
)

// Config 是 deckd 的配置。
type Config struct {
	Dirs         deckstore.Dirs
	Profile      string // deck 使用的 agent-deck profile，默认 deck
	LayoutSocket string // 布局 tmux server 的 socket 路径
	AgentSocket  string // agent-deck tmux server 的 socket 路径
	DashboardURL string // Token 看板，默认 http://127.0.0.1:8899
	Projectctl   string // Graph 控制器脚本路径（进度面板只读用）
	// UpstreamAddr 是 agent-deck headless 服务地址（例如 127.0.0.1:8420），用来读会话运行状态；留空则状态一律 unknown。
	UpstreamAddr  string
	UpstreamToken string
	// SystemLang 是系统语言（zh 或 en）：只在全新安装、deckd 第一次创建 agents.yaml 时写进 ui_lang（n060 段 3 返工，owner 定：
	// 升级的老用户保持中文，全新安装才跟系统）。已有 agents.yaml 时它不起作用。
	// 由 deck up 按它自己（owner 的 shell）的环境变量算好，经 deck daemon 的 -system-lang 传进来；
	// 空（测试、没传）时全新安装也不记语言（界面按中文）。deckd 自己不换语言：给模型、落盘、日志的文字一直是中文，界面语言只经 LangHeader 告诉界面。
	SystemLang string
	// SessionEnv 解析「会话 PATH」（n060 段 4，见 internal/sessionenv）：deckd 找 agent CLI 的已装与版本、Codex trust 门记下的可执行文件、
	// 列模型、以及交给 agent-deck launch 的 PATH 都用它。nil（测试、没开）= 按 deckd 自己进程的 PATH，行为与以前一样。
	// deck daemon 在生产里开它；解析登录 shell 会运行 owner 的 shell 启动文件，所以测试默认不开。
	SessionEnv *sessionenv.Resolver
	// SessionPath 为真时 Run 自己建一个解析登录 shell 的 Resolver（SessionEnv 为 nil 才建）并在后台读一次，第一次打开设置时就不用等。
	// 之后只有 owner 在 F9 按「刷新会话 PATH」键才会再读（POST /v1/tools/refresh）；打开设置、进加入页和 agent 页、派工、列模型都只用当前快照，不会运行 rc。
	// 只有 deck daemon 设它。
	SessionPath bool
	// PanelApplier 把面板注册表的改动作用到正在运行的布局上（n069：F9 设置里挂上、拔掉面板，立即生效，不重建布局）。
	// nil（测试、布局没接上）= 只存注册表，下次建布局时才生效。deck daemon 在生产里设它。
	PanelApplier PanelApplier
}

// PanelApplier 让布局与面板注册表 defs 一致，返回新挂上与拔掉的面板名。调用方（deckd）保证同一时刻只有一次在跑，
// 并且 defs 是调用时刻存储里最新的注册表。出错时已做的部分保留。
// ErrLayoutNotRunning 是 PanelApplier 在布局 server 没在跑时返回的：不算出错，注册表已存、下次 deck up 建布局按它来；
// 回给界面的 applied 为 false 且没有说明，界面据此写「已保存，布局没在运行」，不说「布局已更新」。
var ErrLayoutNotRunning = errors.New("layout is not running")

type PanelApplier func(ctx context.Context, defs []deckstore.PanelDef) (attached, removed []string, err error)

type paneResolver interface {
	ResolvePeer(ctx context.Context, peer identity.Peer) (identity.Pane, error)
}

// ProgressFunc 读一个项目的进度（只读）。
type ProgressFunc func(ctx context.Context, p deckstore.ProjectDef) (any, error)

// Server 是 deckd 的 HTTP 层。
type Server struct {
	waitMu  sync.Mutex
	waiting map[string]int // 正在 deck agent wait 的主 agent（推送循环跳过它们）
	// sendLocks：给同一个子 agent 的发话一次只走一条（n051 attempt 3 自查：并发发话各自加代次、各自撤回，代次会错位）
	sendLocks sync.Map // 子会话 key → *sync.Mutex
	cfg       Config
	store     *deckstore.Store
	resolver  paneResolver
	bus       *bus
	quota     *dashquota.Client
	view      *viewstate.Machine
	progress  ProgressFunc
	panelMu   sync.Mutex // 挂拔面板：存注册表、改布局串行，两个窗口同时挂拔最后收敛到存储里的状态（n069）
	// 只在测试里设置：按请求决定调用者。生产路径没有任何方式设置它。
	callerFn func(*http.Request) Caller
	// activity 是 agent-deck 会话状态的缓存（n049，侧栏状态圆点）；没配上游地址时为 nil。
	activity *activityCache
	// agents 是开子 agent、发话、转告主 agent 需要的依赖（n051）；没接上时相关接口返回 503。
	agents *agentsDeps
	// bg 是后台工作的 context（n055：等 owner 处理 CLI 启动确认后再送 brief），deckd 关闭时取消
	bg   context.Context
	bgWG sync.WaitGroup
	// bgMu 护着 bg 和下面几项。life 是没接 WithBackground 时的后台 context；bgCancels 是 Close 要取消的全部；
	// closed 之后新造的后台 context 一出生就是取消的
	bgMu      sync.Mutex
	life      context.Context
	bgCancels []context.CancelFunc
	closed    bool
}

// NewServer 组装 HTTP 层。view 可为 nil（布局未接上时 /v1/view 返回 503）。
func NewServer(cfg Config, store *deckstore.Store, resolver paneResolver, quota *dashquota.Client, view *viewstate.Machine, progress ProgressFunc) *Server {
	s := &Server{cfg: cfg, store: store, resolver: resolver, bus: newBus(), quota: quota, view: view, progress: progress}
	store.OnChange(s.bus.publish)
	return s
}

// Handler 返回路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.health)
	mux.HandleFunc("GET /v1/lang", s.getLang)
	mux.HandleFunc("GET /v1/whoami", s.whoami)
	mux.HandleFunc("GET /v1/quota", s.getQuota)
	mux.HandleFunc("GET /v1/projects", s.getProjects)
	mux.HandleFunc("GET /v1/sessions", s.getSessions)
	mux.HandleFunc("GET /v1/snapshot", s.getSnapshot)
	mux.HandleFunc("GET /v1/view", s.getView)
	mux.HandleFunc("POST /v1/view", s.postView)
	mux.HandleFunc("GET /v1/decisions", s.getDecisions)
	mux.HandleFunc("POST /v1/decisions", s.postDecision)
	mux.HandleFunc("POST /v1/decisions/{id}/answer", s.answerDecision)
	mux.HandleFunc("POST /v1/decisions/{id}/cancel", s.cancelDecision)
	mux.HandleFunc("GET /v1/notifications", s.getNotifications)
	mux.HandleFunc("GET /v1/settings", s.getSettings)
	mux.HandleFunc("PUT /v1/settings", s.putSettings)
	mux.HandleFunc("GET /v1/resolve", s.getResolve)
	mux.HandleFunc("GET /v1/tools", s.getTools)
	mux.HandleFunc("GET /v1/tools/models", s.getToolModels)
	mux.HandleFunc("GET /v1/tools/refresh", s.getToolsRefresh)
	mux.HandleFunc("POST /v1/tools/refresh", s.postToolsRefresh) // 刷新会话 PATH（会运行登录启动文件），n060 段 4 返工
	mux.HandleFunc("GET /v1/trust", s.getTrust)
	mux.HandleFunc("DELETE /v1/trust", s.deleteTrust)
	mux.HandleFunc("GET /v1/progress", s.getProgress)
	mux.HandleFunc("GET /v1/panels", s.getPanels)
	mux.HandleFunc("POST /v1/panels/{name}", s.setPanel)
	mux.HandleFunc("GET /v1/events", s.events)
	mux.HandleFunc("POST /v1/agents", s.postAgents)
	mux.HandleFunc("GET /v1/agents/{id}/output", s.getAgentOutput)
	mux.HandleFunc("POST /v1/agents/{id}/send", s.postAgentSend)
	mux.HandleFunc("POST /v1/projects/{id}/main", s.postMain)
	mux.HandleFunc("POST /v1/reports", s.postReport)
	mux.HandleFunc("GET /v1/reports/wait", s.getReportsWait)
	mux.HandleFunc("POST /v1/reports/ack", s.postReportsAck)
	mux.HandleFunc("POST /v1/reports/deliver-now", s.postReportsDeliverNow)
	return markFilter{next: mux, lang: s.uiLang}
}

// uiLang 是此刻的界面语言：设置里的 ui_lang；没有就是中文。
// 没有 ui_lang 只会是升级的老用户（agents.yaml 在这次启动前就存在，且没有这个字段），和升级前一样用中文；
// 全新安装在第一次创建 agents.yaml 时已经把系统语言（Config.SystemLang）写进了 ui_lang（deckstore.LoadSettingsFor），所以不再有「没选过就按系统」这一档。
// 每个响应都带它（markFilter 写 i18n.LangHeader），界面进程据此换语言。
func (s *Server) uiLang() string {
	if l := s.store.UILang(); i18n.ValidLang(l) {
		return l
	}
	return i18n.ZH
}

// getLang 回此刻的界面语言，以及设置里有没有 ui_lang（saved：全新安装第一次创建文件时写进去的也算；没有的是升级的老用户，中文）。界面进程启动时用它先对一次语言，不必等第一个业务请求。
func (s *Server) getLang(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"lang": s.uiLang(), "saved": i18n.ValidLang(s.store.UILang())})
}

// ---------------- 通用 ----------------

func (s *Server) caller(r *http.Request) Caller {
	if s.callerFn != nil {
		return s.callerFn(r)
	}
	return s.resolve(r.Context())
}

type apiError struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
	Args  []any  `json:"args,omitempty"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// withNote 在响应里留下中文兜底，并带上码。英文界面用 note_code，不把整句中文当参数。
func withNote(body map[string]any, err error, max int) map[string]any {
	if body == nil {
		body = map[string]any{}
	}
	if err == nil {
		return body
	}
	body["note"] = deckstore.Clean(err.Error(), max)
	if c, a, ok := i18n.Wire(err); ok {
		body["note_code"] = c
		body["note_args"] = deckstore.CleanArgs(a, max)
	}
	return body
}

// writeErr 回一条错误。error 字段仍是中文原文。能取出码时再带 code 和 args。
func writeErr(w http.ResponseWriter, code int, err error) {
	body := apiError{Error: err.Error()}
	if c, args, ok := i18n.Wire(err); ok {
		body.Code = c
		body.Args = args
	}
	writeJSON(w, code, body)
}

// errFields 在手写 JSON 里带上码。error 仍是中文原文。
func errFields(err error, extra map[string]any) map[string]any {
	body := map[string]any{"error": err.Error()}
	if c, args, ok := i18n.Wire(err); ok {
		body["code"] = c
		body["args"] = args
	}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func fail(w http.ResponseWriter, code int, format string, a ...any) {
	if len(a) == 1 {
		if err, ok := a[0].(error); ok && (format == "%v" || format == "%s" || format == "%w") {
			writeErr(w, code, err)
			return
		}
	}
	writeErr(w, code, fmt.Errorf(format, a...))
}

// storeErr 把存储错误映射成状态码：存储已停止 → 503。
func storeErr(w http.ResponseWriter, err error) {
	var be *deckstore.ErrBroken
	var ve *deckstore.ErrVersion
	switch {
	case errors.As(err, &be):
		writeErr(w, http.StatusServiceUnavailable, err)
	case errors.As(err, &ve):
		writeErr(w, http.StatusConflict, err)
	default:
		writeErr(w, http.StatusBadRequest, err)
	}
}

func readBody(r *http.Request, v any) error {
	if err := readBodyRaw(r, v); err != nil {
		return err
	}
	// 请求体是外部原文的入口：里面伪造的打码标记在这里换掉，后面进通知参数时才分得清真假（返工 4）
	neutralizeValue(v)
	return nil
}

// readBodyRaw 只解码、不中和标记。只有带指纹的请求用它（开子 agent：指纹是客户端对原文算的），
// 用的地方要自己在核对完指纹后立刻 neutralizeValue。
func readBodyRaw(r *http.Request, v any) error {
	const max = 64 << 10
	data, err := io.ReadAll(io.LimitReader(r.Body, max+1))
	if err != nil {
		return err
	}
	if len(data) > max {
		return i18n.E("err.deckd.readbody.1", max)
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return i18n.E("err.deckd.readbody.2")
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return i18n.E("err.deckd.readbody.2")
	}
	return nil
}

// need 检查角色。写操作另要求身份查询本身没有出错。
func need(w http.ResponseWriter, c Caller, write bool, roles ...string) bool {
	if write && c.Err != nil {
		writeErr(w, http.StatusForbidden, i18n.E("err.deckd.need.1", c.Err))
		return false
	}
	for _, r := range roles {
		if c.Role == r {
			return true
		}
	}
	writeErr(w, http.StatusForbidden, i18n.E("err.deckd.need.2", roleName(c.Role)))
	return false
}

func roleName(r string) *i18n.Error {
	switch r {
	case RoleOwner:
		return i18n.E("err.deckd.rolename.1")
	case RoleMain:
		return i18n.E("err.deckd.serverrunspawn.49")
	case RoleSub:
		return i18n.E("err.deckd.serverrunspawn.50")
	}
	return i18n.E("err.deckd.rolename.2")
}

// projectScope 决定本次请求能看哪个项目：owner 可指定任意项目（空 = 全部）；主/子 agent 只能是自己的项目。
func projectScope(w http.ResponseWriter, c Caller, asked string) (string, bool) {
	if c.Role == RoleOwner {
		return asked, true
	}
	if asked != "" && asked != c.Project {
		writeErr(w, http.StatusForbidden, i18n.E("err.deckd.projectscope.1", roleName(c.Role), asked))
		return "", false
	}
	return c.Project, true
}

// stopped 汇总 deck 存储与写入权记账（经中间窗格状态机）的停止原因（n050 评审 R3）。
func (s *Server) stopped() error {
	if err := s.store.Broken(); err != nil {
		return err
	}
	if s.view != nil {
		return s.view.Stopped()
	}
	return nil
}

// ---------------- 只读 ----------------

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"ok": true, "event_id": s.bus.Current()}
	if err := s.stopped(); err != nil {
		out["ok"], out["broken"] = false, err.Error()
	}
	if e := s.activity.Err(); e != "" {
		out["activity_error"] = deckstore.Clean(e, 300) // 会话状态读不到的原因（侧栏圆点显示「未知」时看这里）
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	out := map[string]any{"role": c.Role, "project": c.Project, "session": c.Session}
	if c.Err != nil {
		out["error"] = c.Err.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getQuota(w http.ResponseWriter, r *http.Request) {
	if s.quota == nil {
		writeErr(w, http.StatusServiceUnavailable, i18n.E("err.deckd.servergetquota.1"))
		return
	}
	writeJSON(w, http.StatusOK, s.quota.Display(r.Context())) // 面板用：OpenCode 的估算窗口只在这里补（n056）
}

func (s *Server) getProjects(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, false, RoleOwner, RoleMain, RoleSub) {
		return
	}
	set := s.store.Settings()
	var out []deckstore.ProjectDef
	for _, p := range set.Projects {
		if c.Role == RoleOwner || p.ID == c.Project {
			out = append(out, p)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getSessions(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, false, RoleOwner, RoleMain, RoleSub) {
		return
	}
	project, ok := projectScope(w, c, r.URL.Query().Get("project"))
	if !ok {
		return
	}
	list := s.store.Sessions(project)
	if c.Role == RoleSub {
		var self []deckstore.Session
		for _, x := range list {
			if x.Key == c.Session {
				self = append(self, x)
			}
		}
		list = self
	}
	writeJSON(w, http.StatusOK, s.withActivity(list))
}

// SessionView 是会话加上 agent-deck 报告的运行状态（running、waiting、idle、error、unknown）。
type SessionView struct {
	deckstore.Session
	Activity string `json:"activity"`
}

func (s *Server) withActivity(list []deckstore.Session) []SessionView {
	out := make([]SessionView, 0, len(list))
	for _, x := range list {
		out = append(out, SessionView{Session: x, Activity: s.activity.of(x.TmuxSession)})
	}
	return out
}

// Snapshot 是界面与主 agent 重连后取的全量；EventID 之后的事件从 SSE 续接。
type Snapshot struct {
	EventID       string                   `json:"event_id"`
	Sessions      []deckstore.Session      `json:"sessions"`
	Decisions     []deckstore.Decision     `json:"decisions"`
	Notifications []deckstore.Notification `json:"notifications"`
	View          *viewstate.State         `json:"view,omitempty"`
}

func (s *Server) getSnapshot(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, false, RoleOwner, RoleMain) {
		return
	}
	project, ok := projectScope(w, c, r.URL.Query().Get("project"))
	if !ok {
		return
	}
	// 先取事件 id 再取数据：数据只会比 id 新，续接时多收的事件是幂等刷新，不会漏。
	snap := Snapshot{EventID: s.bus.Current(), Sessions: s.store.Sessions(project), Decisions: s.store.Decisions(project)}
	notes := s.store.Notifications(0, project)
	if len(notes) > 100 {
		notes = notes[len(notes)-100:]
	}
	snap.Notifications = notes
	if s.view != nil && c.Role == RoleOwner {
		st := s.view.State()
		snap.View = &st
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) getDecisions(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, false, RoleOwner, RoleMain) {
		return
	}
	project, ok := projectScope(w, c, r.URL.Query().Get("project"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.store.Decisions(project))
}

func (s *Server) getNotifications(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, false, RoleOwner, RoleMain) {
		return
	}
	project, ok := projectScope(w, c, r.URL.Query().Get("project"))
	if !ok {
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	writeJSON(w, http.StatusOK, s.store.Notifications(after, project))
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, false, RoleOwner, RoleMain) {
		return
	}
	set := s.store.Settings()
	if c.Role == RoleMain {
		var mine []deckstore.ProjectDef
		for _, p := range set.Projects {
			if p.ID == c.Project {
				mine = append(mine, p)
			}
		}
		set.Projects = mine
	}
	writeJSON(w, http.StatusOK, set)
}

func (s *Server) getTrust(w http.ResponseWriter, r *http.Request) {
	if !need(w, s.caller(r), false, RoleOwner) {
		return
	}
	writeJSON(w, http.StatusOK, s.store.TrustGrants())
}

func (s *Server) getView(w http.ResponseWriter, r *http.Request) {
	if !need(w, s.caller(r), false, RoleOwner) {
		return
	}
	if s.view == nil {
		writeErr(w, http.StatusServiceUnavailable, i18n.E("err.deckd.servergetview.1"))
		return
	}
	writeJSON(w, http.StatusOK, s.view.State())
}

func (s *Server) getProgress(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, false, RoleOwner, RoleMain) {
		return
	}
	project, ok := projectScope(w, c, r.URL.Query().Get("project"))
	if !ok {
		return
	}
	if project == "" && c.Role == RoleOwner {
		// owner 没指定项目：跟着中间窗格正在看的会话所属的项目（n069）。没有可跟的就如实说原因，不当错误
		var reason string
		if project, reason = s.followedProject(); project == "" {
			writeJSON(w, http.StatusOK, map[string]any{"project": "", "graph": false, "follow": true, "reason": reason})
			return
		}
	}
	p, found := s.store.Settings().Project(project)
	if !found {
		writeErr(w, http.StatusNotFound, i18n.E("err.deckd.servergetprogress.1", project))
		return
	}
	if s.progress == nil || p.GraphRoot == "" {
		writeJSON(w, http.StatusOK, withNote(map[string]any{"project": project, "graph": false}, i18n.E("err.deckd.servergetprogress.3"), 300))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, err := s.progress(ctx, p)
	if err != nil {
		writeErr(w, http.StatusBadGateway, i18n.E("err.deckd.servergetprogress.2", err))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// followedProject 是中间窗格正在看的会话所属的项目（n069，进度面板没指定项目时跟它）。
// 取不到时返回空项目与原因：no_session（没有在看的会话）、no_project（会话不属于设置里的项目）。
func (s *Server) followedProject() (project, reason string) {
	if s.view == nil {
		return "", "no_session"
	}
	focus := s.view.State().Focus
	if focus == "" || focus == viewstate.Idle {
		return "", "no_session"
	}
	// focus 是状态机里的 agent-deck id：先按它精确找，找不到再退回 Session（key 或 id 都认）。
	// 只调 Session(focus) 会让「另一个会话的 key 恰好等于这个 agent-deck id」时解析到错的会话（先按 key 匹配）。
	var sess deckstore.Session
	ok := false
	for _, x := range s.store.Sessions("") {
		if x.AgentDeckID == focus {
			sess, ok = x, true
			break
		}
	}
	if !ok {
		sess, ok = s.store.Session(focus)
	}
	if !ok {
		return "", "no_session"
	}
	if _, found := s.store.Settings().Project(sess.Project); sess.Project == "" || !found {
		return "", "no_project"
	}
	return sess.Project, ""
}

// ---------------- 写 ----------------

// agentKinds 是主 agent 可以上报的五类（codex_trust、graph_gate、session_wait 由系统生成）。
var agentKinds = map[string]bool{deckstore.KindCost: true, deckstore.KindConfig: true, deckstore.KindPush: true,
	deckstore.KindDisagree: true, deckstore.KindDeviation: true}

type postDecisionBody struct {
	Kind      string            `json:"kind"`
	Summary   string            `json:"summary"`
	Options   []string          `json:"options"`
	Recommend string            `json:"recommend"`
	Scope     map[string]string `json:"scope"`
}

func (s *Server) postDecision(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, true, RoleMain) {
		return
	}
	var b postDecisionBody
	if err := readBody(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, i18n.E("err.deckd.serverpostagents.2", err))
		return
	}
	if !agentKinds[b.Kind] {
		writeErr(w, http.StatusBadRequest, i18n.E("err.deckd.serverpostdecision.1", b.Kind))
		return
	}
	// 开子 agent 的超额例外与 Codex trust 的批准范围只能由 deckd 在开子 agent 时生成（n051 自查）：
	// 主 agent 自己写的话，owner 在面板上只看到说明，批准的却是一个藏在范围里的启动
	if a := b.Scope["action"]; a == "spawn" || a == "codex_trust" {
		writeErr(w, http.StatusBadRequest, i18n.E("err.deckd.serverpostdecision.2", a))
		return
	}
	d, err := s.store.CreateDecision(deckstore.NewDecision{Project: c.Project, Kind: b.Kind, Summary: b.Summary,
		Options: b.Options, Recommend: b.Recommend, Scope: b.Scope, CreatedBy: c.Session})
	if err != nil {
		storeErr(w, err)
		return
	}
	s.store.NotifyCoded(c.Project, "decision", i18n.E("err.deckd.serverpostdecision.3", d.Summary), c.Session)
	writeJSON(w, http.StatusCreated, d)
}

type answerBody struct {
	Option  string `json:"option"`
	Version int    `json:"version"`
}

func (s *Server) answerDecision(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, true, RoleOwner) {
		return
	}
	var b answerBody
	// 只有 owner 的界面能答。它回传的 option 是界面显示过的选项原文，存下来的选项可能带真标记，
	// 只拿来和存下来的选项比较，所以不在这里中和（中和了就对不上选项）。别的请求体一律 readBody 中和（返工 4）。
	// 这个例外只限比较：比较失败时 Store.Answer 在 option 进错误参数之前先中和它（段 3，复审 4 必改 2）。
	if err := readBodyRaw(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, i18n.E("err.deckd.serverpostagents.2", err))
		return
	}
	d, err := s.store.Answer(r.PathValue("id"), b.Option, b.Version, "owner@"+c.Pane)
	if err != nil {
		storeErr(w, err)
		return
	}
	// Codex trust 批准即记住（§14.3，n051）；记不下就如实报出，决定转为 failed
	if err := s.executeCodexTrust(d); err != nil {
		writeErr(w, http.StatusServiceUnavailable, i18n.E("err.deckd.serveranswerdecision.1", err))
		return
	}
	if got, ok := s.store.Decision(d.ID); ok {
		d = got
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) cancelDecision(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, true, RoleMain) {
		return
	}
	if err := s.store.Cancel(r.PathValue("id"), c.Session); err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, true, RoleOwner) {
		return
	}
	// 整份替换：agents 与 projects 都必须显式给出（可以是空列表），缺字段不当成「清空」——
	// 否则一个 {} 就把全部设置抹掉了（n050 第 3 次实现补权限矩阵时发现）
	// generation：读到的代数，不是当前的就 409（n057：两个设置窗口、或界面与 deck project add 不互相覆盖）
	var body struct {
		Agents     *[]deckstore.AgentDef   `json:"agents"`
		Projects   *[]deckstore.ProjectDef `json:"projects"`
		Generation *int                    `json:"generation"`
		// UILang：界面语言。不带这个字段（旧客户端、deck project add）表示不动已存的语言，不是「清空」；带 "" 才是清掉已存的选择（之后和老用户一样是中文）
		UILang *string `json:"ui_lang"`
	}
	if err := readBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, i18n.E("err.deckd.serverpostagents.2", err))
		return
	}
	if body.Agents == nil || body.Projects == nil {
		writeErr(w, http.StatusBadRequest, i18n.E("err.deckd.serverputsettings.1"))
		return
	}
	if body.Generation == nil || *body.Generation < 0 {
		writeErr(w, http.StatusBadRequest, i18n.E("err.deckd.serverputsettings.2"))
		return
	}
	next := deckstore.Settings{Agents: *body.Agents, Projects: *body.Projects, UILang: s.store.UILang()}
	if body.UILang != nil {
		next.UILang = *body.UILang
	}
	if err := checkAgentEdits(s.store.Settings(), next); err != nil {
		fail(w, http.StatusBadRequest, "%v", err)
		return
	}
	if err := next.CheckModelIDs(); err != nil {
		fail(w, http.StatusBadRequest, "%v", err)
		return
	}
	if err := next.CheckMainAgents(); err != nil {
		fail(w, http.StatusBadRequest, "%v", err)
		return
	}
	gen, err := s.store.ReplaceSettingsAtGen(next, *body.Generation)
	if err != nil {
		var changed *deckstore.ErrSettingsChanged
		if errors.As(err, &changed) {
			fail(w, http.StatusConflict, "%v", err)
			return
		}
		storeErr(w, err)
		return
	}
	// 回的是这次存下的那份与它的代数（n057 rev 127：不是保存后再读的当前设置，那可能已是另一个窗口紧接着存的）
	next.Generation = gen
	writeJSON(w, http.StatusOK, next)
}

func (s *Server) deleteTrust(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, true, RoleOwner) {
		return
	}
	q := r.URL.Query()
	found, err := s.store.RevokeTrust(q.Get("project"), q.Get("tool"))
	if err != nil {
		storeErr(w, err)
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, i18n.E("err.deckd.serverdeletetrust.1", q.Get("project"), q.Get("tool")))
		return
	}
	s.store.NotifyCoded(q.Get("project"), "trust", i18n.E("err.deckd.serverdeletetrust.2", q.Get("tool")), "owner")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type viewBody struct {
	Op      string `json:"op"` // open、focus、hide、takeover、handback（重试失败的交还）、resync、reset（中间 client 重建）、fence
	Session string `json:"session"`
	Client  int    `json:"client"` // resync、reset：发通知的中间 client 进程号（不是现在那个就拒绝）
}

func (s *Server) postView(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, true, RoleOwner) {
		return
	}
	var b viewBody
	if err := readBody(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, i18n.E("err.deckd.serverpostagents.2", err))
		return
	}
	if b.Op == "fence" {
		// 中间 client 退出后、新 client 连上前（deck center 调）：等正在进行的视图操作做完再回（n049 评审 2 B1）。
		// 排在「布局没接上」「存储已停」之前：那两种情况也必须先等进行中的操作，不能提前回答
		if s.view != nil {
			s.view.Fence()
		}
		writeJSON(w, http.StatusOK, map[string]any{"fenced": true})
		return
	}
	if s.view == nil {
		writeErr(w, http.StatusServiceUnavailable, i18n.E("err.deckd.servergetview.1"))
		return
	}
	if err := s.stopped(); err != nil {
		fail(w, http.StatusServiceUnavailable, "%v", err)
		return
	}
	var err error
	switch b.Op {
	case "open":
		err = s.view.Open(b.Session)
	case "focus":
		err = s.view.Focus(b.Session)
	case "hide":
		err = s.view.Hide(b.Session)
	case "takeover":
		err = s.view.Takeover(b.Session)
	case "handback":
		err = s.view.RetryHandback(b.Session)
	case "resync":
		// 中间 client 新建后（deck center 首次连上）：按当前焦点重做一次切换，不隐藏任何会话（n049）
		err = s.view.Resync(b.Client)
	case "reset":
		// 中间 client 断开（Ctrl-Q）后、新 client 以只读连上时：焦点会话按隐藏处理（架构稿 §1、§7）
		err = s.view.CenterReset(b.Client)
	default:
		writeErr(w, http.StatusBadRequest, i18n.E("err.deckd.serverpostview.1", b.Op))
		return
	}
	if err != nil {
		var he *viewstate.ErrHandback
		if errors.As(err, &he) {
			// 操作本身已完成（例如已切走），但交还失败：会话保持接管、自动派送暂停
			s.bus.publish(deckstore.Event{Kind: "view"})
			writeJSON(w, http.StatusConflict, errFields(err, map[string]any{"state": s.view.State(), "handback_failed": he.Session}))
			return
		}
		if s.stopped() != nil {
			fail(w, http.StatusServiceUnavailable, "%v", err)
			return
		}
		if errors.Is(err, viewstate.ErrStaleClient) || errors.As(err, new(*viewstate.ErrPartial)) {
			// 中途停下：状态可能已部分提交（见 ErrPartial），面板要按最新状态重画
			s.bus.publish(deckstore.Event{Kind: "view"})
			writeJSON(w, http.StatusConflict, errFields(err, map[string]any{"state": s.view.State()}))
			return
		}
		fail(w, http.StatusConflict, "%v", err)
		return
	}
	s.bus.publish(deckstore.Event{Kind: "view"})
	writeJSON(w, http.StatusOK, s.view.State())
}

// ---------------- 面板注册表 ----------------

func (s *Server) getPanels(w http.ResponseWriter, r *http.Request) {
	if !need(w, s.caller(r), false, RoleOwner) {
		return
	}
	writeJSON(w, http.StatusOK, s.store.Panels())
}

type panelBody struct {
	On *bool `json:"on"` // 必填：缺了或为 null 不能当成 false 把面板拔掉
}

func (s *Server) setPanel(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, true, RoleOwner) {
		return
	}
	var b panelBody
	if err := readBody(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, i18n.E("err.deckd.serverpostagents.2", err))
		return
	}
	if b.On == nil {
		writeErr(w, http.StatusBadRequest, i18n.E("err.deckd.serverpostagents.2", errors.New("missing field on")))
		return
	}
	// 存与改布局放在同一把锁里：改布局时读的是此刻存储里最新的注册表，两个窗口同时挂拔不会各按各的旧值改
	s.panelMu.Lock()
	defer s.panelMu.Unlock()
	if err := s.store.SetPanel(r.PathValue("name"), *b.On); err != nil {
		storeErr(w, err)
		return
	}
	defs := s.store.Panels()
	body := map[string]any{"panels": defs, "applied": false, "attached": []string{}, "removed": []string{}}
	if s.cfg.PanelApplier != nil {
		// 注册表已存下：让布局收敛不能因为客户端断开（F9 里按了空格就退出）而半途取消，所以与请求的生命周期脱钩
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
		defer cancel()
		attached, removed, err := s.cfg.PanelApplier(ctx, defs)
		if attached != nil {
			body["attached"] = attached
		}
		if removed != nil {
			body["removed"] = removed
		}
		// 已存下的不回滚：注册表是 owner 的意愿，布局没跟上只如实说一声（界面显示原因；下次建布局会按注册表来）
		if errors.Is(err, ErrLayoutNotRunning) {
			err = nil
			body["applied"] = false
		} else {
			body["applied"] = err == nil
		}
		if err != nil {
			withNote(body, i18n.E("err.deckd.setpanel.apply", err), 300)
		}
	}
	writeJSON(w, http.StatusOK, body)
}

// ---------------- SSE ----------------

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, false, RoleOwner, RoleMain) {
		return
	}
	project, ok := projectScope(w, c, r.URL.Query().Get("project"))
	if !ok {
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, i18n.E("err.deckd.serverevents.1"))
		return
	}
	last := r.Header.Get("Last-Event-ID")
	if last == "" {
		last = r.URL.Query().Get("after")
	}
	replay, resync, sub := s.bus.subscribe(last, project)
	defer s.bus.unsubscribe(sub)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	send := func(e busEvent) {
		data, _ := json.Marshal(e.Event)
		fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", s.bus.ID(e.Seq), e.Event.Kind, data)
	}
	if resync {
		fmt.Fprintf(w, "id: %s\nevent: resync\ndata: {}\n\n", s.bus.Current())
	}
	for _, e := range replay {
		send(e)
	}
	fl.Flush()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, open := <-sub.ch:
			if !open {
				return // 跟不上被断开：客户端重连后 resync
			}
			send(e)
			fl.Flush()
		case <-tick.C:
			fmt.Fprint(w, ": keepalive\n\n")
			fl.Flush()
		}
	}
}
