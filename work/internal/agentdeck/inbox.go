package agentdeck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/lease"
)

// InboxRecord 是 `inbox export --json` 的一条记录。
//
// 字段照上游 session.TransitionNotificationEvent（internal/session/transition_notifier.go:40-108）。
// 两类记录共用这个结构：Kind 为空是一次状态翻转（from → to），Kind 为 "finished"
// 是一次完成回报（来自上游的完成账本，from/to 都是空串）。
type InboxRecord struct {
	ChildSessionID   string    `json:"child_session_id"`
	ChildTitle       string    `json:"child_title"`
	Profile          string    `json:"profile"`
	FromStatus       string    `json:"from_status"`
	ToStatus         string    `json:"to_status"`
	Timestamp        time.Time `json:"timestamp"`
	Substate         string    `json:"substate"`
	LastOutputHash   string    `json:"last_output_hash"`
	TargetSessionID  string    `json:"target_session_id"`
	TargetKind       string    `json:"target_kind"`
	Kind             string    `json:"kind"`
	DoneStatus       string    `json:"done_status"`
	DoneSummary      string    `json:"done_summary"`
	TurnFingerprint  string    `json:"turn_fingerprint"`
	SourceRemote     string    `json:"source_remote"`
	DeadLetterReason string    `json:"dead_letter_reason"`
}

// Fingerprint 是这条记录的去重键。
//
// 上游 export 的输出里没有 id，也剥掉了自己的 fp 字段。这里逐字照上游 EventFingerprint
// （internal/session/event_fingerprint.go:22-48）重算：每段先去首尾空白，from/to/done_status
// 再转小写，kind 原样；不含时间戳，所以 daemon 重启后用新时间戳重发的同一轮，算出来还是同一个键。
// TestFingerprintNormalizesLikeUpstream 核对这些规范化。
//
// 代价写在这里：没有 last_output_hash 的状态翻转，同一个会话每次 running→waiting
// 都算出同一个键，两次不同的等待会被当成同一条。上游 turn_fingerprint 在这种情况下
// 也一样退化（inbox_outbox.go:92-106）。
func (r InboxRecord) Fingerprint() string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(r.SourceRemote))
	b.WriteByte('|')
	b.WriteString(strings.TrimSpace(r.ChildSessionID))
	b.WriteByte('|')
	b.WriteString(strings.ToLower(strings.TrimSpace(r.FromStatus)))
	b.WriteByte('|')
	b.WriteString(strings.ToLower(strings.TrimSpace(r.ToStatus)))
	b.WriteByte('|')
	b.WriteString(strings.TrimSpace(r.LastOutputHash))
	if r.Kind != "" {
		b.WriteByte('|')
		b.WriteString(r.Kind)
		b.WriteByte('|')
		b.WriteString(strings.ToLower(strings.TrimSpace(r.DoneStatus)))
		b.WriteByte('|')
		b.WriteString(strings.TrimSpace(r.DoneSummary))
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// WriterStatus 是 `inbox writer-status --json` 的输出。
//
// 字段照上游 session.WriterStatus（internal/session/notify_heartbeat.go:57-65）。
// 上游只看心跳文件的新旧（90 秒内算在跑），不看进程；不论在不在跑，退出码都是 0，
// 所以只能看 Running。
type WriterStatus struct {
	Running       bool      `json:"running"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
	AgeSeconds    int64     `json:"age_seconds"`
	Detail        string    `json:"detail"`
}

// InboxExport 非消费式地读本机全部 completion 与 transition 记录。
//
// 为什么是 export 而不是 drain：drain 按父会话分片、读完就删，是 conductor 的消费口；
// Work Deck 不是会话，去 drain 等于和 conductor 抢同一份记录。上游对 export 的承诺是
// 「NON-DESTRUCTIVE ... two conductors draining the same host both get the records」
// （internal/session/inbox_export.go:31-34）。白名单只放 export 与 writer-status，
// inbox 底下其余子命令一律拒绝，见 TestInboxOnlyAllowsNonConsumingReads。
//
// export 不分 profile：它读的是全机记录，要区分只能看每条的 Profile 字段。
// 副作用不为零：记录里出现的每个 profile，上游都会打开它的注册表，没有就建一个
// state.db（见 manifest.go 的 SideEffects）。
func (c *Client) InboxExport(ctx context.Context) ([]InboxRecord, error) {
	out, err := c.exec(ctx, "inbox", "export", "--json")
	if err != nil {
		return nil, err
	}
	var records []InboxRecord
	if err := i18n.UnmarshalExternal(out, &records); err != nil {
		return nil, i18n.E("err.agentdeck.clientinboxexport.1", err)
	}
	return records, nil
}

// InboxWriterStatus 问本机有没有 notify-daemon 在记录。
//
// 会话的等待、出错这类状态翻转只由 notify-daemon 写（cmd/agent-deck/inbox_cmd.go:447-450）；
// 完成账本另有 run-task 的 worker 会写（internal/session/taskworker.go）。没有 daemon 时，
// export 里就没有任何翻转，最多剩几条完成记录，看起来和「没有待决事项」一模一样——
// 所以每次读 export 之前都先问这一句，daemon 不在就必须明说，不能画一个空队列。
func (c *Client) InboxWriterStatus(ctx context.Context) (WriterStatus, error) {
	out, err := c.exec(ctx, "inbox", "writer-status", "--json")
	if err != nil {
		return WriterStatus{}, err
	}
	var st WriterStatus
	if err := i18n.UnmarshalExternal(out, &st); err != nil {
		return WriterStatus{}, i18n.E("err.agentdeck.clientinboxwriterstatus.1", err)
	}
	return st, nil
}

// DeliverAnswer 把一条回答投递回发起会话。
//
// 投递口就是 session send，文本走标准输入。只投本客户端 profile 里的会话：
// session send 只在当前 profile 里解析会话号（上游 session_cmd.go:2760-2767），
// 而本包的 profile 是配置里显式写死的，不会为一条记录临时换 profile。
// 记录属于别的 profile 时直接拒绝，说清原因。
func (c *Client) DeliverAnswer(ctx context.Context, p lease.Permit, rec InboxRecord, message string) error {
	if rec.Profile != "" && rec.Profile != c.Profile {
		return i18n.E("err.agentdeck.clientdeliveranswer.1", rec.ChildSessionID, rec.Profile, c.Profile)
	}
	return c.SendMessage(ctx, p, rec.ChildSessionID, message)
}

// procRoot 是进程表的位置：空表示本平台的进程表（Linux /proc，macOS sysctl），测试里换成假的 /proc 目录。
var procRoot = ""

// NotifyDaemonRunning 看本机进程表里有没有 upstream agent-deck 的 notify-daemon 在跑。
//
// 为什么光有 writer-status 不够：上游只看心跳文件新旧，90 秒内都算在跑
// （internal/session/notify_heartbeat.go:31），daemon 刚退出的那 90 秒里它照样答 running。
// 这里读进程表里每个进程的命令行（Linux 读 /proc/<pid>/cmdline，macOS 读 sysctl kern.procargs2），
// 找「程序名含 agent-deck、参数里有 notify-daemon」的进程。
// 只读，不执行任何命令。读不了进程表时返回错误，调用方据此告警，不当作在跑。
func (c *Client) NotifyDaemonRunning(ctx context.Context) (bool, error) {
	pids, err := procPIDs(procRoot)
	if err != nil {
		return false, i18n.E("err.agentdeck.clientnotifydaemonrunning.1", procTableName(procRoot), err)
	}
	for _, pid := range pids {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		argv, ok := procArgv(procRoot, pid)
		if !ok {
			continue // 进程已经没了，或是内核线程
		}
		if !strings.Contains(filepath.Base(argv[0]), "agent-deck") {
			continue
		}
		for _, a := range argv[1:] {
			if a == "notify-daemon" {
				return true, nil
			}
		}
	}
	return false, nil
}

// ProfileName 是本客户端显式指定的 profile。队列只收这个 profile 的会话记录。
func (c *Client) ProfileName() string { return c.Profile }
