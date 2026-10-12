package deckd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
	"github.com/Tingschen287/terminal-deck/work/internal/viewstate"
)

// n051：子 agent 完成后自动转告主 agent（架构稿 §14.4）。
//
// 怎么判断「做完了」：
//   - 主要信号：子 agent 收尾时运行 deck agent report --done（POST /v1/reports，标「主动报告」）；
//   - Graph 通道：账本显示节点已提交，送一条「账本提醒」，同一 节点#attempt 只送一次。它也只是提醒、不算完成
//     （owner 在 n051 attempt 4 设计复审选 B2-A）；
//   - 兜底：agent-deck 状态从 running 变为 idle 或 waiting 并保持 inferAfter，送一条「推断提醒」。它只是提醒主 agent 子 agent 停下了
//     （可能做完，也可能在等输入或权限确认；agent-deck 里做完一轮的会话没人看过就一直是 waiting），不算完成：
//     之后真正的完成照常转告（owner 在 n051 attempt 2 设计评审选 A）。
//
// 怎么送进主 agent：送达工作循环每 reportEvery 跑一次，经中间窗格状态机的送达门（DeliverToMain）：
// 主 agent 正显示在中间、owner 30 秒内在它那里打过字就先排队（通知「N 条汇报等着转告主 agent」）；
// 送的那一刻先关中间输入并确认，送完再按 brief 状态放开。用 agent-deck 的 --defer-if-busy：主 agent 正在回答就等它这一轮结束，
// 等不到就下次再送（上游确认什么都没送）。同一个子 agent 的同一次完成只送一次；送达未确认按现有规则标出，不自动重发。
// 没有接上中间窗格（deck up 之外起的 deckd）时直接送。

const (
	reportEvery = 3 * time.Second
	inferAfter  = 60 * time.Second
)

type reportBody struct {
	Done bool   `json:"done"`
	Text string `json:"text"`
}

// postReport：子 agent 汇报。--done 进转告队列；不带 --done 的只记进通知栏。
func (s *Server) postReport(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, true, RoleSub) {
		return
	}
	if err := s.stopped(); err != nil {
		fail(w, http.StatusServiceUnavailable, "%v", err)
		return
	}
	var b reportBody
	if err := readBody(r, &b); err != nil {
		fail(w, http.StatusBadRequest, "请求体不对：%v", err)
		return
	}
	if strings.TrimSpace(b.Text) == "" {
		fail(w, http.StatusBadRequest, "汇报是空的")
		return
	}
	x, ok := s.store.Session(c.Session)
	if !ok {
		fail(w, http.StatusForbidden, "认不出这个子 agent")
		return
	}
	if !b.Done {
		n, err := s.store.Notify(x.Project, "report", fmt.Sprintf("子 agent「%s」：%s", x.Title, b.Text), x.Key)
		if err != nil {
			fail(w, http.StatusServiceUnavailable, "%v", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"queued": false, "notification": n})
		return
	}
	// 同一次完成只送一次（n051 评审 1 B4）：完成编号是子 agent 的任务代次，不是正文。改写正文再主动报告，同一代只转告一次；
	// 主 agent 发了新的话（代次加 1）之后的完成才算新的一次。推断提醒与账本提醒都不占完成（B2-A）
	// 完成编号留空：存储在锁里按子 agent 当前的代次给（与发话的加代次、撤回在同一把锁里，attempt 3 自查 S2）
	rep, added, err := s.store.EnqueueReport(deckstore.Report{Sub: x.Key, Source: deckstore.ReportActive, Text: b.Text})
	if err != nil {
		storeErr(w, err)
		return
	}
	// 子 agent 自己说做完了：不管是不是同一次完成的重复，都标 done、释放名额（attempt 2 自查：撞上已送的推断时以前会一直占着）
	if x.Status == deckstore.StatusRunning {
		s.store.SetSessionStatus(x.Key, deckstore.StatusDone, "子 agent 报告已完成")
	}
	if !added && rep.Text != deckstore.Clean(b.Text, 2000) {
		// 同一次完成的补充说明：不再转告主 agent，记进通知栏，不悄悄丢
		s.store.Notify(x.Project, "report", fmt.Sprintf("子 agent「%s」补充（同一次完成，已转告过或正在等送，不再转告）：%s", x.Title, deckstore.Clean(b.Text, 300)), x.Key)
	}
	writeJSON(w, http.StatusOK, map[string]any{"queued": true, "report": rep, "duplicate": !added})
}

// reportLoop 的推断状态：每个子 agent 上一次看到的状态，以及从哪一刻起不在跑。
type inferState struct {
	mu        sync.Mutex
	wasBusy   map[string]bool      // 子会话 key → 曾经在跑
	quietFrom map[string]time.Time // 子会话 key → 从 running 转为 idle/waiting 的时刻
	notified  map[string]int       // 主会话 key → 已通知过「等着转告」的条数（避免刷屏）
	lastGraph time.Time            // 上一次查账本（账本提醒）的时刻
	graphBusy bool                 // 正在后台查账本（一次 projectctl brief 要两三秒，不挡推送）
	graphWG   sync.WaitGroup       // 后台查账本的 goroutine：循环退出前等它结束
	startedAt time.Time            // 第一次推断的时刻：在这之前就有的子 agent，重启后没见过它在跑也照常计时
	seen      map[string]bool      // 子会话 key → 本进程已经看过它的状态
	quietGen  map[string]int       // 子会话 key → 这段安静开始时的任务代次（提醒按它编号）
}

func newInferState() *inferState {
	return &inferState{wasBusy: map[string]bool{}, quietFrom: map[string]time.Time{}, notified: map[string]int{}}
}

// runReports 是汇报的推断与送达循环（deckd 运行期间常驻）。
func (s *Server) runReports(ctx context.Context) {
	st := newInferState()
	defer st.graphWG.Wait()
	t := time.NewTicker(reportEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.inferCompletions(st, time.Now())
			s.graphSubmitsAsync(ctx, st, time.Now())
			s.expireClaims(time.Now())
			s.deliverReports(ctx, st)
		}
	}
}

// inferCompletions：子 agent 从 running 转为 idle 或 waiting 并保持 inferAfter，送一条「推断提醒」（每段安静一条）。
// 提醒不占完成：这一代已经有主动报告、或这段安静开始之后到过主动报告或账本提醒，就不提醒；提醒之后真正的完成照常转告（attempt 2 设计评审 A）。
// 已标 done 的子 agent 也看：主 agent 给它发了新的话（新的一代）、它干完又停下却没报告，同样提醒。
func (s *Server) inferCompletions(st *inferState, now time.Time) {
	if s.activity == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.startedAt.IsZero() {
		st.startedAt = now
	}
	if st.seen == nil {
		st.seen = map[string]bool{}
	}
	if st.quietGen == nil {
		st.quietGen = map[string]int{}
	}
	for _, x := range s.store.Sessions("") {
		if x.Role != "sub" || x.Parent == "" || x.Status != deckstore.StatusRunning && x.Status != deckstore.StatusDone || x.TmuxSession == "" {
			continue
		}
		if !st.seen[x.Key] {
			// deckd 重启：重启前就开的子 agent 第一次被看到时可能已经停着（等权限确认，或做完没报告）。没见过它在跑也当作刚停下，
			// 从这一刻计时；这一代已报告过的入队时被拒（attempt 2 自查）
			st.seen[x.Key] = true
			if x.CreatedAt.Before(st.startedAt) {
				st.wasBusy[x.Key] = true
			}
		}
		switch s.activity.of(x.TmuxSession) {
		case "running":
			st.wasBusy[x.Key] = true
			delete(st.quietFrom, x.Key)
		case "idle", "waiting":
			if !st.wasBusy[x.Key] {
				continue
			}
			from, ok := st.quietFrom[x.Key]
			if !ok || st.quietGen[x.Key] != x.Gen() {
				// 安静段从这一刻算，记下当时的代次；安静中主 agent 又发了话（代次变了），重新计时（attempt 3 自查：
				// 提醒按新代次编号会绕过「这一代已有完成就不提醒」，把发话前那段安静错报给主 agent）
				st.quietFrom[x.Key], st.quietGen[x.Key] = now, x.Gen()
				continue
			}
			if now.Sub(from) < inferAfter {
				continue
			}
			st.wasBusy[x.Key] = false
			delete(st.quietFrom, x.Key)
			// 这一代已经主动报告过、或安静开始之后到过主动报告或账本提醒的，入队时在存储的锁里拒收（与后台账本、主动报告并发也不漏）
			// 提醒有自己的编号（代次 + 这段安静开始的时刻）：不占这一代的完成
			s.store.EnqueueReport(deckstore.Report{Sub: x.Key, Source: deckstore.ReportInferred,
				CompletionID: deckstore.HintID(st.quietGen[x.Key], from),
				Text: fmt.Sprintf("推断：子 agent「%s」停下了 %d 秒（agent-deck 状态从 running 变为 %s），没有运行 deck agent report --done。可能已做完，也可能在等输入或权限确认。"+
					"这只是提醒、不算完成：它之后真正的完成仍会转告。请用 deck agent output %s 查看。",
					x.Title, int(inferAfter.Seconds()), s.activity.of(x.TmuxSession), x.Key)})
		}
	}
}

// deliverReports 把各主 agent 等着的汇报送出去。
func (s *Server) deliverReports(ctx context.Context, st *inferState) {
	if s.agents == nil || s.stopped() != nil {
		return
	}
	s.deliverPending(ctx, st, "", false)
}

// deliverPending 把等送的汇报送给主 agent；project 为空表示全部项目；now 为真表示 owner 按键要求立刻送（跳过打字门）。
// 返回送达的条数。
func (s *Server) deliverPending(ctx context.Context, st *inferState, project string, now bool) int {
	sent := 0
	for _, main := range s.store.Sessions(project) {
		if main.Role != "main" || main.AgentDeckID == "" || main.Status != deckstore.StatusRunning {
			continue
		}
		if s.waitingFor(main.Key) {
			// 主 agent 正在 deck agent wait（n051 增量自查）：它这时算忙，推送会把汇报占在 sending 里一轮又一轮，wait 就取不到；让 wait 取
			continue
		}
		pending := s.store.PendingReports(main.Key)
		for _, rep := range pending {
			err := s.deliverOne(ctx, main, rep, now)
			if errors.Is(err, viewstate.ErrOwnerTyping) {
				// owner 正在主 agent 那里打字：先排队，通知一次（条数变了再通知）
				st.mu.Lock()
				if st.notified[main.Key] != len(pending) {
					st.notified[main.Key] = len(pending)
					s.store.Notify(main.Project, "report", fmt.Sprintf("%d 条汇报等着转告主 agent（你停止输入 30 秒后自动送，或按 F8 立刻送）", len(pending)), "deckd")
				}
				st.mu.Unlock()
				break
			}
			if err != nil {
				break // 主 agent 正在回答或暂时送不了：下一轮再试，后面的也等着（保持先后）
			}
			sent++ // 送出（送达，或送达未确认、已通知 owner）
		}
		if len(s.store.PendingReports(main.Key)) == 0 {
			st.mu.Lock()
			delete(st.notified, main.Key)
			st.mu.Unlock()
		}
	}
	return sent
}

// reportText 是转告给主 agent 的文字（推送与 deck agent wait 共用），标明主动报告、推断提醒或账本提醒。
func (s *Server) reportText(rep deckstore.Report) string {
	sub, _ := s.store.Session(rep.Sub)
	label := "主动报告"
	if rep.Source == deckstore.ReportInferred {
		label = "推断提醒"
	} else if rep.Source == deckstore.ReportGraph {
		label = "账本提醒"
	}
	return fmt.Sprintf("［Agent Deck 转告·%s］子 agent「%s」（%s）：\n%s", label, sub.Title, sub.Key, rep.Text)
}

// maxWait 是 deck agent wait 一次最多等多久。
const maxWait = 10 * time.Minute

// getReportsWait：主 agent 自己等汇报（GET /v1/reports/wait?timeout=秒）。n051 Codex 端到端实测：Codex 开完子 agent 不结束这一轮，
// 用 sleep 反复等，一直忙，推送循环（只在主 agent 空闲时送）就送不进去。拿到的汇报算送达，推送循环不再送。
func (s *Server) getReportsWait(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, false, RoleMain) {
		return
	}
	d := 2 * time.Minute
	if v := r.URL.Query().Get("timeout"); v != "" {
		sec, err := strconv.Atoi(v)
		if err != nil || sec < 0 {
			fail(w, http.StatusBadRequest, "timeout 要写秒数")
			return
		}
		d = time.Duration(sec) * time.Second
	}
	if d > maxWait {
		d = maxWait
	}
	deadline := time.Now().Add(d)
	s.markWaiting(c.Session, 1)
	defer s.markWaiting(c.Session, -1)
	for {
		got, err := s.store.TakeReports(c.Session)
		if err != nil {
			storeErr(w, err)
			return
		}
		if len(got) > 0 {
			out := make([]map[string]string, 0, len(got))
			ids := make([]string, 0, len(got))
			for _, rep := range got {
				out = append(out, map[string]string{"id": rep.ID, "sub": rep.Sub, "source": rep.Source, "text": s.reportText(rep)})
				ids = append(ids, rep.ID)
			}
			body, _ := json.Marshal(map[string]any{"reports": out, "ack": "收到后用 POST /v1/reports/ack 确认（deck agent wait 会自动确认）"})
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write(append(body, '\n')); err != nil {
				// 回应没写出去：主 agent 没看到，退回等送（n051 评审 1 B5）
				s.store.ReleaseClaims(c.Session, ids)
			}
			return
		}
		if !time.Now().Before(deadline) {
			writeJSON(w, http.StatusOK, map[string]any{"reports": []any{}, "timeout": true})
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(waitPoll):
		}
	}
}

func (s *Server) markWaiting(main string, d int) {
	s.waitMu.Lock()
	defer s.waitMu.Unlock()
	if s.waiting == nil {
		s.waiting = map[string]int{}
	}
	s.waiting[main] += d
	if s.waiting[main] <= 0 {
		delete(s.waiting, main)
	}
}

func (s *Server) waitingFor(main string) bool {
	s.waitMu.Lock()
	defer s.waitMu.Unlock()
	return s.waiting[main] > 0
}

// graphEvery 是查 Graph 节点提交信号的间隔（一次 projectctl brief 要两三秒，不必每 3 秒查）。
var graphEvery = 30 * time.Second

// graphSubmitsAsync 在后台查账本（上一次还没查完就跳过）。
func (s *Server) graphSubmitsAsync(ctx context.Context, st *inferState, now time.Time) {
	st.mu.Lock()
	if st.graphBusy {
		st.mu.Unlock()
		return
	}
	st.graphBusy = true
	st.mu.Unlock()
	st.graphWG.Add(1)
	go func() {
		defer st.graphWG.Done()
		defer func() { st.mu.Lock(); st.graphBusy = false; st.mu.Unlock() }()
		s.graphSubmits(ctx, st, now)
	}()
}

// graphSubmits：走 Graph 通道、还在跑的子 agent，账本显示它的节点已提交（submit 之后的 submitted、accepted、rework），
// 就送一条「账本提醒」（§14.4 的第三条信号，n051 评审 1 B6）。attempt 4 起它只是提醒（B2-A）：不占完成、与任务代次无关；
// 按 节点#attempt 去重：同一轮提交只提醒一次，主 agent 之后发了新的话、子 agent 又被看成在跑也不重报。
func (s *Server) graphSubmits(ctx context.Context, st *inferState, now time.Time) {
	if s.agents == nil || s.agents.graphState == nil {
		return
	}
	st.mu.Lock()
	due := now.Sub(st.lastGraph) >= graphEvery
	if due {
		st.lastGraph = now
	}
	st.mu.Unlock()
	if !due {
		return
	}
	set := s.store.Settings()
	for _, x := range s.store.Sessions("") {
		if x.Role != "sub" || x.Status != deckstore.StatusRunning || x.Graph == nil || x.Graph.Attempt < 1 {
			continue
		}
		p, ok := set.Project(x.Project)
		if !ok || p.GraphRoot == "" {
			continue
		}
		state, err := s.agents.graphState(ctx, p, x.Graph.Node)
		if err != nil {
			continue
		}
		switch state {
		case projectctl.StateSubmitted, projectctl.StateAccepted, projectctl.StateRework:
		default:
			continue
		}
		if cur, ok := s.store.Session(x.Key); !ok || cur.Status != deckstore.StatusRunning {
			// 查询途中子 agent 已经主动报告（标了 done）：真正的完成已经转告，不再送账本提醒（attempt 4 自查：提醒会让主 agent 以为还要等）
			continue
		}
		// 账本提醒（owner 在 attempt 4 设计复审选 B2-A）：不占完成，按 节点#attempt 只送一次
		_, added, err := s.store.EnqueueReport(deckstore.Report{Sub: x.Key, Source: deckstore.ReportGraph,
			Ref: fmt.Sprintf("%s#%d", x.Graph.Node, x.Graph.Attempt),
			Text: fmt.Sprintf("账本显示 Graph 节点 %s 第 %d 轮已提交（现在是 %s）。这只是提醒、不算完成：子 agent 运行 deck agent report --done 的主动报告才算完成，到了仍会转告。需要时用 deck agent output %s 查看。",
				x.Graph.Node, x.Graph.Attempt, state, x.Key)})
		if err == nil && added {
			s.store.SetSessionStatus(x.Key, deckstore.StatusDone, "账本显示已提交")
		}
	}
}

// postReportsDeliverNow：owner 按键要求把排队的汇报立刻送出（POST /v1/reports/deliver-now {"project":…}，§14.4 第 2 条）。
func (s *Server) postReportsDeliverNow(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, true, RoleOwner) {
		return
	}
	if s.agents == nil {
		fail(w, http.StatusServiceUnavailable, "deckd 没接上 agent-deck")
		return
	}
	var b struct {
		Project string `json:"project"`
	}
	if err := readBody(r, &b); err != nil {
		fail(w, http.StatusBadRequest, "请求体不对：%v", err)
		return
	}
	sent := s.deliverPending(detached(r), newInferState(), b.Project, true)
	left := 0
	for _, x := range s.store.Sessions(b.Project) {
		if x.Role == "main" {
			left += len(s.store.PendingReports(x.Key))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": sent, "pending": left})
}

// postReportsAck：主 agent 确认收到 deck agent wait 领走的汇报（POST /v1/reports/ack {"ids":[…]}）。
func (s *Server) postReportsAck(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, true, RoleMain) {
		return
	}
	var b struct {
		IDs []string `json:"ids"`
	}
	if err := readBody(r, &b); err != nil || len(b.IDs) == 0 {
		fail(w, http.StatusBadRequest, "要给 ids")
		return
	}
	n, err := s.store.AckReports(c.Session, b.IDs)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"acked": n})
}

// claimTimeout 是 deck agent wait 领走后等确认的上限；过了就转为未确认并通知 owner（测试可改）。
var claimTimeout = 2 * time.Minute

// expireClaims：领走后久不确认的汇报转为未确认（不重发），通知 owner。
func (s *Server) expireClaims(now time.Time) {
	reps, err := s.store.ExpireClaims(now.Add(-claimTimeout))
	if err != nil {
		return
	}
	for _, rep := range reps {
		sub, _ := s.store.Session(rep.Sub)
		s.store.Notify(rep.Project, "report", fmt.Sprintf("主 agent 用 deck agent wait 领走的汇报（%s）没有确认收到，送没送到不确定、不会重发，请在主 agent 那里核对：%s",
			sub.Title, deckstore.Clean(rep.Text, 160)), "deckd")
	}
}

// waitPoll 是 deck agent wait 查一次的间隔（测试可改）。
var waitPoll = time.Second

// deliverOne 送一条汇报。返回 nil 表示这条已处理完（送达或标为未确认）；返回错误表示没送、保持 pending。
func (s *Server) deliverOne(ctx context.Context, main deckstore.Session, rep deckstore.Report, now bool) error {
	sub, _ := s.store.Session(rep.Sub)
	text := s.reportText(rep)
	send := func() error {
		permit, err := s.agents.leases.Manual(main.AgentDeckID)
		if err != nil {
			return err
		}
		// sending 先落盘：送出去但结果没落盘时，重启会把它标成未确认，不重发
		if err := s.store.MarkReport(rep.ID, deckstore.ReportSending); err != nil {
			return err
		}
		// 标 sending 之前的一刻可能被主动报告换了正文（推断 → 主动），按最新的送
		if cur, ok := s.store.Report(rep.ID); ok {
			text = s.reportText(cur)
		}
		err = s.agents.upstream.SendToMain(ctx, permit, main.AgentDeckID, text)
		switch {
		case err == nil:
			return s.store.MarkReport(rep.ID, deckstore.ReportDelivered)
		case errors.Is(err, agentdeck.ErrTargetBusy):
			s.store.MarkReport(rep.ID, deckstore.ReportPending) // 上游确认什么都没送
			return err
		default:
			// 送达未确认或失败：不自动重发，标出来交给 owner
			s.store.MarkReport(rep.ID, deckstore.ReportUnconfirmed)
			s.store.Notify(main.Project, "report", fmt.Sprintf("转告主 agent 的汇报送达未确认（%s）：%s", sub.Title, deckstore.Clean(err.Error(), 200)), "deckd")
			return nil
		}
	}
	if s.view == nil {
		return send()
	}
	if now {
		return s.view.DeliverToMainNow(main.AgentDeckID, send)
	}
	return s.view.DeliverToMain(main.AgentDeckID, send)
}
