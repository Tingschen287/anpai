package deckstore

import (
	"fmt"
	"regexp"
	"sort"
	"time"
)

// 决策 v1 数据合同（架构稿 §4，回应预审 B5、R2）。
//
// 只有 owner 界面能回答；回答必须带当前 version，在 Store 的锁内比较后再改，防止两次回答同时生效。
// 批准只解除对应的那一道门：真正执行前调用 BeginExecute，重新核对仍是 approved、未过期、scope 未变。
// applied 只表示动作已执行完成；uncertain（效果不明）不自动重做。

// 决策类别。
const (
	KindCost        = "cost"        // 花额度（超出预算包络、额度未知时启动 frontier）
	KindConfig      = "config"      // 改电脑设置
	KindPush        = "push"        // 推送代码
	KindDisagree    = "disagree"    // 协作者意见不一
	KindDeviation   = "deviation"   // 偏离 owner 决策
	KindCodexTrust  = "codex_trust" // 允许上游改写 ~/.codex/config.toml
	KindGraphGate   = "graph_gate"  // Graph 门：回答只返回命令，不写账本
	KindSessionWait = "session_wait"
)

var kinds = map[string]bool{KindCost: true, KindConfig: true, KindPush: true, KindDisagree: true, KindDeviation: true,
	KindCodexTrust: true, KindGraphGate: true, KindSessionWait: true}

// 状态。
const (
	StateOpen      = "open"
	StateApproved  = "approved" // 选了 approve
	StateAnswered  = "answered" // 选了其他具体选项（意见不一、偏离等）
	StateRejected  = "rejected"
	StateStopped   = "stopped"
	StateExecuting = "executing"
	StateApplied   = "applied"
	StateFailed    = "failed"
	StateUncertain = "uncertain"
	StateExpired   = "expired"
	StateCancelled = "cancelled"
	StateStale     = "stale"
)

// 固定选项。每个决定都含 reject 与 stop。
const (
	OptApprove = "approve"
	OptReject  = "reject"
	OptStop    = "stop"
)

// Decision 是一个待 owner 决定的事项。
type Decision struct {
	ID         string            `json:"id"`
	Project    string            `json:"project"`
	Kind       string            `json:"kind"`
	Summary    string            `json:"summary"`
	Options    []string          `json:"options"`
	Recommend  string            `json:"recommend,omitempty"`
	Scope      map[string]string `json:"scope,omitempty"`
	ScopeHash  string            `json:"scope_hash,omitempty"`
	Version    int               `json:"version"`
	State      string            `json:"state"`
	Answer     string            `json:"answer,omitempty"`
	AnsweredBy string            `json:"answered_by,omitempty"`
	CreatedBy  string            `json:"created_by"`
	CreatedAt  time.Time         `json:"created_at"`
	ExpiresAt  time.Time         `json:"expires_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
	Result     string            `json:"result,omitempty"`
}

// DefaultDecisionTTL 是决定的默认有效期。
const DefaultDecisionTTL = 24 * time.Hour

// NewDecision 是创建决定的输入；id、version、状态、时间由 Store 定，不信调用方。
type NewDecision struct {
	Project   string
	Kind      string
	Summary   string
	Options   []string // 额外的具体选项；approve/reject/stop 由 Store 按类别补上
	Recommend string
	Scope     map[string]string
	CreatedBy string
	TTL       time.Duration
}

// CreateDecision 新建一个 open 的决定。
func (s *Store) CreateDecision(in NewDecision) (Decision, error) {
	var out Decision
	err := s.mutate(func() (func() error, []Event, error) {
		if !kinds[in.Kind] {
			return nil, nil, fmt.Errorf("不认识的决定类别 %q", in.Kind)
		}
		if _, ok := s.settings.Project(in.Project); !ok && in.Project != "" {
			return nil, nil, fmt.Errorf("没有项目 %s", in.Project)
		}
		if in.Summary == "" || in.CreatedBy == "" {
			return nil, nil, fmt.Errorf("决定缺少摘要或创建者")
		}
		opts := []string{}
		seen := map[string]bool{}
		add := func(o string) {
			o = Clean(o, 80)
			if o != "" && !seen[o] {
				seen[o] = true
				opts = append(opts, o)
			}
		}
		switch in.Kind {
		case KindCost, KindConfig, KindPush, KindCodexTrust:
			add(OptApprove)
		}
		for _, o := range in.Options {
			add(o)
		}
		add(OptReject)
		add(OptStop)
		if err := checkScope(in.Scope); err != nil {
			return nil, nil, err
		}
		ttl := in.TTL
		if ttl <= 0 {
			ttl = DefaultDecisionTTL
		}
		now := s.now()
		scope := map[string]string{}
		for k, v := range in.Scope {
			scope[k] = v
		}
		d := &Decision{ID: newID("dec"), Project: in.Project, Kind: in.Kind, Summary: Clean(in.Summary, 800),
			Options: opts, Recommend: Clean(in.Recommend, 80), Scope: scope, ScopeHash: ScopeHash(scope),
			Version: 1, State: StateOpen, CreatedBy: in.CreatedBy, CreatedAt: now, ExpiresAt: now.Add(ttl), UpdatedAt: now}
		s.decs[d.ID] = d
		out = d.clone()
		return s.saveDecisions, []Event{{Kind: "decision", Project: d.Project, ID: d.ID}}, nil
	})
	return out, err
}

var scopeKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// checkScope：scope 会原样落盘、经 API 返回，并参与执行前的比对，所以不能清洗（会改变语义），只能拒绝。
// 键只许小写字母开头的字母、数字与下划线，值限长、不许控制字符，疑似密钥直接拒（n050 评审 B7）。
func checkScope(scope map[string]string) error {
	if len(scope) > 32 {
		return fmt.Errorf("scope 字段太多")
	}
	for k, v := range scope {
		if !scopeKeyRe.MatchString(k) {
			return fmt.Errorf("scope 的键 %q 不合规（小写字母开头，只含小写字母、数字、下划线）", k)
		}
		if len(v) > 300 || Clean(v, 0) != v {
			return fmt.Errorf("scope 的 %s 含控制字符、疑似密钥或过长", k)
		}
	}
	return nil
}

// clone 返回不与内部共享切片与 map 的副本。
func (d *Decision) clone() Decision {
	c := *d
	c.Options = append([]string(nil), d.Options...)
	if d.Scope != nil {
		c.Scope = make(map[string]string, len(d.Scope))
		for k, v := range d.Scope {
			c.Scope[k] = v
		}
	}
	return c
}

// ErrVersion 表示回答时带的 version 已过时（别人先改了）。
type ErrVersion struct{ Want, Have int }

func (e *ErrVersion) Error() string {
	return fmt.Sprintf("这个决定已经变了（你看到的是第 %d 版，现在是第 %d 版），请刷新后再答", e.Want, e.Have)
}

// Answer 记录 owner 的回答。by 只能是 owner 界面的身份（调用方保证）；version 必须等于当前版本。
func (s *Store) Answer(id, option string, version int, by string) (Decision, error) {
	var out Decision
	err := s.mutate(func() (func() error, []Event, error) {
		d, ok := s.decs[id]
		if !ok {
			return nil, nil, fmt.Errorf("没有决定 %s", id)
		}
		s.expireLocked(d)
		if d.State != StateOpen {
			return nil, nil, fmt.Errorf("决定 %s 已是 %s，不能再答", id, d.State)
		}
		if d.Version != version {
			return nil, nil, &ErrVersion{Want: version, Have: d.Version}
		}
		valid := false
		for _, o := range d.Options {
			if o == option {
				valid = true
			}
		}
		if !valid {
			return nil, nil, fmt.Errorf("选项 %q 不在决定 %s 的选项里", option, id)
		}
		switch option {
		case OptApprove:
			d.State = StateApproved
		case OptReject:
			d.State = StateRejected
		case OptStop:
			d.State = StateStopped
		default:
			d.State = StateAnswered
		}
		d.Answer, d.AnsweredBy, d.UpdatedAt = option, by, s.now()
		d.Version++
		out = d.clone()
		return s.saveDecisions, []Event{{Kind: "decision", Project: d.Project, ID: d.ID}}, nil
	})
	return out, err
}

// BeginExecute 在真正执行被批准的动作前调用：仍是 approved、未过期、scope 与当前请求一致，才转为 executing。
// scope 不一致就转为 stale，不执行。
func (s *Store) BeginExecute(id string, scopeNow map[string]string) (Decision, error) {
	var out Decision
	err := s.mutate(func() (func() error, []Event, error) {
		d, ok := s.decs[id]
		if !ok {
			return nil, nil, fmt.Errorf("没有决定 %s", id)
		}
		if s.expireLocked(d) {
			out = d.clone()
			return s.saveDecisions, []Event{{Kind: "decision", Project: d.Project, ID: d.ID}}, fmt.Errorf("决定 %s 已过期", id)
		}
		if d.State != StateApproved {
			return nil, nil, fmt.Errorf("决定 %s 是 %s，不能执行", id, d.State)
		}
		if ScopeHash(scopeNow) != d.ScopeHash {
			d.State, d.Result, d.UpdatedAt = StateStale, "执行时的请求与批准时不一致，不执行", s.now()
			d.Version++
			out = d.clone()
			return s.saveDecisions, []Event{{Kind: "decision", Project: d.Project, ID: d.ID}}, fmt.Errorf("决定 %s 批准的范围与本次请求不一致，已作废", id)
		}
		d.State, d.UpdatedAt = StateExecuting, s.now()
		d.Version++
		out = d.clone()
		return s.saveDecisions, []Event{{Kind: "decision", Project: d.Project, ID: d.ID}}, nil
	})
	// 过期或作废时，状态改变已经落盘，只是执行被拒：out 带着新状态一起返回。
	return out, err
}

// FinishExecute 记录执行结果：applied、failed 或 uncertain。
func (s *Store) FinishExecute(id, state, result string) error {
	if state != StateApplied && state != StateFailed && state != StateUncertain {
		return fmt.Errorf("执行结果只能是 applied、failed、uncertain，不是 %q", state)
	}
	return s.mutate(func() (func() error, []Event, error) {
		d, ok := s.decs[id]
		if !ok {
			return nil, nil, fmt.Errorf("没有决定 %s", id)
		}
		if d.State != StateExecuting {
			return nil, nil, fmt.Errorf("决定 %s 不在执行中（%s）", id, d.State)
		}
		d.State, d.Result, d.UpdatedAt = state, Clean(result, 400), s.now()
		d.Version++
		return s.saveDecisions, []Event{{Kind: "decision", Project: d.Project, ID: d.ID}}, nil
	})
}

// Cancel 由创建者（主 agent）撤回一个还没答的决定。
func (s *Store) Cancel(id, by string) error {
	return s.mutate(func() (func() error, []Event, error) {
		d, ok := s.decs[id]
		if !ok {
			return nil, nil, fmt.Errorf("没有决定 %s", id)
		}
		if d.CreatedBy != by {
			return nil, nil, fmt.Errorf("只有创建者能撤回决定 %s", id)
		}
		if d.State != StateOpen {
			return nil, nil, fmt.Errorf("决定 %s 已是 %s，不能撤回", id, d.State)
		}
		d.State, d.UpdatedAt = StateCancelled, s.now()
		d.Version++
		return s.saveDecisions, []Event{{Kind: "decision", Project: d.Project, ID: d.ID}}, nil
	})
}

// Decisions 列出决定（含过期判定）。project 为空返回全部。
func (s *Store) Decisions(project string) []Decision {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Decision
	for _, d := range s.decs {
		if project == "" || d.Project == project {
			s.expireLocked(d)
			out = append(out, d.clone())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Decision 按 id 取一个决定。
func (s *Store) Decision(id string) (Decision, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.decs[id]
	if !ok {
		return Decision{}, false
	}
	s.expireLocked(d)
	return d.clone(), true
}

// expireLocked：open 或 approved 但已过有效期的转为 expired（只改内存，落盘随下一次写）。
func (s *Store) expireLocked(d *Decision) bool {
	if (d.State == StateOpen || d.State == StateApproved) && !d.ExpiresAt.IsZero() && s.now().After(d.ExpiresAt) {
		d.State, d.UpdatedAt = StateExpired, s.now()
		d.Version++
		return true
	}
	return false
}

func (s *Store) saveDecisions() error {
	list := make([]*Decision, 0, len(s.decs))
	for _, d := range s.decs {
		list = append(list, d)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return writeJSON(s.path("decisions.json"), list)
}
