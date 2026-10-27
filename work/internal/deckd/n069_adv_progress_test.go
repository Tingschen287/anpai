package deckd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/lease"
	"github.com/Tingschen287/terminal-deck/work/internal/viewstate"
)

// ---- 对抗测试（n069adv）----

func (f *fixture) rawGet(path string) (int, map[string]any) {
	req, _ := http.NewRequest("GET", f.http.URL+path, nil)
	req.Header.Set("X-Test-As", "owner")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return -1, nil
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// 1a 反复 Open/Focus/Hide，同时取跟随：项目必须是读取前后某个焦点会话的项目，不 panic、无数据竞争（go test -race）。
func TestAdvFollowUnderRapidSwitching(t *testing.T) {
	f := newFixture(t)
	f.viewOn(t, "")
	m := f.srv.view
	ids := []string{f.main.AgentDeckID, f.sub.AgentDeckID, f.other.AgentDeckID}
	projOf := map[string]string{f.main.AgentDeckID: "alpha", f.sub.AgentDeckID: "alpha", f.other.AgentDeckID: "beta", "": "", viewstate.Idle: ""}
	var stop atomic.Bool
	var wg sync.WaitGroup
	for w := 0; w < 3; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; !stop.Load(); i++ {
				id := ids[(i+w)%3]
				switch i % 3 {
				case 0:
					m.Open(id)
				case 1:
					m.Focus(id)
				default:
					m.Hide(id)
				}
			}
		}(w)
	}
	var bad atomic.Value
	var skipped atomic.Int64
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 400; i++ {
				preS := m.State()
				pre := preS.Focus
				p, why := f.srv.followedProject()
				postS := m.State()
				post := postS.Focus
				if preS.LayoutGen != postS.LayoutGen {
					skipped.Add(1)
					continue // 读取期间切换过：无法判定，跳过（LayoutGen 每次切换都加一，排除 ABA）
				}
				if p == "" && why == "" {
					bad.Store("空项目没有原因")
				}
				if p != "" && p != projOf[pre] && p != projOf[post] {
					// 读取期间焦点可能来回变多次（pre、post 之间换过别的）——这里只能抓到「项目既不是前也不是后」，宽松检查
					bad.Store("项目 " + p + " 不属于前后焦点 " + pre + "/" + post)
				}
				if p != "" && p != "alpha" && p != "beta" {
					bad.Store("未知项目 " + p)
				}
			}
		}()
	}
	for i := 0; i < 200; i++ {
		code, out := f.rawGet("/v1/progress")
		if code != 200 || (out["project"] == "" && out["follow"] != true) {
			t.Fatalf("%d %v", code, out)
		}
	}
	time.Sleep(50 * time.Millisecond)
	stop.Store(true)
	wg.Wait()
	t.Logf("跳过（读取期间切换过）%d / 1600", skipped.Load())
	if v := bad.Load(); v != nil {
		t.Fatal(v)
	}
}

// 1b 稳定后：主与子 agent 同时打开，焦点在子 agent 上，跟的是同一个项目；隐藏到没有，回到 no_session。
func TestAdvFollowMainAndSubThenHide(t *testing.T) {
	f := newFixture(t)
	f.viewOn(t, f.main.AgentDeckID)
	if err := f.srv.view.Open(f.sub.AgentDeckID); err != nil {
		t.Fatal(err)
	}
	if p, why := f.srv.followedProject(); p != "alpha" || why != "" {
		t.Fatalf("子 agent 在看：%q %q", p, why)
	}
	f.srv.view.Open(f.other.AgentDeckID)
	if p, _ := f.srv.followedProject(); p != "beta" {
		t.Fatalf("%q", p)
	}
	f.srv.view.Hide(f.other.AgentDeckID) // 焦点回到上一个打开的
	if p, why := f.srv.followedProject(); p != "alpha" || why != "" {
		t.Fatalf("隐藏后：%q %q", p, why)
	}
	f.srv.view.Hide("")
	f.srv.view.Hide("")
	st := f.srv.view.State()
	p, why := f.srv.followedProject()
	t.Logf("全隐藏后 state=%+v -> %q %q", st, p, why)
	if p != "" || why != "no_session" {
		t.Fatalf("应是 no_session：%q %q", p, why)
	}
}

// 1c 焦点是 Idle 常量、空串、注册表里没有的会话：不 panic，no_session。
func TestAdvFollowIdleAndUnknownFocus(t *testing.T) {
	f := newFixture(t)
	l, _ := lease.Open(filepath.Join(t.TempDir(), "l.json"))
	t.Cleanup(func() { l.Close() })
	c := &typingCenter{ro: true, shown: viewstate.Idle, last: time.Now()}
	lookup := func(id string) (viewstate.Target, bool) {
		switch id {
		case viewstate.Idle, "ghost":
			return viewstate.Target{Session: id, Tmux: "t_" + id, Role: "main"}, true
		}
		return viewstate.Target{}, false
	}
	m := viewstate.New(c, l, lookup, func(*i18n.Error) error { return nil }, nil)
	f.srv.view = m
	for _, id := range []string{viewstate.Idle, "ghost"} {
		if err := m.Open(id); err != nil {
			t.Fatal(err)
		}
		if p, why := f.srv.followedProject(); p != "" || why != "no_session" {
			t.Fatalf("%s：%q %q", id, p, why)
		}
		code, out := f.rawGet("/v1/progress")
		if code != 200 || out["reason"] != "no_session" {
			t.Fatalf("%d %v", code, out)
		}
	}
}

// 1d Session() 先按 Key 找、后按 AgentDeckID 找：焦点（AgentDeckID）恰与另一个会话的 Key 相同时会解析到错的会话。
// 现实中 agent-deck id 与 deckd 的 key 取自不同命名空间，几乎不会撞——推测级别，这里只证明机制存在。
func TestAdvFollowKeyVsAgentDeckIDCollision(t *testing.T) {
	f := newFixture(t)
	// 另起一个 beta 会话，其 agent-deck id 恰好等于 alpha 主 agent 的 key
	r, err := f.store.Reserve(deckstore.Session{Project: "beta", Role: "sub", Agent: "claude", Tool: "claude", Model: "m", Tier: deckstore.Economy, Title: "x", CreatedBy: "owner", RequestID: "req-col", Dir: "/tmp/beta", TaskSHA256: strings.Repeat("a", 64), ArgsSHA256: strings.Repeat("b", 64)}, "")
	if err != nil {
		t.Skip(err)
	}
	if err := f.store.LaunchSucceeded(r.Key, f.main.Key, "agentdeck_beta_col"); err != nil {
		t.Skipf("存储拒绝重复 id：%v", err)
	}
	f.viewOn(t, "")
	// 状态机里焦点 = lookup(beta 会话).Session = 它的 AgentDeckID = alpha 主 agent 的 Key
	if err := f.srv.view.Open(r.Key); err != nil {
		t.Skip(err)
	}
	foc := f.srv.view.State().Focus
	p, _ := f.srv.followedProject()
	t.Logf("focus=%s followed=%s（屏上实际是 beta 会话）", foc, p)
	if p != "beta" {
		t.Errorf("屏幕上是 beta 的会话，却跟到了 %q", p)
	}
}

// 2a Graph 没接 / 回调报错 / 回调为空：接口表现
func TestAdvProgressGraphFailures(t *testing.T) {
	f := newFixture(t)
	set := f.store.Settings()
	for i := range set.Projects {
		if set.Projects[i].ID == "alpha" {
			set.Projects[i].GraphRoot = t.TempDir()
		}
	}
	if err := f.store.ReplaceSettings(set); err != nil {
		t.Fatal(err)
	}
	// 回调为空（daemon 没配 projectctl）→ graph:false + note
	code, out := f.rawGet("/v1/progress?project=alpha")
	if code != 200 || out["graph"] != false || out["note"] == nil {
		t.Fatalf("progress=nil：%d %v", code, out)
	}
	// 回调报错 → 502
	f.srv.progress = func(context.Context, deckstore.ProjectDef) (any, error) { return nil, errors.New("boom \x1b[31mred\x1b[0m") }
	code, out = f.rawGet("/v1/progress?project=alpha")
	t.Logf("回调报错：%d %v", code, out)
	if code != 502 {
		t.Fatalf("%d", code)
	}
	if s, _ := out["error"].(string); strings.Contains(s, "\x1b") {
		t.Logf("（低，目前 graphProgress 从不返回 error，推测级）502 的 error 带控制序列：%q", s)
	}
	// 回调返回 nil any → 200 + null body？
	f.srv.progress = func(context.Context, deckstore.ProjectDef) (any, error) { return nil, nil }
	req, _ := http.NewRequest("GET", f.http.URL+"/v1/progress?project=alpha", nil)
	req.Header.Set("X-Test-As", "owner")
	resp, _ := http.DefaultClient.Do(req)
	var raw any
	json.NewDecoder(resp.Body).Decode(&raw)
	resp.Body.Close()
	t.Logf("回调返回 (nil,nil)：%d body=%v", resp.StatusCode, raw)
	// 回调 panic：net/http 会恢复这个连接，但进程不死
	f.srv.progress = func(context.Context, deckstore.ProjectDef) (any, error) { panic("cb panic") }
	code, _ = f.rawGet("/v1/progress?project=alpha")
	t.Logf("回调 panic：客户端看到 %d（-1 = 连接被掐）", code)
}

// 2b 回调很慢、客户端先走：回调的 ctx 应随断开取消，不留 30 秒的悬挂。
func TestAdvSlowCallbackCancelledOnClientGone(t *testing.T) {
	f := newFixture(t)
	set := f.store.Settings()
	for i := range set.Projects {
		set.Projects[i].GraphRoot = t.TempDir()
	}
	f.store.ReplaceSettings(set)
	done := make(chan error, 1)
	started := make(chan struct{})
	f.srv.progress = func(ctx context.Context, p deckstore.ProjectDef) (any, error) {
		close(started)
		select {
		case <-ctx.Done():
			done <- ctx.Err()
		case <-time.After(30 * time.Second):
			done <- errors.New("30s 仍未被取消")
		}
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", f.http.URL+"/v1/progress?project=alpha", nil)
	req.Header.Set("X-Test-As", "owner")
	go http.DefaultClient.Do(req)
	<-started
	select {
	case err := <-done:
		t.Logf("回调 ctx 结束：%v", err)
		if err.Error() == "30s 仍未被取消" {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("客户端断开 3 秒后回调的 ctx 仍未取消")
	}
}

// 2c 真正的 graphProgress：GraphRoot 指向不存在的目录（不会执行任何脚本，LoadGraph 先失败）。
func TestAdvGraphProgressUnreadableRoot(t *testing.T) {
	f := newFixture(t)
	set := f.store.Settings()
	for i := range set.Projects {
		if set.Projects[i].ID == "alpha" {
			set.Projects[i].GraphRoot = "/tmp/n069adv-B/definitely-missing-root"
		}
	}
	f.store.ReplaceSettings(set)
	f.srv.progress = graphProgress("/nonexistent/projectctl.py")
	code, out := f.rawGet("/v1/progress?project=alpha")
	t.Logf("%d %v", code, out)
	if code != 200 {
		t.Fatal(code)
	}
	if out["graph"] != true {
		t.Logf("graph=%v", out["graph"])
	}
	if pr, _ := out["problems"].([]any); len(pr) == 0 {
		t.Fatalf("读不到 Graph 却没有 problems：%v", out)
	}
	// 面板端会把它画成「已验收 0 / 其余 0」加一行问题：见 panels 的 TestAdvUnreadableGraphLooksLikeEmptyProject
}

// 4 旧写法：panels.yaml 里的 `deck panel progress -project X` 在面板这一端变成 ?project=X，优先于跟随，
// 与屏上正在看的会话无关；空值等同没指定；带怪字符的项目名是 404 不是跟随；main 不能越权看别的项目。
func TestAdvProjectParamPrecedence(t *testing.T) {
	f := newFixture(t)
	f.viewOn(t, f.other.AgentDeckID) // 屏上是 beta
	for q, want := range map[string]string{"?project=alpha": "alpha", "?project=beta": "beta"} {
		if code, out := f.rawGet("/v1/progress" + q); code != 200 || out["project"] != want || out["follow"] == true {
			t.Fatalf("%s：%d %v", q, code, out)
		}
	}
	if code, out := f.rawGet("/v1/progress?project="); code != 200 || out["project"] != "beta" {
		t.Fatalf("空值应等同没指定（跟随）：%d %v", code, out)
	}
	for _, q := range []string{"?project=alpha%20", "?project=ALPHA", "?project=alpha&project=beta%00", "?project=%00"} {
		code, out := f.rawGet("/v1/progress" + q)
		t.Logf("%s -> %d %v", q, code, out["project"])
		if q == "?project=alpha&project=beta%00" {
			continue
		}
		if code != 404 {
			t.Errorf("%s：想要 404，得到 %d", q, code)
		}
	}
	// main 跟自己的项目；越权 403；main 不被跟随带走
	req, _ := http.NewRequest("GET", f.http.URL+"/v1/progress?project=beta", nil)
	req.Header.Set("X-Test-As", "main")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("main 看别的项目应 403：%d", resp.StatusCode)
	}
}
