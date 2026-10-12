package deckd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/viewstate"
)

// n051：子 agent 完成后自动转告主 agent（架构稿 §14.4）。
//
// 怎么判断「做完了」：
//   - 主要信号：子 agent 收尾时运行 deck agent report --done（POST /v1/reports，标「主动报告」）；
//   - 兜底：agent-deck 状态从 running 变为 idle 或 waiting 并保持 inferAfter，标「推断」。
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
	// 同一次完成只送一次：完成编号取汇报正文的指纹，子 agent 重复运行同一条 report --done 不会重复转告
	rep, added, err := s.store.EnqueueReport(deckstore.Report{Sub: x.Key, Source: deckstore.ReportActive,
		CompletionID: "active:" + TaskSHA256(b.Text)[:16], Text: b.Text})
	if err != nil {
		storeErr(w, err)
		return
	}
	if added {
		s.store.SetSessionStatus(x.Key, deckstore.StatusDone, "子 agent 报告已完成")
	}
	writeJSON(w, http.StatusOK, map[string]any{"queued": true, "report": rep, "duplicate": !added})
}

// reportLoop 的推断状态：每个子 agent 上一次看到的状态，以及从哪一刻起不在跑。
type inferState struct {
	mu        sync.Mutex
	wasBusy   map[string]bool      // 子会话 key → 曾经在跑
	quietFrom map[string]time.Time // 子会话 key → 从 running 转为 idle/waiting 的时刻
	notified  map[string]int       // 主会话 key → 已通知过「等着转告」的条数（避免刷屏）
}

// runReports 是汇报的推断与送达循环（deckd 运行期间常驻）。
func (s *Server) runReports(ctx context.Context) {
	st := &inferState{wasBusy: map[string]bool{}, quietFrom: map[string]time.Time{}, notified: map[string]int{}}
	t := time.NewTicker(reportEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.inferCompletions(st, time.Now())
			s.deliverReports(ctx, st)
		}
	}
}

// inferCompletions：子 agent 从 running 转为 idle/waiting 并保持 inferAfter，按「推断」入队。
func (s *Server) inferCompletions(st *inferState, now time.Time) {
	if s.activity == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, x := range s.store.Sessions("") {
		if x.Role != "sub" || x.Parent == "" || x.Status != deckstore.StatusRunning || x.TmuxSession == "" {
			continue
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
			if !ok {
				st.quietFrom[x.Key] = now
				continue
			}
			if now.Sub(from) < inferAfter {
				continue
			}
			// 这一段「在跑 → 安静」只推断一次：完成编号取转为安静的时刻
			s.store.EnqueueReport(deckstore.Report{Sub: x.Key, Source: deckstore.ReportInferred,
				CompletionID: fmt.Sprintf("inferred:%d", from.Unix()),
				Text: fmt.Sprintf("子 agent「%s」可能已完成（推断：agent-deck 状态从 running 变为空闲并保持 %d 秒；它没有运行 deck agent report --done）。需要时用 deck agent output %s 查看。",
					x.Title, int(inferAfter.Seconds()), x.Key)})
			st.wasBusy[x.Key] = false
			delete(st.quietFrom, x.Key)
		}
	}
}

// deliverReports 把各主 agent 等着的汇报送出去。
func (s *Server) deliverReports(ctx context.Context, st *inferState) {
	if s.agents == nil || s.stopped() != nil {
		return
	}
	for _, main := range s.store.Sessions("") {
		if main.Role != "main" || main.AgentDeckID == "" || main.Status != deckstore.StatusRunning {
			continue
		}
		pending := s.store.PendingReports(main.Key)
		for _, rep := range pending {
			err := s.deliverOne(ctx, main, rep)
			if errors.Is(err, viewstate.ErrOwnerTyping) {
				// owner 正在主 agent 那里打字：先排队，通知一次（条数变了再通知）
				st.mu.Lock()
				if st.notified[main.Key] != len(pending) {
					st.notified[main.Key] = len(pending)
					s.store.Notify(main.Project, "report", fmt.Sprintf("%d 条汇报等着转告主 agent（你停止输入 30 秒后自动送）", len(pending)), "deckd")
				}
				st.mu.Unlock()
				break
			}
			if err != nil {
				break // 主 agent 正在回答或暂时送不了：下一轮再试，后面的也等着（保持先后）
			}
		}
		if len(s.store.PendingReports(main.Key)) == 0 {
			st.mu.Lock()
			delete(st.notified, main.Key)
			st.mu.Unlock()
		}
	}
}

// deliverOne 送一条汇报。返回 nil 表示这条已处理完（送达或标为未确认）；返回错误表示没送、保持 pending。
// reportText 是转告给主 agent 的文字（推送与 deck agent wait 共用），标明主动报告、推断或账本提交。
func (s *Server) reportText(rep deckstore.Report) string {
	sub, _ := s.store.Session(rep.Sub)
	label := "主动报告"
	if rep.Source == deckstore.ReportInferred {
		label = "推断"
	} else if rep.Source == deckstore.ReportGraph {
		label = "账本提交"
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
	d := 5 * time.Minute
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
	for {
		got, err := s.store.TakeReports(c.Session)
		if err != nil {
			storeErr(w, err)
			return
		}
		if len(got) > 0 {
			out := make([]map[string]string, 0, len(got))
			for _, rep := range got {
				out = append(out, map[string]string{"id": rep.ID, "sub": rep.Sub, "source": rep.Source, "text": s.reportText(rep)})
			}
			writeJSON(w, http.StatusOK, map[string]any{"reports": out})
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

// waitPoll 是 deck agent wait 查一次的间隔（测试可改）。
var waitPoll = time.Second

func (s *Server) deliverOne(ctx context.Context, main deckstore.Session, rep deckstore.Report) error {
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
	return s.view.DeliverToMain(main.AgentDeckID, send)
}
