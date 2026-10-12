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
}

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

// TakeReports 由主 agent 自己取走它还没送的汇报（deck agent wait，n051）：在锁内把 pending 直接标为 delivered 并返回。
// 推送循环正在送（sending）的不取；被取走的，推送循环随后标 sending 会因状态不符而放弃，同一条只送一次。
func (s *Store) TakeReports(main string) ([]Report, error) {
	var out []Report
	err := s.mutate(func() (func() error, []Event, error) {
		var evs []Event
		for _, x := range s.reports {
			if x.Main == main && x.State == ReportPending {
				x.State, x.UpdatedAt = ReportDelivered, s.now()
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
