package deckstore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 子 agent 完成后给主 agent 的汇报队列（owner 在 n048 验收时要求，架构稿 §14.4）。
//
// 同一个子 agent 的同一次真正完成只收一次：真正的完成只认主动报告，按 子会话 + 任务代次 去重。
// 推断提醒与账本提醒都只是「提醒」（owner 在 n051 attempt 2 设计评审选 A、attempt 4 设计复审选 B2-A）：各有自己的编号、不占完成，
// 账本提醒按 节点#attempt 只送一次；同一子 agent 同时只留一条等送的推断提醒。提醒还没送时到了更准的消息就换掉它（账本提醒换掉推断提醒，
// 主动报告换掉任何提醒），只送一次。送不送、什么时候送由 viewstate 的送达门决定；
// 这里只记状态：pending（等送）→ delivered（已送达）或 unconfirmed（送达未确认，不自动重发）。

// 汇报来源。
const (
	ReportActive   = "active"   // 子 agent 自己运行 deck agent report --done
	ReportInferred = "inferred" // 推断提醒：状态停下（idle/waiting）并保持一段时间；可能做完，也可能在等输入或权限确认，不算完成
	ReportGraph    = "graph"    // 账本提醒：账本出现该节点的 submit；只是提醒、不算完成，按 节点#attempt 只送一次（B2-A）
)

// 汇报状态。
const (
	ReportPending     = "pending"
	ReportSending     = "sending" // 已开始发送、结果还没落盘：重启后一律当送达未确认，不重发
	ReportDelivered   = "delivered"
	ReportUnconfirmed = "unconfirmed"
)

// Report 是一条等着转告主 agent 的汇报。
type Report struct {
	ID           string    `json:"id"`
	Project      string    `json:"project"`
	Main         string    `json:"main"` // 主 agent 会话 key
	Sub          string    `json:"sub"`  // 子 agent 会话 key
	CompletionID string    `json:"completion_id"`
	Source       string    `json:"source"`
	Text         string    `json:"text"`
	State        string    `json:"state"`
	CreatedAt    time.Time `json:"created_at"` // 这条汇报入队的时刻，决定转告顺序；被替换时不变
	UpdatedAt    time.Time `json:"updated_at"`
	// ArrivedAt 是现在这份内容到的时刻：新建时同 CreatedAt，被更准的消息替换（或并进）时改为那条消息到的时刻。
	// 判断推断提醒是否过时看它（n051 评审 4 B1：账本提醒换掉旧推断提醒后沿用旧时刻，下一段安静又多送一条推断提醒）。
	// 旧版本写的没有它，按 CreatedAt 算
	ArrivedAt time.Time `json:"arrived_at,omitempty"`
	// Claim 不空表示被 deck agent wait 领走、等主 agent 确认收到（n051 评审 1 B5）；推送中的 sending 没有它
	Claim     string    `json:"claim,omitempty"`
	ClaimedAt time.Time `json:"claimed_at,omitempty"`
	// Ref 是账本提醒的 节点#attempt：同一 attempt 只提醒一次，主 agent 发了新的话也不重报（attempt 2 设计评审 A；attempt 4 起只是提醒，B2-A）
	Ref string `json:"ref,omitempty"`
}

// ClaimWait 是 deck agent wait 的领取标记。
const ClaimWait = "wait"

// EnqueueReport 收下一条汇报。同一子会话、同一完成编号已经收过就返回已有的那条，不重复。
func (s *Store) EnqueueReport(r Report) (Report, bool, error) {
	var out Report
	added := false
	err := s.mutate(func() (func() error, []Event, error) {
		if r.Source != ReportActive && r.Source != ReportInferred && r.Source != ReportGraph {
			return nil, nil, i18n.E("err.deckstore.storeenqueuereport.1", r.Source)
		}
		sub, ok := s.sessions[r.Sub]
		if !ok || sub.Role != "sub" {
			return nil, nil, i18n.E("err.deckstore.storeenqueuereport.2", r.Sub)
		}
		switch {
		case r.Source == ReportActive && r.CompletionID == "":
			// 真正的完成不给编号：在锁里取子 agent 当前的任务代次（attempt 3 自查 S2：先取快照再入队，会和发话失败的撤回交错）。
			// 新话正在发时到的完成：上一代还没有完成就算上一代的（子 agent 正好做完上一件），否则算新的一代（新任务做得快）。
			// 两种情况都只送一次，不丢（owner 选 A'）
			gen := sub.Gen()
			if sub.SendInFlight && gen > 1 && !s.hasCompletionLocked(sub.Key, CompletionIDFor(gen-1)) {
				gen--
			}
			r.CompletionID = CompletionIDFor(gen)
		case r.Source == ReportGraph:
			// 账本提醒（owner 在 attempt 4 设计复审选 B2-A）：不占完成，按 节点#attempt 编号、只送一次
			if r.Ref == "" {
				return nil, nil, i18n.E("err.deckstore.storeenqueuereport.3")
			}
			// 编号用 节点#attempt 的摘要：节点 id 长时也不超过编号上限（attempt 4 自查）
			sum := sha256.Sum256([]byte(r.Ref))
			r.CompletionID = "ledger:" + hex.EncodeToString(sum[:8])
		}
		if !completionRe.MatchString(r.CompletionID) {
			return nil, nil, i18n.E("err.deckstore.storeenqueuereport.4", r.CompletionID)
		}
		main, ok := s.sessions[sub.Parent]
		if !ok || main.Role != "main" || main.Project != sub.Project {
			return nil, nil, i18n.E("err.deckstore.storeenqueuereport.5", r.Sub)
		}
		for _, x := range s.reports {
			// 同一次真正的完成（主动报告、同一代）或同一条账本提醒（同一 节点#attempt）只收一次
			if x.Sub == r.Sub && x.Source == r.Source && r.Source != ReportInferred && x.CompletionID == r.CompletionID {
				out = *x
				return nil, nil, nil
			}
		}
		if r.Source == ReportInferred && s.staleHintLocked(sub.Key, r.CompletionID) {
			// 这一代已经有真正的完成，或这段安静开始之后已经到过主动报告或账本提醒（检查与入队在同一把锁里）：不提醒
			return nil, nil, nil
		}
		for _, x := range s.reports {
			if x.Sub != r.Sub || !isReminder(x.Source) || x.State != ReportPending || x.Main != main.Key {
				continue
			}
			switch {
			case r.Source == ReportInferred:
				// 同一子 agent 同时只留一条等送的提醒
				out = *x
				return nil, nil, nil
			case r.Source == ReportGraph && x.Source == ReportGraph:
				continue // 不同轮的账本提醒各送各的
			}
			// 提醒还没送：换成更准的那条（账本提醒换掉推断提醒；主动报告换掉任何提醒），只送一次。
			// 到达时刻改为新消息的：过时判断看的是新内容什么时候到的（n051 评审 4 B1）
			now := s.now()
			x.CompletionID, x.Source, x.Text, x.Ref, x.UpdatedAt, x.ArrivedAt = r.CompletionID, r.Source, Clean(r.Text, 2000), r.Ref, now, now
			out, added = *x, true
			return s.saveReports, []Event{{Kind: "report", Project: x.Project, ID: x.ID}}, nil
		}
		now := s.now()
		x := &Report{ID: newID("rep"), Project: sub.Project, Main: main.Key, Sub: sub.Key, CompletionID: r.CompletionID,
			Source: r.Source, Text: Clean(r.Text, 2000), State: ReportPending, CreatedAt: now, UpdatedAt: now, ArrivedAt: now, Ref: r.Ref}
		s.reports = append(s.reports, x)
		out, added = *x, true
		return s.saveReports, []Event{{Kind: "report", Project: x.Project, ID: x.ID}}, nil
	})
	return out, added, err
}

// isReminder：推断提醒与账本提醒都只是提醒，不占完成（attempt 2 裁定 A、attempt 4 裁定 B2-A）。
func isReminder(source string) bool { return source == ReportInferred || source == ReportGraph }

// staleHintLocked：推断提醒 hint:代次:安静开始的毫秒 已经过时——这一代有了主动报告，或安静开始之后到过主动报告或账本提醒
// （按到达时刻算：替换进旧行的消息也算它自己到的时刻）。
func (s *Store) staleHintLocked(sub, hintID string) bool {
	if s.hasCompletionLocked(sub, hintGen(hintID)) {
		return true
	}
	p := strings.SplitN(hintID, ":", 3)
	if len(p) != 3 {
		return false
	}
	ms, err := strconv.ParseInt(p[2], 10, 64)
	if err != nil {
		return false
	}
	from := time.UnixMilli(ms)
	for _, x := range s.reports {
		if x.Sub == sub && x.Source != ReportInferred && !x.arrived().Before(from) {
			return true
		}
	}
	return false
}

// HasCompletion：这个子 agent 的这次完成（完成编号）已经收过。只认主动报告：推断提醒与账本提醒都不算（B2-A），旧版本按代次编号的推断行也不算。
func (s *Store) HasCompletion(sub, completionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hasCompletionLocked(sub, completionID)
}

func (s *Store) hasCompletionLocked(sub, completionID string) bool {
	if completionID == "" {
		return false
	}
	for _, x := range s.reports {
		if x.Sub == sub && x.Source == ReportActive && x.CompletionID == completionID {
			return true
		}
	}
	return false
}

// arrived 是现在这份内容到的时刻（旧版本写的没有 ArrivedAt，按 CreatedAt）。
func (x *Report) arrived() time.Time {
	if x.ArrivedAt.IsZero() {
		return x.CreatedAt
	}
	return x.ArrivedAt
}

// HintID 是推断提醒的编号：代次 + 这段安静开始的时刻（毫秒）。
func HintID(gen int, quietFrom time.Time) string {
	return fmt.Sprintf("hint:%d:%d", gen, quietFrom.UnixMilli())
}

// hintGen 从提醒编号取出所属代次的完成编号；不是提醒编号返回空。
func hintGen(id string) string {
	p := strings.SplitN(id, ":", 3)
	if len(p) != 3 || p[0] != "hint" {
		return ""
	}
	return "gen:" + p[1]
}

// absorbLocked：提醒从 sending 退回 pending（主 agent 正忙、或 wait 的回应没写出去）时，同一子 agent 在它之后已经到了真正的完成，
// 就把那条完成并进这条提醒的位置、删掉那条：只送一次，送真正的完成（attempt 2 自查：主 agent 忙时提醒多半处在 sending，原地替换常常落空）。
func (s *Store) absorbLocked(x *Report) {
	if !isReminder(x.Source) || x.State != ReportPending {
		return
	}
	// 这一代在提醒之前就有完成的，提醒入队时已被拒；所以只看提醒之后到的真正完成。先后按到达时刻比（n054 自查）：
	// 主动报告换进更早的旧提醒行时，CreatedAt 是旧行的，按它比会漏掉「完成在这条账本提醒之后到」，完成送达后又送一条账本提醒
	stale := false
	for i, y := range s.reports {
		if y == x || y.Sub != x.Sub || y.Source != ReportActive || y.arrived().Before(x.arrived()) {
			continue
		}
		// 合并只在同一主 agent 内（attempt 3 自查：提醒挂在已停止的旧主 agent 上时，并过去新主 agent 就取不到了）
		if y.State == ReportPending && y.Main == x.Main {
			x.CompletionID, x.Source, x.Text, x.Ref, x.UpdatedAt, x.ArrivedAt = y.CompletionID, y.Source, y.Text, y.Ref, s.now(), y.arrived()
			s.reports = append(s.reports[:i], s.reports[i+1:]...)
			return
		}
		stale = true // 提醒之后已经有真正的完成（不管哪一代、什么状态）
	}
	// 真正的完成已经领走、送达或标为未确认（评审 2 R3；attempt 3 自查：也包括之后新一代的完成）：提醒过时了，删掉这条从没送出去的提醒
	if stale {
		for i, y := range s.reports {
			if y == x {
				s.reports = append(s.reports[:i], s.reports[i+1:]...)
				return
			}
		}
	}
}

// Report 按 id 取一条汇报（副本）。
func (s *Store) Report(id string) (Report, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.reports {
		if x.ID == id {
			return *x, true
		}
	}
	return Report{}, false
}

// Reports 返回某项目的全部汇报（副本，按时间）。
func (s *Store) Reports(project string) []Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Report
	for _, x := range s.reports {
		if project == "" || x.Project == project {
			out = append(out, *x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// PendingReports 返回某主 agent 还没送的汇报，按时间。
func (s *Store) PendingReports(main string) []Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Report
	for _, x := range s.reports {
		if x.Main == main && x.State == ReportPending {
			out = append(out, *x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// AdoptOrphans 让新主 agent 接管同项目里已结束（stopped）的主 agent 留下的子 agent 与待送汇报（n051 增量自查）：
// 否则旧主 agent 退出后，它开的子 agent 的汇报既推不到新主 agent、新主 agent 也取不到，悄悄丢了。
// 先写汇报再写注册表；两次写之间失败时，已改的待送汇报归新主 agent，子 agent 之后的汇报仍挂旧主 agent（owner 从通知栏看得到）。
func (s *Store) AdoptOrphans(project, newMain string) (subs, reports int, err error) {
	err = s.mutate(func() (func() error, []Event, error) {
		nm, ok := s.sessions[newMain]
		if !ok || nm.Role != "main" || nm.Project != project {
			return nil, nil, i18n.E("err.deckstore.storeadoptorphans.1", newMain, project)
		}
		gone := map[string]bool{}
		for _, x := range s.sessions {
			if x.Role == "main" && x.Project == project && x.Key != newMain && x.Status == StatusStopped {
				gone[x.Key] = true
			}
		}
		if len(gone) == 0 {
			return nil, nil, nil
		}
		for _, x := range s.sessions {
			if x.Role == "sub" && x.Project == project && gone[x.Parent] {
				x.Parent = newMain
				subs++
			}
		}
		for _, r := range s.reports {
			if r.Project == project && gone[r.Main] && r.State == ReportPending {
				r.Main, r.UpdatedAt = newMain, s.now()
				reports++
			}
		}
		if subs == 0 && reports == 0 {
			return nil, nil, nil
		}
		return func() error {
			if err := s.saveReports(); err != nil {
				return err
			}
			return s.saveRegistry()
		}, []Event{{Kind: "session", Project: project, ID: newMain}}, nil
	})
	return subs, reports, err
}

// TakeReports 由主 agent 自己领走它还没送的汇报（deck agent wait，n051）：在锁内把 pending 标为 sending 并记下领取，
// 主 agent 收到后用 AckReports 确认才算送达；回应没写出去就 ReleaseClaims 退回；久不确认由 ExpireClaims 转为未确认（评审 1 B5）。
// 推送循环正在送（sending）的不取；被领走的，推送循环随后标 sending 会因状态不符而放弃，同一条只送一次。
func (s *Store) TakeReports(main string) ([]Report, error) {
	var out []Report
	err := s.mutate(func() (func() error, []Event, error) {
		var evs []Event
		for _, x := range s.reports {
			if x.Main == main && x.State == ReportPending {
				x.State, x.Claim, x.ClaimedAt, x.UpdatedAt = ReportSending, ClaimWait, s.now(), s.now()
				out = append(out, *x)
				evs = append(evs, Event{Kind: "report", Project: x.Project, ID: x.ID})
			}
		}
		if len(out) == 0 {
			return nil, nil, nil
		}
		return s.saveReports, evs, nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// claimed 在锁内把主 agent main 领走的 ids 改成 state；返回改了几条。
func (s *Store) claimed(main string, ids []string, state string) (int, error) {
	n := 0
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	err := s.mutate(func() (func() error, []Event, error) {
		var evs []Event
		var back []*Report
		for _, x := range s.reports {
			if want[x.ID] && x.Main == main && x.State == ReportSending && x.Claim == ClaimWait {
				x.State, x.Claim, x.UpdatedAt = state, "", s.now()
				back = append(back, x)
				n++
				evs = append(evs, Event{Kind: "report", Project: x.Project, ID: x.ID})
			}
		}
		for _, x := range back {
			s.absorbLocked(x)
		}
		if n == 0 {
			return nil, nil, nil
		}
		return s.saveReports, evs, nil
	})
	return n, err
}

// AckReports：主 agent 确认收到领走的汇报，标为送达。
func (s *Store) AckReports(main string, ids []string) (int, error) {
	return s.claimed(main, ids, ReportDelivered)
}

// ReleaseClaims：领走后回应没写出去，退回等送（推送或下一次 wait 再送）。
func (s *Store) ReleaseClaims(main string, ids []string) (int, error) {
	return s.claimed(main, ids, ReportPending)
}

// ExpireClaims：领走后超过 before 还没确认的，转为未确认（不重发），返回这些汇报供通知 owner。
func (s *Store) ExpireClaims(before time.Time) ([]Report, error) {
	var out []Report
	err := s.mutate(func() (func() error, []Event, error) {
		var evs []Event
		for _, x := range s.reports {
			if x.State == ReportSending && x.Claim == ClaimWait && x.ClaimedAt.Before(before) {
				x.State, x.Claim, x.UpdatedAt = ReportUnconfirmed, "", s.now()
				out = append(out, *x)
				evs = append(evs, Event{Kind: "report", Project: x.Project, ID: x.ID})
			}
		}
		if len(out) == 0 {
			return nil, nil, nil
		}
		return s.saveReports, evs, nil
	})
	return out, err
}

var completionRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

// MarkReport 推进汇报状态：pending → sending → delivered / unconfirmed（或确认没送时回到 pending）。sending 要在真正调发送之前落盘，
// 这样「已发送但结果没落盘」时重启会看到 sending，并标为 unconfirmed，不重放（n050 评审建议）。
func (s *Store) MarkReport(id, state string) error {
	// sending → pending 只用于「上游确认什么都没送」（主 agent 正在回答，--defer-if-busy 超时，n051）
	next := map[string]string{ReportSending: ReportPending, ReportDelivered: ReportSending, ReportUnconfirmed: ReportSending, ReportPending: ReportSending}
	from, ok := next[state]
	if !ok {
		return i18n.E("err.deckstore.storemarkreport.1")
	}
	return s.mutate(func() (func() error, []Event, error) {
		for _, x := range s.reports {
			if x.ID == id {
				if x.State != from {
					return nil, nil, i18n.E("err.deckstore.storemarkreport.2", id, x.State, state)
				}
				x.State, x.UpdatedAt = state, s.now()
				s.absorbLocked(x)
				return s.saveReports, []Event{{Kind: "report", Project: x.Project, ID: x.ID}}, nil
			}
		}
		return nil, nil, i18n.E("err.deckstore.storemarkreport.3", id)
	})
}

func (s *Store) saveReports() error { return writeJSON(s.path("reports.json"), s.reports) }
