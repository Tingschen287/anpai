// Package deckd 是 Agent Deck 的后台服务（n050，架构稿 §3）：unix socket 上的 HTTP + JSON，另有一条 SSE。
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
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/dashquota"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/identity"
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
}

type paneResolver interface {
	ResolvePeer(ctx context.Context, peer identity.Peer) (identity.Pane, error)
}

// ProgressFunc 读一个项目的进度（只读）。
type ProgressFunc func(ctx context.Context, p deckstore.ProjectDef) (any, error)

// Server 是 deckd 的 HTTP 层。
type Server struct {
	cfg      Config
	store    *deckstore.Store
	resolver paneResolver
	bus      *bus
	quota    *dashquota.Client
	view     *viewstate.Machine
	progress ProgressFunc
	// 只在测试里设置：按请求决定调用者。生产路径没有任何方式设置它。
	callerFn func(*http.Request) Caller
	// activity 是 agent-deck 会话状态的缓存（n049，侧栏状态圆点）；没配上游地址时为 nil。
	activity *activityCache
	// agents 是开子 agent、发话、转告主 agent 需要的依赖（n051）；没接上时相关接口返回 503。
	agents *agentsDeps
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
	return mux
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
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, format string, a ...any) {
	writeJSON(w, code, apiError{Error: fmt.Sprintf(format, a...)})
}

// storeErr 把存储错误映射成状态码：存储已停止 → 503。
func storeErr(w http.ResponseWriter, err error) {
	var be *deckstore.ErrBroken
	var ve *deckstore.ErrVersion
	switch {
	case errors.As(err, &be):
		fail(w, http.StatusServiceUnavailable, "%v", err)
	case errors.As(err, &ve):
		fail(w, http.StatusConflict, "%v", err)
	default:
		fail(w, http.StatusBadRequest, "%v", err)
	}
}

func readBody(r *http.Request, v any) error {
	const max = 64 << 10
	data, err := io.ReadAll(io.LimitReader(r.Body, max+1))
	if err != nil {
		return err
	}
	if len(data) > max {
		return fmt.Errorf("请求体超过 %d 字节", max)
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("请求体在 JSON 对象之后还有内容")
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("请求体在 JSON 对象之后还有内容")
	}
	return nil
}

// need 检查角色。写操作另要求身份查询本身没有出错。
func need(w http.ResponseWriter, c Caller, write bool, roles ...string) bool {
	if write && c.Err != nil {
		fail(w, http.StatusForbidden, "认不出调用者（%v），拒绝写入", c.Err)
		return false
	}
	for _, r := range roles {
		if c.Role == r {
			return true
		}
	}
	fail(w, http.StatusForbidden, "%s 不能调用这个接口", roleName(c.Role))
	return false
}

func roleName(r string) string {
	switch r {
	case RoleOwner:
		return "owner 界面"
	case RoleMain:
		return "主 agent"
	case RoleSub:
		return "子 agent"
	}
	return "未知调用者"
}

// projectScope 决定本次请求能看哪个项目：owner 可指定任意项目（空 = 全部）；主/子 agent 只能是自己的项目。
func projectScope(w http.ResponseWriter, c Caller, asked string) (string, bool) {
	if c.Role == RoleOwner {
		return asked, true
	}
	if asked != "" && asked != c.Project {
		fail(w, http.StatusForbidden, "%s 不能访问其他项目 %s", roleName(c.Role), asked)
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
		fail(w, http.StatusServiceUnavailable, "没接 Token 看板")
		return
	}
	writeJSON(w, http.StatusOK, s.quota.All(r.Context()))
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
		fail(w, http.StatusServiceUnavailable, "布局还没接上（中间窗格由 deck up 建立）")
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
	p, found := s.store.Settings().Project(project)
	if !found {
		fail(w, http.StatusNotFound, "没有项目 %s", project)
		return
	}
	if s.progress == nil || p.GraphRoot == "" {
		writeJSON(w, http.StatusOK, map[string]any{"project": project, "graph": false, "note": "这个项目没有接 Graph"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, err := s.progress(ctx, p)
	if err != nil {
		fail(w, http.StatusBadGateway, "读项目进度失败：%v", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
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
		fail(w, http.StatusBadRequest, "请求体不对：%v", err)
		return
	}
	if !agentKinds[b.Kind] {
		fail(w, http.StatusBadRequest, "主 agent 只能上报 cost、config、push、disagree、deviation，不是 %q", b.Kind)
		return
	}
	d, err := s.store.CreateDecision(deckstore.NewDecision{Project: c.Project, Kind: b.Kind, Summary: b.Summary,
		Options: b.Options, Recommend: b.Recommend, Scope: b.Scope, CreatedBy: c.Session})
	if err != nil {
		storeErr(w, err)
		return
	}
	s.store.Notify(c.Project, "decision", "主 agent 有 1 件事要你决定："+d.Summary, c.Session)
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
	if err := readBody(r, &b); err != nil {
		fail(w, http.StatusBadRequest, "请求体不对：%v", err)
		return
	}
	d, err := s.store.Answer(r.PathValue("id"), b.Option, b.Version, "owner@"+c.Pane)
	if err != nil {
		storeErr(w, err)
		return
	}
	// Codex trust 批准即记住（§14.3，n051）；记不下就如实报出，决定转为 failed
	if err := s.executeCodexTrust(d); err != nil {
		fail(w, http.StatusServiceUnavailable, "已批准，但记不下授权：%v", err)
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
	var body struct {
		Agents   *[]deckstore.AgentDef   `json:"agents"`
		Projects *[]deckstore.ProjectDef `json:"projects"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, http.StatusBadRequest, "请求体不对：%v", err)
		return
	}
	if body.Agents == nil || body.Projects == nil {
		fail(w, http.StatusBadRequest, "整份替换设置须同时给出 agents 与 projects")
		return
	}
	next := deckstore.Settings{Agents: *body.Agents, Projects: *body.Projects}
	if err := s.store.ReplaceSettings(next); err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.store.Settings())
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
		fail(w, http.StatusNotFound, "没有项目 %s、工具 %s 的已记住授权", q.Get("project"), q.Get("tool"))
		return
	}
	s.store.Notify(q.Get("project"), "trust", "已撤销 "+q.Get("tool")+" 的已记住授权：下次启动会重新问你", "owner")
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
		fail(w, http.StatusBadRequest, "请求体不对：%v", err)
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
		fail(w, http.StatusServiceUnavailable, "布局还没接上（中间窗格由 deck up 建立）")
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
		fail(w, http.StatusBadRequest, "不认识的操作 %q", b.Op)
		return
	}
	if err != nil {
		var he *viewstate.ErrHandback
		if errors.As(err, &he) {
			// 操作本身已完成（例如已切走），但交还失败：会话保持接管、自动派送暂停
			s.bus.publish(deckstore.Event{Kind: "view"})
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "state": s.view.State(), "handback_failed": he.Session})
			return
		}
		if s.stopped() != nil {
			fail(w, http.StatusServiceUnavailable, "%v", err)
			return
		}
		if errors.Is(err, viewstate.ErrStaleClient) || errors.As(err, new(*viewstate.ErrPartial)) {
			// 中途停下：状态可能已部分提交（见 ErrPartial），面板要按最新状态重画
			s.bus.publish(deckstore.Event{Kind: "view"})
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "state": s.view.State()})
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
	On bool `json:"on"`
}

func (s *Server) setPanel(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, true, RoleOwner) {
		return
	}
	var b panelBody
	if err := readBody(r, &b); err != nil {
		fail(w, http.StatusBadRequest, "请求体不对：%v", err)
		return
	}
	if err := s.store.SetPanel(r.PathValue("name"), b.On); err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.store.Panels())
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
		fail(w, http.StatusInternalServerError, "不支持流式响应")
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
