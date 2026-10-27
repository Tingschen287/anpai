package deckd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/sessionenv"
)

// n060 段 4 返工：读登录 PATH 的触发时机（owner 2026-10-27 定：启动读一次，之后按键刷新）。
// 登录 shell 一律是假的 RunFunc（带计数、可挂起、可失败），不运行 owner 的 shell 启动文件。

// probeCtl 是可控的假登录 shell：计数、可以挂起到放行、可以让它失败、登录 PATH 可以换。
type probeCtl struct {
	n     atomic.Int32
	mu    sync.Mutex
	login string
	fail  bool
	gate  chan struct{} // 非 nil：Run 要等它关闭才返回（读在进行中）
	in    chan struct{} // 非 nil：Run 进来时发一个信号
}

func (p *probeCtl) set(login string, fail bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.login, p.fail = login, fail
}

func (p *probeCtl) hold() chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.gate, p.in = make(chan struct{}), make(chan struct{}, 8)
	return p.gate
}

func (p *probeCtl) release() {
	p.mu.Lock()
	g := p.gate
	p.gate = nil
	p.mu.Unlock()
	if g != nil {
		close(g)
	}
}

func (p *probeCtl) run(ctx context.Context, _ string, args, _ []string) ([]byte, error) {
	p.n.Add(1)
	p.mu.Lock()
	gate, in, login, fail := p.gate, p.in, p.login, p.fail
	p.mu.Unlock()
	if in != nil {
		in <- struct{}{}
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if fail {
		return nil, errors.New("exit status 1")
	}
	i := strings.Index(args[3], "'ANPAI")
	rest := args[3][i+1:]
	m := rest[:strings.Index(rest, "B'")]
	return []byte("rc noise\n" + m + "B" + login + m + "E\n"), nil
}

func newProbeResolver(t *testing.T, p *probeCtl) *sessionenv.Resolver {
	t.Helper()
	sh := filepath.Join(t.TempDir(), "zsh")
	if err := os.WriteFile(sh, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := sessionenv.NewLoginResolver(p.run)
	r.Getenv = func(k string) string {
		if k == "SHELL" {
			return sh
		}
		return os.Getenv(k)
	}
	r.Environ = func() []string { return nil }
	r.Wait = 2 * time.Second
	return r
}

func idle(t *testing.T, r *sessionenv.Resolver) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if !r.State(context.Background()).Refreshing {
			return
		}
	}
	t.Fatal("读一直没有结束")
}

func toolsOf(t *testing.T, w *agentWorld) map[string]ToolStatus {
	t.Helper()
	code, b := w.do("owner", "GET", "/v1/tools", "")
	var tools []ToolStatus
	if err := json.Unmarshal([]byte(b), &tools); err != nil || code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	by := map[string]ToolStatus{}
	for _, x := range tools {
		by[x.Tool] = x
	}
	return by
}

// 登录 shell 只在两个时机运行：deckd 启动读一次，和 owner 按刷新键。打开设置（GET /v1/tools）、进加入页和 agent 页（同一个请求）、
// 派工、列模型都不运行，缓存「过期」（时间走过 11 秒、6 分钟、一天）也不运行——用计数的假 RunFunc 证明。
func TestN060S4FixOnlyStartupAndTheKeyRunTheLoginShell(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(root, "run.log")
	oldDir, newDir := filepath.Join(root, "old"), filepath.Join(root, "new")
	fakeCLI(t, oldDir, "codex", "codex-cli 0.139.0", log)
	fakeCLI(t, newDir, "codex", "codex-cli 0.164.0", log)
	t.Setenv("PATH", oldDir+":/usr/bin:/bin")
	w := newAgentWorld(t)
	w.srv.agents.toolInfo = scopedToolInfo
	p := &probeCtl{login: newDir}
	r := newProbeResolver(t, p)
	now := time.Unix(2_000_000, 0)
	var nowMu sync.Mutex
	r.Now = func() time.Time { nowMu.Lock(); defer nowMu.Unlock(); return now }
	advance := func(d time.Duration) { nowMu.Lock(); now = now.Add(d); nowMu.Unlock() }
	w.srv.cfg.SessionEnv = r
	r.Start() // deckd 启动时后台读一次
	idle(t, r)
	if p.n.Load() != 1 {
		t.Fatalf("启动读一次：%d", p.n.Load())
	}
	use := func(tag string) {
		toolsOf(t, w) // 打开设置窗口、进加入页、进 agent 页都是这个请求
		if code, b := w.do("owner", "GET", "/v1/tools/models?tool=codex", ""); code != 200 {
			t.Fatalf("列模型：%d %s", code, b)
		}
		if tag == "a" { // 子 agent 名额有限，只真开一次非 Codex 的
			if code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "x", "t", "f"+tag, "/tmp/alpha", "", "claude", "claude-sonnet-5-5")); code != 201 {
				t.Fatalf("派工：%d %s", code, b)
			}
		}
		if code, b := w.do("main", "POST", "/v1/agents", spawnJSON("economy", "x", "t", "c"+tag, "/tmp/alpha", "", "codex", "gpt-6-luna")); code != 202 && code != 201 && code != 409 {
			t.Fatalf("派 Codex（过 trust 门）：%d %s", code, b)
		}
	}
	use("a")
	for _, d := range []time.Duration{11 * time.Second, 31 * time.Second, 6 * time.Minute, 24 * time.Hour} {
		advance(d) // 以前：Fresh 10 秒、失败 30 秒、成功 5 分钟过期，下一次使用就重读
		use("b" + d.String())
	}
	if n := p.n.Load(); n != 1 {
		t.Fatalf("进页面、派工、列模型、缓存过期都不该运行登录 shell：共运行了 %d 次", n)
	}
	// 启动那次失败的，同样只读一次，不会 30 秒后自己再试
	p2 := &probeCtl{fail: true}
	r2 := newProbeResolver(t, p2)
	r2.Now = r.Now
	w.srv.cfg.SessionEnv = r2
	r2.Start()
	idle(t, r2)
	for i := 0; i < 3; i++ {
		advance(time.Minute)
		toolsOf(t, w)
	}
	if n := p2.n.Load(); n != 1 {
		t.Fatalf("启动读失败后也只读一次：%d", n)
	}
}

// 按键触发一次：重读，已装与版本换成新的；没开会话 PATH 解析时按键什么都不做；非 owner 不能按。
func TestN060S4FixTheKeyRunsItOnceAndUpdatesTheTools(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(root, "run.log")
	oldDir, newDir, newer := filepath.Join(root, "old"), filepath.Join(root, "new"), filepath.Join(root, "newer")
	fakeCLI(t, oldDir, "codex", "codex-cli 0.139.0", log)
	fakeCLI(t, newDir, "codex", "codex-cli 0.164.0", log)
	fakeCLI(t, newer, "codex", "codex-cli 0.170.0", log)
	t.Setenv("PATH", oldDir+":/usr/bin:/bin")
	w := newAgentWorld(t)
	w.srv.agents.toolInfo = scopedToolInfo
	// 没开：按键不运行任何东西
	if code, b := w.do("owner", "POST", "/v1/tools/refresh", ""); code != 202 || !strings.Contains(b, `"enabled":false`) || !strings.Contains(b, `"started":false`) {
		t.Fatalf("没开会话 PATH 解析：%d %s", code, b)
	}
	p := &probeCtl{login: newDir}
	r := newProbeResolver(t, p)
	w.srv.cfg.SessionEnv = r
	r.Start()
	idle(t, r)
	if v := toolsOf(t, w)["codex"].Version; v != "codex-cli 0.164.0" {
		t.Fatalf("启动时读到的：%q", v)
	}
	for _, as := range []string{"main", "unknown"} {
		if code, _ := w.do(as, "POST", "/v1/tools/refresh", ""); code != 403 {
			t.Fatalf("%s 不能按刷新键：%d", as, code)
		}
	}
	if p.n.Load() != 1 {
		t.Fatal("被拒绝的请求不该运行登录 shell")
	}
	p.set(newer, false) // owner 刚装了新版、改了 PATH
	if code, b := w.do("owner", "POST", "/v1/tools/refresh", ""); code != 202 || !strings.Contains(b, `"started":true`) {
		t.Fatalf("按键：%d %s", code, b)
	}
	idle(t, r)
	if n := p.n.Load(); n != 2 {
		t.Fatalf("按一次键重读一次：%d", n)
	}
	if c := toolsOf(t, w)["codex"]; c.Version != "codex-cli 0.170.0" || !strings.HasPrefix(c.Path, newer) || c.PathRefreshing {
		t.Fatalf("读完后已装与版本是新的：%+v", c)
	}
}

// 连按（同一个窗口连按、几个窗口同时按）只跑一次：读的过程中再按合并进去，不叠加。
func TestN060S4FixRepeatedKeysRunOnlyOnce(t *testing.T) {
	root := t.TempDir()
	newDir := filepath.Join(root, "new")
	fakeCLI(t, newDir, "codex", "codex-cli 0.164.0", filepath.Join(root, "run.log"))
	w := newAgentWorld(t)
	w.srv.agents.toolInfo = scopedToolInfo
	p := &probeCtl{login: newDir}
	r := newProbeResolver(t, p)
	w.srv.cfg.SessionEnv = r
	r.Start()
	idle(t, r)
	gate := p.hold()
	var wg sync.WaitGroup
	var started atomic.Int32
	for i := 0; i < 8; i++ { // 几个窗口同时连按
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, b := w.do("owner", "POST", "/v1/tools/refresh", "")
			if strings.Contains(b, `"started":true`) {
				started.Add(1)
			}
		}()
	}
	wg.Wait()
	<-p.in // 第一次读进来了
	if n := p.n.Load(); n != 2 || started.Load() != 1 {
		t.Fatalf("连按只起一次读：运行 %d 次（含启动那次），%d 个请求说自己起了新读", n, started.Load())
	}
	close(gate)
	idle(t, r)
	if code, b := w.do("owner", "POST", "/v1/tools/refresh", ""); code != 202 || !strings.Contains(b, `"started":true`) {
		t.Fatalf("读完之后再按可以再读：%d %s", code, b)
	}
	idle(t, r)
	if n := p.n.Load(); n != 3 {
		t.Fatalf("%d", n)
	}
}

// 刷新期间派工、列模型、显示都照常、用旧快照，不等慢 rc：刷新读被挂住时，这些请求照样很快返回，且带着「正在刷新」标记。
func TestN060S4FixDispatchDoesNotWaitForARefreshAndUsesTheOldSnapshot(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(root, "run.log")
	oldSnap, newer := filepath.Join(root, "oldsnap"), filepath.Join(root, "newer")
	fakeCLI(t, oldSnap, "codex", "codex-cli 0.164.0", log)
	fakeCLI(t, newer, "codex", "codex-cli 0.170.0", log)
	w := newAgentWorld(t)
	up := &ctxUp{fakeUp: w.up}
	w.srv.agents.upstream = up
	w.srv.agents.toolInfo = scopedToolInfo
	p := &probeCtl{login: oldSnap}
	r := newProbeResolver(t, p)
	r.Wait = time.Minute // 慢 rc：刷新读要很久
	w.srv.cfg.SessionEnv = r
	r.Start()
	idle(t, r)
	gate := p.hold()
	p.set(newer, false)
	if code, b := w.do("owner", "POST", "/v1/tools/refresh", ""); code != 202 {
		t.Fatalf("%d %s", code, b)
	}
	<-p.in
	t.Cleanup(p.release)
	done := make(chan struct{})
	go func() {
		defer close(done)
		start := time.Now()
		if c := toolsOf(t, w)["codex"]; c.Version != "codex-cli 0.164.0" || !c.PathRefreshing {
			t.Errorf("刷新期间显示用旧快照并标「正在刷新」：%+v", c)
		}
		if code, b := w.do("owner", "GET", "/v1/tools/models?tool=codex", ""); code != 200 {
			t.Errorf("列模型：%d %s", code, b)
		}
		// Codex 派工：trust 门取的是旧快照里的 codex 0.164.0
		code, b := w.do("main", "POST", "/v1/agents", spawnJSON("economy", "task", "t", "d1", "/tmp/alpha", "", "codex", "gpt-6-luna"))
		if code != 202 {
			t.Errorf("派工：%d %s", code, b)
		} else if sc := decode(t, b)["decision"].(map[string]any)["scope"].(map[string]any); sc["tool_version"] != "codex-cli 0.164.0" || !strings.HasPrefix(sc["tool_path"].(string), oldSnap) {
			t.Errorf("派工用的是旧快照：%v", sc)
		}
		if code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "x", "t", "d2", "/tmp/alpha", "", "claude", "claude-sonnet-5-5")); code != 201 {
			t.Errorf("派工：%d %s", code, b)
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("刷新期间派工、列模型、显示不该等慢 rc：%v", d)
		}
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("派工、列模型、显示被刷新卡住了")
	}
	if p.n.Load() != 2 {
		t.Fatalf("刷新读只有这一个：%d", p.n.Load())
	}
	p.release()
	idle(t, r)
	if c := toolsOf(t, w)["codex"]; c.Version != "codex-cli 0.170.0" || c.PathRefreshing {
		t.Fatalf("读完后换成新的：%+v", c)
	}
	_ = gate
}

// 启动时读失败：退回 deckd 自己的 PATH，并带可译的原因（界面可见）；按刷新键重试成功后恢复。
// 按键刷新失败时，已有的好快照保留，失败原因单独带出（不把 owner 习惯的目录弄丢）。
func TestN060S4FixStartupFailureIsShownAndTheKeyRecovers(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(root, "run.log")
	own, newDir := filepath.Join(root, "own"), filepath.Join(root, "new")
	fakeCLI(t, own, "codex", "codex-cli 0.150.0", log)
	fakeCLI(t, newDir, "codex", "codex-cli 0.164.0", log)
	fakeCLI(t, newDir, "kimi", "kimi 2.3.2", log)
	t.Setenv("PATH", own+":/usr/bin:/bin")
	w := newAgentWorld(t)
	w.srv.agents.toolInfo = scopedToolInfo
	p := &probeCtl{fail: true}
	r := newProbeResolver(t, p)
	w.srv.cfg.SessionEnv = r
	r.Start()
	idle(t, r)
	by := toolsOf(t, w)
	for _, x := range by {
		if x.PathSource != "process" || x.NoteCode != "err.deckd.servergettools.nopath" || x.PathRefreshing {
			t.Fatalf("启动读失败：每项带来源与原因：%+v", x)
		}
	}
	if c := by["codex"]; c.Version != "codex-cli 0.150.0" || by["kimi"].Installed {
		t.Fatalf("失败时按 deckd 自己的 PATH 找：%+v %+v", c, by["kimi"])
	}
	// 按键，读成功：恢复
	p.set(newDir, false)
	w.do("owner", "POST", "/v1/tools/refresh", "")
	idle(t, r)
	by = toolsOf(t, w)
	if c := by["codex"]; c.PathSource != "login-shell" || c.NoteCode != "" || c.Version != "codex-cli 0.164.0" || !by["kimi"].Installed {
		t.Fatalf("按键恢复：%+v %+v", c, by["kimi"])
	}
	// 之后某次刷新失败：好快照保留，失败原因单独带出
	p.set("", true)
	w.do("owner", "POST", "/v1/tools/refresh", "")
	idle(t, r)
	c := toolsOf(t, w)["codex"]
	if c.PathSource != "login-shell" || c.Version != "codex-cli 0.164.0" || c.NoteCode != "err.deckd.servergettools.refreshfail" || c.Note == "" {
		t.Fatalf("刷新失败：旧快照保留、带失败原因：%+v", c)
	}
	// 再按一次成功：失败提示消失
	p.set(newDir, false)
	w.do("owner", "POST", "/v1/tools/refresh", "")
	idle(t, r)
	if c := toolsOf(t, w)["codex"]; c.NoteCode != "" {
		t.Fatalf("成功后失败提示消失：%+v", c)
	}
}

// 第一次读还没完成就有人要用：等这一次，有上限（探针超时）；上限到了退回 deckd 自己的 PATH，不会一直等。
func TestN060S4FixFirstUseWaitsForTheFirstReadWithALimit(t *testing.T) {
	root := t.TempDir()
	newDir := filepath.Join(root, "new")
	fakeCLI(t, newDir, "codex", "codex-cli 0.164.0", filepath.Join(root, "run.log"))
	w := newAgentWorld(t)
	w.srv.agents.toolInfo = scopedToolInfo
	p := &probeCtl{login: newDir}
	gate := p.hold()
	r := newProbeResolver(t, p)
	w.srv.cfg.SessionEnv = r
	r.Start()
	<-p.in
	got := make(chan ToolStatus, 1)
	go func() { got <- toolsOf(t, w)["codex"] }()
	select {
	case <-got:
		t.Fatal("第一次读还没完成，应该等它")
	case <-time.After(150 * time.Millisecond):
	}
	close(gate)
	if c := <-got; c.Version != "codex-cli 0.164.0" || c.PathSource != "login-shell" {
		t.Fatalf("等到了第一次读的结果：%+v", c)
	}
	// 有上限：第一次读永远不返回，探针超时（Wait）到了就退回，请求不会一直挂着
	p3 := &probeCtl{login: newDir}
	p3.hold()
	r3 := newProbeResolver(t, p3)
	r3.Wait = 200 * time.Millisecond
	w.srv.cfg.SessionEnv = r3
	start := time.Now()
	c := toolsOf(t, w)["codex"]
	if d := time.Since(start); d > 3*time.Second || c.PathSource != "process" || c.NoteCode == "" {
		t.Fatalf("第一次读超时：应在上限内退回并带原因：%v %+v", d, c)
	}
	if n := p3.n.Load(); n != 1 {
		t.Fatalf("%d", n)
	}
}

// 一次派工只取一份快照：派工进行到一半，一次刷新读完、换了新快照，这次派工的 trust 门和交给上游的 launch 仍然是同一份（旧的），
// 下一次派工才用新的。
func TestN060S4FixOneSnapshotPerDispatchEvenIfARefreshLandsMidway(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(root, "run.log")
	oldDir, newDir := filepath.Join(root, "old"), filepath.Join(root, "new")
	oldCodex := fakeCLI(t, oldDir, "codex", "codex-cli 0.164.0", log)
	newCodex := fakeCLI(t, newDir, "codex", "codex-cli 0.170.0", log)
	w := newAgentWorld(t)
	up := &ctxUp{fakeUp: w.up}
	w.srv.agents.upstream = up
	p := &probeCtl{login: oldDir}
	r := newProbeResolver(t, p)
	w.srv.cfg.SessionEnv = r
	r.Start()
	idle(t, r)
	mid := false
	w.srv.agents.toolInfo = func(ctx context.Context, bin string) (string, string, error) {
		path, ver, err := scopedToolInfo(ctx, bin)
		if mid { // trust 门查完之后、launch 之前：owner 按了刷新键，读完换了快照
			p.set(newDir, false)
			r.Refresh()
			idle(t, r)
			mid = false
		}
		return path, ver, err
	}
	code, b := spawnCodex(w, "m1")
	if code != 202 {
		t.Fatalf("%d %s", code, b)
	}
	approveTrust(t, w, b)
	mid = true
	if code, b = spawnCodex(w, "m2"); code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	if got := sessionenv.LookPath(up.paths[0], "codex"); got != oldCodex {
		t.Fatalf("这次派工开始时的快照是旧的：trust 门与 launch 都该用它（%q），不是中途换上的新快照（%q）：%q", oldCodex, newCodex, got)
	}
	// 下一次派工用新快照：路径变了，旧授权不适用，重新问
	if code, b = spawnCodex(w, "m3"); code != 202 {
		t.Fatalf("新快照里 codex 是另一个文件，应重新问：%d %s", code, b)
	}
	if sc := decode(t, b)["decision"].(map[string]any)["scope"].(map[string]any); sc["tool_path"] != newCodex || sc["tool_version"] != "codex-cli 0.170.0" {
		t.Fatalf("下一次派工用新快照：%v", sc)
	}
}

// 按键之后界面轮询「读完没有」用 GET /v1/tools/refresh：只回状态，不跑任何 CLI 的 --version，也不运行登录 shell；只有 owner 能看。
func TestN060S4FixPollingTheRefreshStatusRunsNothing(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(root, "run.log")
	newDir := filepath.Join(root, "new")
	fakeCLI(t, newDir, "codex", "codex-cli 0.164.0", log)
	w := newAgentWorld(t)
	w.srv.agents.toolInfo = scopedToolInfo
	// 没开：说没开
	if code, b := w.do("owner", "GET", "/v1/tools/refresh", ""); code != 200 || !strings.Contains(b, `"enabled":false`) || !strings.Contains(b, `"refreshing":false`) {
		t.Fatalf("没开：%d %s", code, b)
	}
	p := &probeCtl{login: newDir}
	r := newProbeResolver(t, p)
	w.srv.cfg.SessionEnv = r
	r.Start()
	idle(t, r)
	before, _ := os.ReadFile(log)
	for i := 0; i < 20; i++ {
		if code, b := w.do("owner", "GET", "/v1/tools/refresh", ""); code != 200 || !strings.Contains(b, `"enabled":true`) || !strings.Contains(b, `"refreshing":false`) {
			t.Fatalf("%d %s", code, b)
		}
	}
	after, _ := os.ReadFile(log)
	if string(before) != string(after) || p.n.Load() != 1 {
		t.Fatalf("轮询状态不该跑任何 CLI 或登录 shell：CLI 日志 %d→%d 字节，登录 shell %d 次", len(before), len(after), p.n.Load())
	}
	// 读在跑时说在刷新
	p.hold()
	r.Refresh()
	<-p.in
	if code, b := w.do("owner", "GET", "/v1/tools/refresh", ""); code != 200 || !strings.Contains(b, `"refreshing":true`) {
		t.Fatalf("读在跑：%d %s", code, b)
	}
	p.release()
	idle(t, r)
	for _, as := range []string{"main", "unknown"} {
		if code, _ := w.do(as, "GET", "/v1/tools/refresh", ""); code != 403 {
			t.Fatalf("%s 不能看：%d", as, code)
		}
	}
}
