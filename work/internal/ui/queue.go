package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tingschen287/terminal-deck/work/internal/queue"
)

// queueInterval 是运行时待决事项的轮询间隔。
//
// 上游 events/ 目录在 v1.15.0 没有生产代码写入，拿它当刷新信号等不到任何信号，
// 所以按固定间隔轮询 writer-status 与 export。两条都是本地 CLI，一轮不到一秒。
const queueInterval = 15 * time.Second

// queueWiring 是决策队列用到的外部能力。store 为 nil 表示没接队列。
type queueWiring struct {
	// err 非空表示队列文件打不开（例如损坏）。这时队列不可用，但界面必须常驻告警，
	// 不能因为没接上就一声不吭。
	err   string
	store *queue.Store
	// up 为 nil 表示没配置 upstream agent-deck：队列里只有 Graph 的门，并且要明说。
	up     queue.Upstream
	del    queue.Deliverer
	script string
}

// queueView 是队列在界面上的状态。
type queueView struct {
	intake queue.Intake
	// polled 为 false 表示运行时来源还一次都没取过。
	polled bool
	show   bool
	cursor int
	// pending 是按了数字、等 y 确认的那个选项。
	pendingID     string
	pendingOption string
	busy          bool
}

// WithQueueError 记下队列打不开的原因：界面常驻告警，按 i 也只看到这句。
func (m Model) WithQueueError(err error) Model {
	m.qw = queueWiring{err: err.Error()}
	return m
}

// WithQueue 给界面接上决策队列。
func (m Model) WithQueue(store *queue.Store, up queue.Upstream, del queue.Deliverer, script string) Model {
	m.qw = queueWiring{store: store, up: up, del: del, script: script}
	return m
}

type queuePolledMsg struct {
	intake  queue.Intake
	records int
	err     error
}

type queueAnsweredMsg struct {
	item queue.Item
	err  error
}

type queueTickMsg time.Time

func queueTick() tea.Cmd {
	return tea.Tick(queueInterval, func(t time.Time) tea.Msg { return queueTickMsg(t) })
}

// pollQueue 先自检再读 export，把会话条目并进队列。
func (m Model) pollQueue() tea.Cmd {
	if m.qw.store == nil {
		return nil
	}
	store, up := m.qw.store, m.qw.up
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		now := time.Now()
		in, recs := queue.Poll(ctx, up, now)
		msg := queuePolledMsg{intake: in, records: len(recs)}
		// 读取失败时不合并：那一轮什么都不知道，不能把已有条目标成「已不在」。
		if in.Configured && in.ExportErr == "" {
			if _, err := store.Merge(queue.SourceSession, queue.FromInbox(recs, now), now); err != nil {
				msg.err = err
			}
		}
		return msg
	}
}

// mergeGraph 把当前快照里的 Graph 门并进队列。判断在 queue.MergeGraph 里，与 --once 共用。
func (m Model) mergeGraph() string {
	if m.qw.store == nil {
		return ""
	}
	var b queue.Bindings
	if m.bindings != nil {
		b = m.bindings
	}
	if _, _, err := queue.MergeGraph(m.qw.store, m.snap, b, m.qw.script, time.Now()); err != nil {
		return "决策队列落盘失败：" + err.Error()
	}
	return ""
}

// queueAlert 是必须常驻显示的告警。没接队列时不说话。
func (m Model) queueAlert() string {
	if m.qw.err != "" {
		return "决策队列打不开（" + m.qw.err + "）：待决事项一律看不到，不能当作没有"
	}
	if m.qw.store == nil {
		return ""
	}
	if !m.qv.polled {
		return "运行时待决事项还没取过（正在自检 notify-daemon），现在的队列不完整"
	}
	return m.qv.intake.Alert()
}

func (m Model) onQueueKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.qw.store == nil {
		// 队列打不开：视图里只有告警，只认返回键。
		switch msg.String() {
		case "i", "esc", "q":
			m.qv.show = false
		}
		return m, nil
	}
	items := m.qw.store.Items()
	if m.qv.pendingID != "" {
		switch msg.String() {
		case "y":
			id, opt := m.qv.pendingID, m.qv.pendingOption
			m.qv.pendingID, m.qv.pendingOption = "", ""
			m.qv.busy = true
			m.notice, m.noticeOK = "正在处理回答…", true
			store, del := m.qw.store, m.qw.del
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				it, err := queue.Respond(ctx, store, del, id, opt, time.Now())
				return queueAnsweredMsg{item: it, err: err}
			}
		default:
			m.qv.pendingID, m.qv.pendingOption = "", ""
			m.notice, m.noticeOK = "已取消，没有投递任何东西", true
			return m, nil
		}
	}
	switch msg.String() {
	case "i", "esc", "q":
		m.qv.show = false
		return m, nil
	case "R", "K":
		// 给一次中断的投递下结论。两个键都不发消息：R 只清标记，要再发得重新选选项并按 y。
		if m.qv.cursor >= len(items) {
			return m, nil
		}
		it := items[m.qv.cursor]
		if it.Answer == nil || !it.Answer.InFlight {
			m.notice, m.noticeOK = "这一条没有待核的投递，R/K 只用于结案中断或送达未确认的投递", false
			return m, nil
		}
		received := msg.String() == "K"
		if _, err := m.qw.store.ResolveInFlight(it.ID, received, time.Now()); err != nil {
			m.notice, m.noticeOK = "没记上："+err.Error(), false
			return m, nil
		}
		if received {
			m.notice, m.noticeOK = "已记为对方收到，这一条标为已回答", true
		} else {
			m.notice, m.noticeOK = "已清掉中断标记，这一条回到待拍板；要再发，重新按数字选选项并按 y", true
		}
		return m, nil
	case "up", "k":
		if m.qv.cursor > 0 {
			m.qv.cursor--
		}
		return m, nil
	case "down", "j":
		if m.qv.cursor < len(items)-1 {
			m.qv.cursor++
		}
		return m, nil
	case "r":
		return m, m.pollQueue()
	}
	if n, err := strconv.Atoi(msg.String()); err == nil && n >= 1 && n <= 9 {
		if m.qv.busy {
			m.notice, m.noticeOK = "上一条回答还在处理，稍等", false
			return m, nil
		}
		if m.qv.cursor >= len(items) {
			return m, nil
		}
		it := items[m.qv.cursor]
		if it.Source.Kind == queue.SourceSession && it.Status == queue.StatusAnswered {
			m.notice, m.noticeOK = "这一条已经回答过了，不再投递", false
			return m, nil
		}
		if it.Answer != nil && it.Answer.InFlight {
			why := "上次投递在途中中断，结果未知"
			if it.Answer.Note != "" {
				why = it.Answer.Note
			}
			m.notice, m.noticeOK = why+"；为免对方收到两遍不自动重投。先回主界面用 p 预览那个会话，再按 K（已收到）或 R（没收到，可重答）", false
			return m, nil
		}
		if it.Status == queue.StatusResolved {
			m.notice, m.noticeOK = "这一条来源那头已经不等了，不用再回答", false
			return m, nil
		}
		if n > len(it.Options) {
			m.notice, m.noticeOK = fmt.Sprintf("这一条只有 %d 个选项", len(it.Options)), false
			return m, nil
		}
		opt := it.Options[n-1]
		m.qv.pendingID, m.qv.pendingOption = it.ID, opt.Key
		m.notice, m.noticeOK = confirmText(it, opt), true
		return m, nil
	}
	return m, nil
}

// confirmText 说清楚按 y 之后会发生什么。投递是往别人的会话里敲字，撤不回来。
func confirmText(it queue.Item, opt queue.Option) string {
	switch {
	case it.Source.Kind == queue.SourceSession && opt.Message != "":
		return fmt.Sprintf("将向会话 %s 发送「%s」。按 y 确认，其他键取消", it.Source.SessionID, opt.Message)
	case opt.Command != "":
		return "Work Deck 不写 Graph 账本，只会记下你的选择并给出命令：" + opt.Command + "。按 y 确认，其他键取消"
	}
	return "将只记下「" + opt.Label + "」，不给任何会话发消息。按 y 确认，其他键取消"
}

// QueueScreen 是队列视图要画的内容。
type QueueScreen struct {
	Show   bool
	Items  []queue.Item
	Cursor int
	Alert  string
	// Healthy 是来源可信时的依据，Foreign 是没收进队列的记录说明。
	Healthy string
	Foreign string
}

func (m Model) queueScreen() QueueScreen {
	if m.qw.store == nil {
		return QueueScreen{Show: m.qv.show, Alert: m.queueAlert()}
	}
	q := QueueScreen{Show: m.qv.show, Items: m.qw.store.Items(), Cursor: m.qv.cursor, Alert: m.queueAlert()}
	if m.qv.polled {
		q.Foreign = m.qv.intake.ForeignNote()
		if q.Alert == "" {
			q.Healthy = m.qv.intake.Healthy()
		}
	}
	return q
}

// QueueSnapshot 是 --once 一次性渲染用的队列内容：不开界面，也要把告警与条数画出来。
func QueueSnapshot(store *queue.Store, in queue.Intake, openErr error) (QueueScreen, int) {
	if openErr != nil {
		return QueueScreen{Alert: "决策队列打不开（" + openErr.Error() + "）：待决事项一律看不到，不能当作没有"}, 0
	}
	q := QueueScreen{Items: store.Items(), Alert: in.Alert(), Foreign: in.ForeignNote(), Healthy: in.Healthy()}
	return q, store.OpenCount()
}

// renderQueueSummary 是主界面上常驻的一行：几条待决，以及来源有没有问题。
func renderQueueSummary(q QueueScreen, openCount int, w func(string, ...any)) {
	if q.Alert != "" {
		w("%s", styleAlert.Render("决策队列告警："+q.Alert))
	}
	switch {
	case openCount > 0:
		w("%s", styleUnknown.Render(fmt.Sprintf("决策队列：%d 条待你拍板（按 i 打开）", openCount)))
	case q.Alert != "" || q.Healthy == "":
		w("%s", styleDim.Render("决策队列暂无条目——但来源不可信，这不代表没有待决事项（按 i 打开）"))
	default:
		// 即使各项自检都过，也不说「为空」：上游心跳不带进程号，确认不了在跑的 daemon
		// 写的就是当前数据目录（本目录的 daemon 刚退出、别的目录的 daemon 在跑时，自检照样全过）。
		w("%s", styleAlert.Render(unconfirmedEmpty(q.Healthy)))
	}
	if q.Foreign != "" {
		w("%s", styleDim.Render(q.Foreign))
	}
}

// renderQueuePanel 画队列视图：列表加选中条目的详情。
func renderQueuePanel(q QueueScreen, width int, w func(string, ...any)) {
	w("%s", styleSection.Render("决策队列"))
	if q.Alert != "" {
		w("%s", styleAlert.Render("告警："+q.Alert))
	}
	if q.Foreign != "" {
		w("%s", styleDim.Render(q.Foreign))
	}
	if len(q.Items) == 0 {
		if q.Alert != "" || q.Healthy == "" {
			w("%s", styleAlert.Render("队列里暂无条目，但来源不可信，不能当作没有待决事项"))
		} else {
			w("%s", styleAlert.Render(unconfirmedEmpty(q.Healthy)))
		}
		w("%s", styleDim.Render("i/Esc 返回　r 立即重取"))
		return
	}
	for i, it := range q.Items {
		mark := "  "
		if i == q.Cursor {
			mark = "▸ "
		}
		line := fmt.Sprintf("%s%-8s %-6s %-19s %s", mark, statusLabel(it.Status), it.Risk, string(it.Category), it.Title)
		switch {
		case it.Status == queue.StatusOpen && (it.Risk == queue.RiskHigh || it.Risk == queue.RiskCritical):
			w("%s", styleAlert.Render(line))
		case it.Status == queue.StatusOpen:
			w("%s", styleUnknown.Render(line))
		default:
			w("%s", styleDim.Render(line))
		}
	}
	if q.Cursor < len(q.Items) {
		it := q.Items[q.Cursor]
		w("")
		for _, ln := range wrapText(it.Detail, width-4) {
			w("  %s", ln)
		}
		for i, o := range it.Options {
			rec := ""
			if o.Key == it.Recommendation.Option {
				rec = "　← 推荐（置信度 " + it.Recommendation.Confidence + "）"
			}
			w("  %d. %s%s", i+1, o.Label, rec)
		}
		if it.Recommendation.Option == "" {
			w("%s", styleDim.Render("  不给推荐："+it.Recommendation.Basis))
		} else {
			w("%s", styleDim.Render("  推荐依据："+it.Recommendation.Basis))
		}
		if len(it.BlockingSessions) > 0 {
			w("%s", styleDim.Render("  卡住的会话："+strings.Join(it.BlockingSessions, "、")))
		}
		if len(it.EvidenceRefs) > 0 {
			w("%s", styleDim.Render("  证据："+strings.Join(it.EvidenceRefs, "；")))
		}
		if it.Deadline != nil {
			w("%s", styleUnknown.Render("  截止："+it.Deadline.Local().Format("01-02 15:04")))
		} else {
			w("%s", styleDim.Render("  截止：来源没有给"))
		}
		if it.Source.Kind == queue.SourceSession && !it.Source.Present {
			w("%s", styleDim.Render("  上游记录已不在 export 里（可能被 conductor 取走或过期清掉）"))
		}
		if a := it.Answer; a != nil {
			if a.InFlight && a.Note != "" {
				w("%s", styleAlert.Render("  结果待核："+a.Note+"：不自动重投。预览那个会话后按 K（已收到）或 R（没收到，清标记可重答）"))
			} else if a.InFlight {
				w("%s", styleAlert.Render("  上次投递在途中中断，结果未知：不自动重投。预览那个会话后按 K（已收到）或 R（没收到，清标记可重答）"))
			} else if a.DeliveryError != "" {
				w("%s", styleAlert.Render("  上次回答投递失败："+a.DeliveryError+"（可以重选）"))
			} else if a.Note != "" {
				w("%s", styleDim.Render("  已回答："+a.Note))
			}
		}
	}
	w("%s", styleDim.Render("↑↓ 选　数字 选选项（再按 y 确认）　R/K 结案中断的投递　r 立即重取　i/Esc 返回"))
}

// unconfirmedEmpty 是自检全过、队列里又没有条目时的说法。Work Deck 从不说「队列为空」。
func unconfirmedEmpty(evidence string) string {
	return "决策队列暂无条目，但无法确认当前数据目录的 notify-daemon 在跑（上游心跳不带进程号；" +
		evidence + "）：空不代表没有"
}

func statusLabel(s string) string {
	switch s {
	case queue.StatusOpen:
		return "待拍板"
	case queue.StatusAnswered:
		return "已回答"
	}
	return "已不等"
}

func (m Model) queueOn() bool { return m.qw.store != nil || m.qw.err != "" }

func (m Model) queueOpen() int {
	if m.qw.store == nil {
		return 0
	}
	return m.qw.store.OpenCount()
}
