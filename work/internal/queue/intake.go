package queue

import (
	"context"
	"fmt"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
)

// Upstream 是取运行时待决事项要用的上游读取，由 adapter 包实现。
type Upstream interface {
	InboxWriterStatus(ctx context.Context) (agentdeck.WriterStatus, error)
	// NotifyDaemonRunning 看进程表：writer-status 只看心跳，daemon 刚退出的 90 秒里仍答 running。
	NotifyDaemonRunning(ctx context.Context) (bool, error)
	InboxExport(ctx context.Context) ([]agentdeck.InboxRecord, error)
	// ProfileName 是本客户端的 profile。export 是全机的，只收这个 profile 的记录：
	// 别的 profile 的会话，回答投递不过去（session send 只在当前 profile 里找会话）。
	ProfileName() string
}

// Deliverer 把回答投递回发起会话，由 adapter 包实现。
type Deliverer interface {
	DeliverAnswer(ctx context.Context, rec agentdeck.InboxRecord, message string) error
}

// Intake 是一轮取数的体检结果。
//
// 它存在的理由：inbox export 返回空数组有三种可能——真没有、notify-daemon 没在记录、
// 读取本身失败。三种看起来一模一样。队列为空只有在第一种情况下才能这么说。
type Intake struct {
	CheckedAt time.Time
	// Configured 为 false 表示没配置 upstream agent-deck，根本没去取。
	Configured bool
	Writer     agentdeck.WriterStatus
	WriterErr  string
	// ProcessUp 是进程表里有没有 notify-daemon；ProcessErr 是查进程表本身失败。
	ProcessUp  bool
	ProcessErr string
	ExportErr  string
	// Records 是收进队列的本 profile 记录数；Foreign 是别的 profile 或没写 profile、没收进来的记录数。
	Records int
	Foreign int
	Profile string
}

// Poll 先自检 notify-daemon，再非消费式地读一次 export。
//
// 自检失败或 daemon 不在时仍然读 export：export 不消费任何东西，daemon 停之前
// 记下的记录照样有用。但结果带着告警，界面必须把告警显示出来。
func Poll(ctx context.Context, up Upstream, now time.Time) (Intake, []agentdeck.InboxRecord) {
	in := Intake{CheckedAt: now, Configured: up != nil}
	if up == nil {
		return in, nil
	}
	in.Profile = up.ProfileName()
	st, err := up.InboxWriterStatus(ctx)
	if err != nil {
		in.WriterErr = err.Error()
	} else {
		in.Writer = st
	}
	if running, err := up.NotifyDaemonRunning(ctx); err != nil {
		in.ProcessErr = err.Error()
	} else {
		in.ProcessUp = running
	}
	recs, err := up.InboxExport(ctx)
	if err != nil {
		in.ExportErr = err.Error()
		return in, nil
	}
	own := make([]agentdeck.InboxRecord, 0, len(recs))
	for _, r := range recs {
		if r.Profile != "" && r.Profile == in.Profile {
			own = append(own, r)
		} else {
			in.Foreign++
		}
	}
	in.Records = len(own)
	return in, own
}

// Alert 返回必须显眼显示的告警；空串表示运行时来源这一轮是可信的。
func (in Intake) Alert() string {
	switch {
	case !in.Configured:
		return "没有配置 upstream agent-deck：运行时会话的待决事项取不到，队列里只有 Graph 的门"
	case in.WriterErr != "":
		return "查不了 notify-daemon 状态（" + in.WriterErr + "）：运行时待决事项可能缺失，不能当作没有"
	case in.ProcessErr != "":
		return "查不了 notify-daemon 进程（" + in.ProcessErr + "）：不能确认它在跑，运行时待决事项可能缺失，不能当作没有"
	case !in.Writer.Running:
		msg := "notify-daemon 没在运行：没有东西在记录会话的等待与转变，运行时待决事项看不到，空不代表没有。启动：agent-deck notify-daemon"
		if in.Writer.Detail != "" {
			msg += "。上游说明：" + in.Writer.Detail
		}
		return msg
	case !in.ProcessUp:
		return "notify-daemon 的心跳还在 90 秒窗口内，但进程表里找不到它：它可能刚退出，运行时待决事项可能缺失，空不代表没有"
	case in.ExportErr != "":
		return "读不到 inbox export（" + in.ExportErr + "）：运行时待决事项取不到，不是没有"
	}
	return ""
}

// Healthy 是来源可信时给人看的一句依据；有告警时返回空串。
func (in Intake) Healthy() string {
	if in.Alert() != "" {
		return ""
	}
	return fmt.Sprintf("notify-daemon 在跑（进程在，心跳 %d 秒前）", in.Writer.AgeSeconds)
}

// ForeignNote 说明有多少记录没收进队列。没有就返回空串。
func (in Intake) ForeignNote() string {
	if in.Foreign == 0 {
		return ""
	}
	return fmt.Sprintf("另有 %d 条 inbox 记录属于别的 profile 或没写 profile（本机 Work Deck 用 %q），回答投递不过去，没有收进队列", in.Foreign, in.Profile)
}

// Respond 处理 owner 对一个条目的回答。
//
//   - 会话条目、选项带 Message：投递回发起会话，结果记进 Answer。投递失败条目留在 open。
//   - 会话条目、选项不带 Message：只记下已处理。
//   - Graph 条目：Work Deck 不写账本，把命令写进 Answer.Note 交给 owner 执行；条目留在 open。
//   - 已回答的会话条目不再投递；上次投递中断（InFlight）的不自动重投。
func Respond(ctx context.Context, s *Store, d Deliverer, id, option string, now time.Time) (Item, error) {
	it, ok := s.Get(id)
	if !ok {
		return Item{}, fmt.Errorf("队列里没有条目 %s", id)
	}
	opt, ok := it.OptionByKey(option)
	if !ok {
		return Item{}, fmt.Errorf("条目 %s 没有选项 %q", id, option)
	}
	if it.Source.Kind == SourceSession && it.Status == StatusAnswered {
		return it, fmt.Errorf("这一条已经回答过了，不再投递")
	}
	if it.Answer != nil && it.Answer.InFlight {
		return it, fmt.Errorf("上次投递在途中中断，结果未知；为免对方收到两遍，不自动重投。请先按 p 预览那个会话确认")
	}
	ans := Answer{Option: option, AnsweredAt: now}
	switch {
	case it.Source.Kind == SourceSession && opt.Message != "":
		if d == nil {
			ans.DeliveryError = "没有配置 upstream agent-deck，投递不了"
			break
		}
		if err := s.MarkInFlight(id, option, now); err != nil {
			return it, fmt.Errorf("投递前记账失败，没有投递：%w", err)
		}
		rec := agentdeck.InboxRecord{ChildSessionID: it.Source.SessionID, Profile: it.Source.Profile}
		if err := d.DeliverAnswer(ctx, rec, opt.Message); err != nil {
			ans.DeliveryError = err.Error()
		} else {
			ans.Delivered = true
			ans.Note = fmt.Sprintf("已向会话 %s 发送「%s」", it.Source.SessionID, opt.Message)
		}
	case it.Source.Kind == SourceSession:
		ans.Note = "只记下已处理，没有给会话发消息"
	case opt.Command != "":
		ans.Note = "Work Deck 不写 Graph 账本，请在命令行执行：" + opt.Command + "（执行后 Graph 状态变了，这一条才离开队列）"
	default:
		ans.Note = "只记下已看过"
	}
	return s.Record(id, ans)
}
