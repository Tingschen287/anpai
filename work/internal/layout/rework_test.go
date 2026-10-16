package layout

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
)

// n049 评审 1 S2：显隐的接回失败要如实返回；并发的布局操作串行化。

type unparkFails struct {
	TmuxMux
	pane string
}

func (m unparkFails) Unpark(pane, target string, spec agentdeck.SplitSpec) error {
	if pane == m.pane {
		return errors.New("注入：接回失败")
	}
	return m.TmuxMux.Unpark(pane, target, spec)
}

func TestToggleReportsFailedRestore(t *testing.T) {
	m, s, _ := realLayout(t)
	if err := Toggle(m, s, "right"); err != nil { // 先隐藏右栏
		t.Fatal(err)
	}
	if err := Toggle(m, s, "bottom"); err != nil { // 先隐藏下栏
		t.Fatal(err)
	}
	roles := byRole(t, m)
	err := Toggle(unparkFails{m, roles[RoleTabs].ID}, s, "right")
	if err == nil || !strings.Contains(err.Error(), "标签条") {
		t.Fatalf("标签条接不回应报错，不能报成功：%v", err)
	}
	err = Toggle(unparkFails{m, roles[RoleSidebar].ID}, s, "bottom")
	if err == nil || !strings.Contains(err.Error(), "侧栏") {
		t.Fatalf("侧栏接不回应报错，不能报成功：%v", err)
	}
}

func TestLayoutLockSerializes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deck.lock")
	unlock, err := Lock(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(path, 100*time.Millisecond); err == nil {
		t.Fatal("锁被占着时应等不到、报错")
	}
	got := make(chan error, 1)
	go func() {
		u, err := Lock(path, 2*time.Second)
		if err == nil {
			u()
		}
		got <- err
	}()
	time.Sleep(100 * time.Millisecond)
	unlock()
	if err := <-got; err != nil {
		t.Fatalf("前一个放开后应拿到：%v", err)
	}
}

// 快速连按显隐键：每个操作都先拿锁，做完一轮后布局与原来一致。
func TestConcurrentTogglesUnderLockRestoreTheLayout(t *testing.T) {
	m, s, _ := realLayout(t)
	before := byRole(t, m)
	lock := filepath.Join(t.TempDir(), "deck.lock")
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for _, region := range []string{"left", "right", "bottom", "left", "right", "bottom"} {
		wg.Add(1)
		go func(region string) {
			defer wg.Done()
			unlock, err := Lock(lock, 10*time.Second)
			if err != nil {
				errs <- err
				return
			}
			defer unlock()
			errs <- Toggle(m, s, region)
		}(region)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	after := byRole(t, m)
	for role, p := range before {
		q := after[role]
		if q.Window != p.Window || q.Width != p.Width || q.Height != p.Height || q.PID != p.PID {
			t.Fatalf("每栏各显隐两次后 %s 应复原：%+v → %+v", role, p, q)
		}
	}
}
