package deckd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/lease"
)

// n051：开子 agent 的准入、发话、看输出、起主 agent、汇报转告。

type fakeUp struct {
	mu        sync.Mutex
	launches  []agentdeck.LaunchRequest
	sends     []string // "id|text"
	mainSends []string
	n         int
	launchErr error
	launchOut *agentdeck.LaunchResult
	sendErr   error
	mainErr   error
	output    string
	sendHook  func(ctx context.Context) // 测试：送消息时先调它（模拟上游等 CLI 就绪）
	subBusy   bool                      // 测试：子 agent 还在跑，SendIfIdle 什么都不送
	stops     []string
	stopErr   error
	paneErr   error // 测试：取画面失败（n055）
	paneHook  func() // 测试：取画面时先调它（不持锁；n055 评审 1 B2：卡在登记之后、看完画面之前）
}

func (f *fakeUp) StopSession(_ context.Context, p lease.Permit, id string) error {
	if err := p.Allows(id); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops = append(f.stops, id)
	return f.stopErr
}

func (f *fakeUp) Launch(_ context.Context, req agentdeck.LaunchRequest) (agentdeck.LaunchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.launches = append(f.launches, req)
	if f.launchOut != nil {
		return *f.launchOut, f.launchErr
	}
	f.n++
	return agentdeck.LaunchResult{SessionID: fmt.Sprintf("ad-new%d", f.n), Success: true, Tool: req.Tool}, f.launchErr
}
func (f *fakeUp) ListSessions(context.Context) ([]agentdeck.ListedSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []agentdeck.ListedSession
	for i := 1; i <= f.n; i++ {
		out = append(out, agentdeck.ListedSession{ID: fmt.Sprintf("ad-new%d", i), TmuxSession: fmt.Sprintf("agentdeck_new%d", i)})
	}
	return out, nil
}
func (f *fakeUp) SendMessage(ctx context.Context, p lease.Permit, id, msg string) error {
	if err := p.Allows(id); err != nil {
		return err
	}
	if f.sendHook != nil {
		f.sendHook(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, id+"|"+msg)
	return f.sendErr
}

// SendIfIdle：subBusy 时像上游一样在打字前放弃（ErrTargetBusy，什么都没送）；sendHook 在判断忙不忙之前跑。
func (f *fakeUp) SendIfIdle(ctx context.Context, p lease.Permit, id, msg string) error {
	if err := p.Allows(id); err != nil {
		return err
	}
	if f.sendHook != nil {
		f.sendHook(ctx)
	}
	f.mu.Lock()
	busy := f.subBusy
	f.mu.Unlock()
	if busy {
		return agentdeck.ErrTargetBusy
	}
	hook := f.sendHook
	f.sendHook = nil
	defer func() { f.sendHook = hook }()
	return f.SendMessage(ctx, p, id, msg)
}
func (f *fakeUp) SendToMain(_ context.Context, p lease.Permit, id, msg string) error {
	if err := p.Allows(id); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mainErr == nil {
		f.mainSends = append(f.mainSends, id+"|"+msg)
	}
	return f.mainErr
}
func (f *fakeUp) PaneOutputPlain(context.Context, string) (string, error) {
	f.mu.Lock()
	hook := f.paneHook
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.output, f.paneErr
}

// fakeReadyScreen 是 Claude 与 Codex 的输入框都认得出的画面（n055：送第一条消息前要认出输入框，真机画面的写法）。
const fakeReadyScreen = "──────────────────────────────\n❯\u00a0\n──────────────────────────────\n› \n  ? for shortcuts"

// setOutput 换掉假上游的画面（n055：后台送 brief 的循环会并发读它）。
func (f *fakeUp) setOutput(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.output = s
}

// sendCount 是已送出的消息条数（加锁读）。
func (f *fakeUp) sendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sends)
}

type agentWorld struct {
	*fixture
	up      *fakeUp
	leases  *lease.Store
	version string
	// 上游侧绕过检查的假结果：按工具给配置检查的错误；启动后命令行核对的错误
	cfgBypass  map[string]error
	procBypass error
	procWant   []agentdeck.LaunchArgs // 启动后核对收到的白名单依据
	gone       map[string]bool        // 测试：这些 tmux 会话已经不在
}

func newAgentWorld(t *testing.T) *agentWorld {
	f := newFixture(t)
	set := f.store.Settings()
	for i := range set.Projects {
		set.Projects[i].Agents = []string{"claude", "codex"}
		set.Projects[i].Models = map[deckstore.Tier]string{deckstore.Frontier: "claude-opus-5-5", deckstore.Balanced: "claude-sonnet-5", deckstore.Economy: "gpt-5.6-luna"}
	}
	if err := f.store.ReplaceSettings(set); err != nil {
		t.Fatal(err)
	}
	l, err := lease.Open(filepath.Join(t.TempDir(), "leases.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	w := &agentWorld{fixture: f, up: &fakeUp{output: "\x1b[31mhello\x1b[0m done\n" + fakeReadyScreen}, leases: l, version: "codex 1.0"}
	f.srv.WithAgents(w.up, l, func(_ context.Context, bin string) (string, string, error) {
		return "/usr/bin/" + bin, w.version, nil
	}, nil)
	// 送 brief 前看画面的节奏换成很短的（n055）
	f.srv.agents.startup = startupTiming{poll: time.Millisecond, settle: 50 * time.Millisecond, await: 2 * time.Second}
	// 上游侧绕过检查换成可控的假检查（不读本机真实的 agent-deck 配置）
	f.srv.WithBypassChecks(BypassChecks{
		Config: func(tool string) error { return w.cfgBypass[tool] },
		Processes: func(_ context.Context, tmux string, want agentdeck.LaunchArgs) error {
			w.procWant = append(w.procWant, want)
			return w.procBypass
		},
		Alive: func(_ context.Context, tmux string) (bool, error) {
			if w.gone[tmux] {
				return false, nil
			}
			return true, nil
		},
	})
	return w
}

// spawnBody 按 deck agent spawn 的算法填指纹。
func spawnJSON(tier, title, task, req, dir, exception string, agentID, model string) string {
	b := spawnBody{Tier: tier, Title: title, Task: task, RequestID: req, Dir: dir, Exception: exception, TaskSHA256: TaskSHA256(task)}
	b.ArgsSHA256 = SpawnArgsSHA256(agentID, tier, model, title, dir, "")
	out, _ := json.Marshal(b)
	return string(out)
}

func decode(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("不是 JSON：%s", body)
	}
	return m
}

func TestSpawnHappyPathOrdinaryChannel(t *testing.T) {
	w := newAgentWorld(t)
	code, body := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "写测试", "给 foo 写测试", "r1", "/tmp/alpha", "", "claude", "claude-sonnet-5"))
	if code != 201 {
		t.Fatalf("应开成：%d %s", code, body)
	}
	m := decode(t, body)
	sess := m["session"].(map[string]any)
	if sess["parent"] != w.main.Key || sess["status"] != deckstore.StatusRunning || sess["tmux_session"] != "agentdeck_new1" || sess["graph"] != nil {
		t.Fatalf("注册表记录不对：%v", sess)
	}
	if m["brief_delivered"] != true {
		t.Fatalf("brief 应送达：%v", m)
	}
	lr := w.up.launches[0]
	if lr.Tool != "claude" || lr.Model != "claude-sonnet-5" || lr.ProjectPath != "/tmp/alpha" || !lr.NoWait || lr.Group != "alpha" {
		t.Fatalf("启动参数不对：%+v", lr)
	}
	if len(w.up.sends) != 1 || !strings.Contains(w.up.sends[0], "给 foo 写测试") || !strings.Contains(w.up.sends[0], "deck agent report --done") {
		t.Fatalf("brief 应含任务与收尾约定：%v", w.up.sends)
	}
	if l, _ := w.leases.Get("ad-new1"); l.Holder != lease.HolderWorkbench {
		t.Fatalf("子 agent 的写入权应登记给工作台：%+v", l)
	}
	ns := w.store.Notifications(0, "alpha")
	if len(ns) == 0 || !strings.Contains(ns[len(ns)-1].Text, "主 agent开了子 agent「写测试」") {
		t.Fatalf("应通知开了子 agent：%v", ns)
	}
}

func TestSpawnHardRejectsMakeNoDecision(t *testing.T) {
	w := newAgentWorld(t)
	cases := map[string]string{
		"绕过参数":    spawnJSON("balanced", "do it --dangerously-skip-permissions", "t", "r1", "/tmp/alpha", "", "claude", "claude-sonnet-5"),
		"档位没配":    spawnJSON("nope", "x", "t", "r2", "/tmp/alpha", "", "claude", ""),
		"相对目录":    spawnJSON("balanced", "x", "t", "r3", "rel/dir", "", "claude", "claude-sonnet-5"),
		"参数指纹对不上": strings.Replace(spawnJSON("balanced", "x", "t", "r4", "/tmp/alpha", "", "claude", "claude-sonnet-5"), `"title":"x"`, `"title":"y"`, 1),
		"任务为空":    spawnJSON("balanced", "x", " ", "r5", "/tmp/alpha", "", "claude", "claude-sonnet-5"),
	}
	for name, body := range cases {
		if code, b := w.do("main", "POST", "/v1/agents", body); code != 400 {
			t.Errorf("%s：应硬拒绝 400：%d %s", name, code, b)
		}
	}
	if len(w.store.Decisions("alpha")) != 0 || len(w.up.launches) != 0 {
		t.Fatal("硬拒绝不应生成决定、不应启动")
	}
	for _, as := range []string{"sub", "unknown", "broken-id"} {
		if code, _ := w.do(as, "POST", "/v1/agents", spawnJSON("balanced", "x", "t", "r9", "/tmp/alpha", "", "claude", "claude-sonnet-5")); code != 403 {
			t.Errorf("%s 不能开子 agent：%d", as, code)
		}
	}
	// 主 agent 不能替别的项目开
	var other spawnBody
	json.Unmarshal([]byte(spawnJSON("balanced", "x", "t", "r10", "/tmp/alpha", "", "claude", "claude-sonnet-5")), &other)
	other.Project = "beta"
	ob, _ := json.Marshal(other)
	if code, _ := w.do("main", "POST", "/v1/agents", string(ob)); code != 403 {
		t.Errorf("主 agent 不能在其他项目开：%d", code)
	}
}

func TestSpawnOverEnvelopeNeedsApprovedExceptionOnce(t *testing.T) {
	w := newAgentWorld(t)
	body := spawnJSON("frontier", "难题", "t", "rf1", "/tmp/alpha", "", "claude", "claude-opus-5-5") // 默认包络 frontier 0 个
	code, b := w.do("main", "POST", "/v1/agents", body)
	if code != 202 {
		t.Fatalf("超出包络应生成 cost 决定：%d %s", code, b)
	}
	dec := decode(t, b)["decision"].(map[string]any)
	if dec["kind"] != deckstore.KindCost || len(w.up.launches) != 0 {
		t.Fatalf("应是 cost 决定、还没启动：%v", dec)
	}
	id := dec["id"].(string)
	// 未批准就带例外：拒
	if code, _ := w.do("main", "POST", "/v1/agents", spawnJSON("frontier", "难题", "t", "rf1", "/tmp/alpha", id, "claude", "claude-opus-5-5")); code != 409 {
		t.Fatalf("未批准的例外不能用：%d", code)
	}
	if code, b := w.do("owner", "POST", "/v1/decisions/"+id+"/answer", fmt.Sprintf(`{"option":"approve","version":%v}`, dec["version"])); code != 200 {
		t.Fatalf("owner 批准：%d %s", code, b)
	}
	// 批准的是另一个请求（换了标题）：作废
	if code, _ := w.do("main", "POST", "/v1/agents", spawnJSON("frontier", "别的", "t", "rf1", "/tmp/alpha", id, "claude", "claude-opus-5-5")); code != 409 {
		t.Fatalf("范围不一致应拒：%d", code)
	}
	d, _ := w.store.Decision(id)
	if d.State != deckstore.StateStale {
		t.Fatalf("范围不一致应作废：%s", d.State)
	}
	// 重新走一遍：新决定、批准、同一请求带例外
	code, b = w.do("main", "POST", "/v1/agents", spawnJSON("frontier", "难题", "t", "rf2", "/tmp/alpha", "", "claude", "claude-opus-5-5"))
	dec = decode(t, b)["decision"].(map[string]any)
	id = dec["id"].(string)
	w.do("owner", "POST", "/v1/decisions/"+id+"/answer", fmt.Sprintf(`{"option":"approve","version":%v}`, dec["version"]))
	if code, b := w.do("main", "POST", "/v1/agents", spawnJSON("frontier", "难题", "t", "rf2", "/tmp/alpha", id, "claude", "claude-opus-5-5")); code != 201 {
		t.Fatalf("批准后带同一请求与例外应开成：%d %s", code, b)
	}
	if d, _ := w.store.Decision(id); d.State != deckstore.StateApplied {
		t.Fatalf("例外应标为已用：%s", d.State)
	}
	if code, _ := w.do("main", "POST", "/v1/agents", spawnJSON("frontier", "难题", "t", "rf3", "/tmp/alpha", id, "claude", "claude-opus-5-5")); code != 409 {
		t.Fatalf("例外只能用一次：%d", code)
	}
}

func TestSpawnCodexTrustRememberedPerToolVersion(t *testing.T) {
	w := newAgentWorld(t)
	codex := func(req string) string {
		return spawnJSON("economy", "codex 任务", "t", req, "/tmp/alpha", "", "codex", "gpt-5.6-luna")
	}
	code, b := w.do("main", "POST", "/v1/agents", codex("c1"))
	if code != 202 {
		t.Fatalf("第一次应问 Codex trust：%d %s", code, b)
	}
	dec := decode(t, b)["decision"].(map[string]any)
	if dec["kind"] != deckstore.KindCodexTrust || len(w.up.launches) != 0 {
		t.Fatalf("应是 codex_trust 决定、未启动：%v", dec)
	}
	if code, b := w.do("owner", "POST", "/v1/decisions/"+dec["id"].(string)+"/answer", fmt.Sprintf(`{"option":"approve","version":%v}`, dec["version"])); code != 200 || decode(t, b)["state"] != deckstore.StateApplied {
		t.Fatalf("批准即记住并标为 applied：%d %s", code, b)
	}
	if !w.store.TrustValid("alpha", "codex", "/tmp/alpha", "/tmp/alpha", "/usr/bin/codex", "codex 1.0") {
		t.Fatal("应记住授权")
	}
	if code, b := w.do("main", "POST", "/v1/agents", codex("c2")); code != 201 {
		t.Fatalf("记住后不再问：%d %s", code, b)
	}
	ns := w.store.Notifications(0, "alpha")
	found := false
	for _, n := range ns {
		if strings.Contains(n.Text, "按已记住的授权启动 Codex") {
			found = true
		}
	}
	if !found {
		t.Fatal("按记住的授权启动应记一条通知")
	}
	w.version = "codex 2.0" // 版本变了：重新问
	if code, _ := w.do("main", "POST", "/v1/agents", codex("c3")); code != 202 {
		t.Fatalf("版本变了应重新问：%d", code)
	}
	w.version = "codex 1.0"
	// 项目目录变了、实际工作目录没变：也重新问（n051 评审 1 B3，§14.3）
	set := w.store.Settings()
	for i := range set.Projects {
		if set.Projects[i].ID == "alpha" {
			set.Projects[i].Path = "/tmp/moved-alpha"
		}
	}
	if err := w.store.ReplaceSettings(set); err != nil {
		t.Fatal(err)
	}
	if code, b := w.do("main", "POST", "/v1/agents", codex("c4")); code != 202 || !strings.Contains(b, "codex_trust") {
		t.Fatalf("项目目录变了应重新问：%d %s", code, b)
	}
}

func TestSpawnLaunchFailureReleasesOrHoldsSlot(t *testing.T) {
	w := newAgentWorld(t)
	w.up.launchErr = errors.New("upstream refused")
	w.up.launchOut = &agentdeck.LaunchResult{Error: "unknown tool", Code: "INVALID"}
	code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "x", "t", "f1", "/tmp/alpha", "", "claude", "claude-sonnet-5"))
	if code != 502 || decode(t, b)["uncertain"] != false {
		t.Fatalf("上游明确拒绝是失败：%d %s", code, b)
	}
	key := decode(t, b)["session"].(string)
	if x, _ := w.store.Session(key); x.Status != deckstore.StatusFailed {
		t.Fatalf("应标为失败、释放名额：%s", x.Status)
	}
	w.up.launchOut = &agentdeck.LaunchResult{}
	w.up.launchErr = context.DeadlineExceeded
	code, b = w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "x", "t", "f2", "/tmp/alpha", "", "claude", "claude-sonnet-5"))
	key = decode(t, b)["session"].(string)
	if x, _ := w.store.Session(key); code != 502 || x.Status != deckstore.StatusUncertain {
		t.Fatalf("超时是结果不明、继续占名额：%d %s", code, x.Status)
	}
}

func TestSendAndOutputToSubAgents(t *testing.T) {
	w := newAgentWorld(t)
	w.leases.Grant(w.sub.AgentDeckID, "测试")
	if code, b := w.do("main", "POST", "/v1/agents/"+w.sub.Key+"/send", `{"text":"再跑一遍"}`); code != 200 {
		t.Fatalf("主 agent 给子 agent 发话：%d %s", code, b)
	}
	if code, _ := w.do("main", "POST", "/v1/agents/"+w.main.Key+"/send", `{"text":"x"}`); code != 403 {
		t.Fatalf("不能向主 agent 发话：%d", code)
	}
	if code, _ := w.do("betamain", "POST", "/v1/agents/"+w.sub.Key+"/send", `{"text":"x"}`); code != 403 {
		t.Fatalf("别的项目不能发话：%d", code)
	}
	if code, _ := w.do("sub", "POST", "/v1/agents/"+w.sub.Key+"/send", `{"text":"x"}`); code != 403 {
		t.Fatalf("子 agent 不能发话：%d", code)
	}
	w.leases.OwnerTakeover(w.sub.AgentDeckID, "deck")
	if code, _ := w.do("main", "POST", "/v1/agents/"+w.sub.Key+"/send", `{"text":"x"}`); code != 409 {
		t.Fatalf("owner 接管着时不能发话：%d", code)
	}
	code, b := w.do("main", "GET", "/v1/agents/"+w.sub.Key+"/output", "")
	if code != 200 || strings.Contains(b, "\\u001b") || !strings.Contains(b, "hello") {
		t.Fatalf("输出应可读、去掉控制序列：%d %s", code, b)
	}
}

func TestStartMainAgentOncePerProject(t *testing.T) {
	w := newAgentWorld(t)
	if code, _ := w.do("owner", "POST", "/v1/projects/alpha/main", `{}`); code != 409 {
		t.Fatalf("alpha 已有主 agent：%d", code)
	}
	if code, _ := w.do("main", "POST", "/v1/projects/alpha/main", `{}`); code != 403 {
		t.Fatalf("只有 owner 能起主 agent：%d", code)
	}
	set := w.store.Settings()
	g := set.Projects[0]
	g.ID, g.Name, g.Path = "gamma", "Gamma", "/tmp/gamma"
	g.Envelope = deckstore.DefaultEnvelope("gamma")
	set.Projects = append(set.Projects, g)
	if err := w.store.ReplaceSettings(set); err != nil {
		t.Fatal(err)
	}
	code, b := w.do("owner", "POST", "/v1/projects/gamma/main", `{}`)
	if code != 201 {
		t.Fatalf("起主 agent：%d %s", code, b)
	}
	sess := decode(t, b)["session"].(map[string]any)
	if sess["role"] != "main" || sess["brief_confirmed"] != true {
		t.Fatalf("主 agent 应登记并确认 brief：%v", sess)
	}
	if !strings.Contains(w.up.sends[len(w.up.sends)-1], "deck agent escalate") {
		t.Fatalf("主 agent 的 brief 应含上报用法：%v", w.up.sends)
	}
}
