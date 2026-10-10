package layout

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type script struct {
	mu      sync.Mutex
	now     time.Time
	runs    []run // 每次 attach 的结果
	i       int
	ops     map[int]string // client 进程号 → op（Announce 在后台并发调用，按进程号还原先后）
	ctxs    map[int]context.Context
	pauses  []string
	answers []bool
}

type run struct {
	lasts time.Duration
	err   error // nil：正常断开（Ctrl-Q）；非 nil：连不上
	start error // Attach 本身失败
}

func (s *script) loop() *CenterLoop {
	return &CenterLoop{
		Now: func() time.Time { s.mu.Lock(); defer s.mu.Unlock(); return s.now },
		Attach: func() (int, func() error, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			r := s.runs[s.i]
			s.i++
			if r.start != nil {
				return 0, nil, r.start
			}
			return 100 + s.i, func() error { s.mu.Lock(); s.now = s.now.Add(r.lasts); s.mu.Unlock(); return r.err }, nil
		},
		Announce: func(ctx context.Context, op string, pid int) {
			s.mu.Lock()
			if s.ops == nil {
				s.ops, s.ctxs = map[int]string{}, map[int]context.Context{}
			}
			s.ops[pid], s.ctxs[pid] = op, ctx
			s.mu.Unlock()
		},
		Pause: func(reason string) bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.pauses = append(s.pauses, reason)
			a := s.answers[0]
			s.answers = s.answers[1:]
			return a
		},
	}
}

func waitOps(t *testing.T, s *script, n int) []string {
	t.Helper()
	for i := 0; i < 200; i++ {
		s.mu.Lock()
		var got []string
		for pid := 101; pid < 101+len(s.runs); pid++ {
			if op, ok := s.ops[pid]; ok {
				got = append(got, op)
			}
		}
		s.mu.Unlock()
		if len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Announce 只来了 %v", s.ops)
	return nil
}

// 首次连上 resync；Ctrl-Q（正常断开）后 reset；连不上之后 resync。
func TestCenterLoopChoosesOpByHowTheClientEnded(t *testing.T) {
	s := &script{now: time.Unix(0, 0), runs: []run{
		{lasts: time.Minute},                                                         // 首次，owner 用了一会儿后按 Ctrl-Q
		{lasts: time.Minute, err: errors.New("lost")},                                // 第二次，连接出错
		{lasts: time.Minute},                                                         // 第三次
		{start: errors.New("x")}, {start: errors.New("x")}, {start: errors.New("x")}, // 之后连续起不来
	}, answers: []bool{false}}
	s.loop().Run()
	ops := waitOps(t, s, 3)
	if strings.Join(ops, ",") != "resync,reset,resync" {
		t.Fatalf("op 顺序不对：%v", ops)
	}
}

// 连续 3 次快速断开就停下等 owner；owner 选重试后重新计数、按 resync 连。
func TestCenterLoopStopsRetryingAfterRepeatedQuickExits(t *testing.T) {
	s := &script{now: time.Unix(0, 0), runs: []run{
		{lasts: 100 * time.Millisecond, err: errors.New("no server")},
		{lasts: 100 * time.Millisecond, err: errors.New("no server")},
		{lasts: 100 * time.Millisecond, err: errors.New("no server")},
		{lasts: time.Minute}, // owner 按了重试，这次连上了
		{start: errors.New("x")}, {start: errors.New("x")}, {start: errors.New("x")},
	}, answers: []bool{true, false}}
	s.loop().Run()
	if len(s.pauses) != 2 || !strings.Contains(s.pauses[0], "连续 3 次") || !strings.Contains(s.pauses[0], "no server") {
		t.Fatalf("应在第 3 次快速断开后停下并说明原因：%v", s.pauses)
	}
	if s.i != 7 {
		t.Fatalf("停下前不应多试：attach 了 %d 次", s.i)
	}
	ops := waitOps(t, s, 4)
	if ops[3] != "resync" {
		t.Fatalf("重试后连上应是 resync：%v", ops)
	}
}

// 快速但正常的断开（例如连按 Ctrl-Q）也计数，避免会话一开就关时空转。
func TestCenterLoopCountsQuickNormalExitsToo(t *testing.T) {
	s := &script{now: time.Unix(0, 0), runs: []run{
		{lasts: 10 * time.Millisecond}, {lasts: 10 * time.Millisecond}, {lasts: 10 * time.Millisecond},
	}, answers: []bool{false}}
	s.loop().Run()
	if len(s.pauses) != 1 {
		t.Fatalf("应停下一次：%v", s.pauses)
	}
}

// 每个 client 的通知在它退出后即取消（n049 评审 1 B1）。
func TestCenterLoopCancelsAnnounceWhenItsClientExits(t *testing.T) {
	s := &script{now: time.Unix(0, 0), runs: []run{
		{lasts: time.Minute}, {lasts: time.Minute},
		{start: errors.New("x")}, {start: errors.New("x")}, {start: errors.New("x")},
	}, answers: []bool{false}}
	s.loop().Run()
	waitOps(t, s, 2)
	s.mu.Lock()
	defer s.mu.Unlock()
	for pid, ctx := range s.ctxs {
		if ctx.Err() == nil {
			t.Fatalf("client %d 退出后它的通知还没取消", pid)
		}
	}
}
