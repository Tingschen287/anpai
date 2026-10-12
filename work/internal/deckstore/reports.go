package deckstore

import (
	"fmt"
	"regexp"
	"sort"
	"time"
)

// 子 agent 完成后给主 agent 的汇报队列（owner 在 n048 验收时要求，架构稿 §14.4）。
//
// 同一个子 agent 的同一次完成只收一次（按 子会话 + 完成编号 去重）。送不送、什么时候送由 viewstate 的送达门决定；
// 这里只记状态：pending（等送）→ delivered（已送达）或 unconfirmed（送达未确认，不自动重发）。

// 汇报来源。
const (
	ReportActive   = "active"   // 子 agent 自己运行 deck agent report --done
	ReportInferred = "inferred" // 状态转为空闲并保持一段时间，推断为完成
	ReportGraph    = "graph"    // 账本出现该节点的 submit
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
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	// Claim 不空表示被 deck agent wait 领走、等主 agent 确认收到（n051 评审 1 B5）；推送中的 sending 没有它
	Claim     string    `json:"claim,omitempty"`
	ClaimedAt time.Time `json:"claimed_at,omitempty"`
}

// ClaimWait 是 deck agent wait 的领取标记。
const ClaimWait = "wait"

// EnqueueReport 收下一条汇报。同一子会话、同一完成编号已经收过就返回已有的那条，不重复。
func (s *Store) EnqueueReport(r Report) (Report, bool, error) {
	var out Report
	added := false
	err := s.mutate(func() (func() error, []Event, error) {
		if !completionRe.MatchString(r.CompletionID) {
			return nil, nil, fmt.Errorf("完成编号 %q 不合规（1–64 个字母、数字或 ._:-）", r.CompletionID)
		}
		if r.Source != ReportActive && r.Source != ReportInferred && r.Source != ReportGraph {
			return nil, nil, fmt.Errorf("不认识的汇报来源 %q", r.Source)
		}
		sub, ok := s.sessions[r.Sub]
		if !ok || sub.Role != "sub" {
			return nil, nil, fmt.Errorf("%s 不是已登记的子 agent", r.Sub)
		}
		main, ok := s.sessions[sub.Parent]
		if !ok || main.Role != "main" || main.Project != sub.Project {
			return nil, nil, fmt.Errorf("子 agent %s 没有登记主 agent", r.Sub)
		}
		for _, x := range s.reports {
			if x.Sub == r.Sub && x.CompletionID == r.CompletionID {
				// 同一次完成：推断先入队、还没送，主动报告到了就换成主动报告的正文与标注（只送一次，送更准的那条）
				if x.State == ReportPending && x.Source == ReportInferred && r.Source == ReportActive {
					x.Source, x.Text, x.UpdatedAt = r.Source, Clean(r.Text, 2000), s.now()
					out = *x
					return s.saveReports, []Event{{Kind: "report", Project: x.Project, ID: x.ID}}, nil
				}
				out = *x
				return nil, nil, nil
			}
		}
		now := s.now()
		x := &Report{ID: newID("rep"), Project: sub.Project, Main: main.Key, Sub: sub.Key, CompletionID: r.CompletionID,
			Source: r.Source, Text: Clean(r.Text, 2000), State: ReportPending, CreatedAt: now, UpdatedAt: now}
		s.reports = append(s.reports, x)
		out, added = *x, true
		return s.saveReports, []Event{{Kind: "report", Project: x.Project, ID: x.ID}}, nil
	})
	return out, added, err
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
			return nil, nil, fmt.Errorf("%s 不是项目 %s 的主 agent", newMain, project)
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
		for _, x := range s.reports {
			if want[x.ID] && x.Main == main && x.State == ReportSending && x.Claim == ClaimWait {
				x.State, x.Claim, x.UpdatedAt = state, "", s.now()
				n++
				evs = append(evs, Event{Kind: "report", Project: x.Project, ID: x.ID})
			}
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
		return fmt.Errorf("汇报状态只能推进到 sending、delivered 或 unconfirmed")
	}
	return s.mutate(func() (func() error, []Event, error) {
		for _, x := range s.reports {
			if x.ID == id {
				if x.State != from {
					return nil, nil, fmt.Errorf("汇报 %s 是 %s，不能改成 %s", id, x.State, state)
				}
				x.State, x.UpdatedAt = state, s.now()
				return s.saveReports, []Event{{Kind: "report", Project: x.Project, ID: x.ID}}, nil
			}
		}
		return nil, nil, fmt.Errorf("没有汇报 %s", id)
	})
}

func (s *Store) saveReports() error { return writeJSON(s.path("reports.json"), s.reports) }
