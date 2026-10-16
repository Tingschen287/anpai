package deckd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

type fixture struct {
	t     *testing.T
	store *deckstore.Store
	srv   *Server
	http  *httptest.Server
	main  deckstore.Session
	sub   deckstore.Session
	other deckstore.Session // 另一个项目的主 agent
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	dirs := deckstore.Dirs{Config: filepath.Join(root, "cfg"), State: filepath.Join(root, "state")}
	st, err := deckstore.Open(dirs)
	if err != nil {
		t.Fatal(err)
	}
	set := st.Settings()
	for _, id := range []string{"alpha", "beta"} {
		set.Projects = append(set.Projects, deckstore.ProjectDef{ID: id, Name: id, Path: "/tmp/" + id,
			Agents: []string{"claude"}, Models: map[deckstore.Tier]deckstore.ModelList{deckstore.Frontier: {"claude-opus-5-5"}},
			MainAgent: deckstore.MainAgent{Agent: "claude", Tier: deckstore.Frontier}, Envelope: deckstore.DefaultEnvelope(id)})
	}
	if err := st.ReplaceSettings(set); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, store: st}
	mk := func(project, role, tmux string) deckstore.Session {
		r, err := st.Reserve(deckstore.Session{Project: project, Role: role, Agent: "claude", Tool: "claude", Model: "m", Tier: deckstore.Economy, Title: role, CreatedBy: "owner", RequestID: "req-" + tmux, Dir: "/tmp/" + project, TaskSHA256: strings.Repeat("a", 64), ArgsSHA256: strings.Repeat("b", 64)}, "")
		if err != nil {
			t.Fatal(err)
		}
		st.LaunchSucceeded(r.Key, "ad-"+tmux, tmux)
		x, _ := st.Session(r.Key)
		return x
	}
	f.main = mk("alpha", "main", "agentdeck_alpha_main")
	f.sub = mk("alpha", "sub", "agentdeck_alpha_sub")
	f.other = mk("beta", "main", "agentdeck_beta_main")
	f.srv = NewServer(Config{Dirs: dirs}, st, nil, nil, nil, nil)
	// 测试里按请求头指定调用者；生产路径没有这个口子。
	f.srv.callerFn = func(r *http.Request) Caller {
		switch r.Header.Get("X-Test-As") {
		case "owner":
			return Caller{Role: RoleOwner, Pane: "/s/deck|%1"}
		case "main":
			return Caller{Role: RoleMain, Project: "alpha", Session: f.main.Key}
		case "sub":
			return Caller{Role: RoleSub, Project: "alpha", Session: f.sub.Key}
		case "betamain":
			return Caller{Role: RoleMain, Project: "beta", Session: f.other.Key}
		case "broken-id":
			return Caller{Role: RoleOwner, Err: errors.New("进程已消失")}
		}
		return Caller{Role: RoleUnknown}
	}
	f.http = httptest.NewServer(f.srv.Handler())
	t.Cleanup(f.http.Close)
	return f
}

func (f *fixture) do(as, method, path, body string) (int, string) {
	f.t.Helper()
	req, _ := http.NewRequest(method, f.http.URL+path, strings.NewReader(body))
	req.Header.Set("X-Test-As", as)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// 权限矩阵：每个端点 × 每种角色的期望状态码。
func TestPermissionMatrix(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		method, path, body string
		want               map[string]int
	}{
		{"GET", "/v1/health", "", map[string]int{"owner": 200, "main": 200, "sub": 200, "unknown": 200}},
		{"GET", "/v1/projects", "", map[string]int{"owner": 200, "main": 200, "sub": 200, "unknown": 403}},
		{"GET", "/v1/sessions", "", map[string]int{"owner": 200, "main": 200, "sub": 200, "unknown": 403}},
		{"GET", "/v1/snapshot", "", map[string]int{"owner": 200, "main": 200, "sub": 403, "unknown": 403}},
		{"GET", "/v1/decisions", "", map[string]int{"owner": 200, "main": 200, "sub": 403, "unknown": 403}},
		{"GET", "/v1/notifications", "", map[string]int{"owner": 200, "main": 200, "sub": 403, "unknown": 403}},
		{"GET", "/v1/settings", "", map[string]int{"owner": 200, "main": 200, "sub": 403, "unknown": 403}},
		{"GET", "/v1/trust", "", map[string]int{"owner": 200, "main": 403, "sub": 403, "unknown": 403}},
		{"PUT", "/v1/settings", "{}", map[string]int{"owner": 400, "main": 403, "sub": 403, "unknown": 403}},
		{"POST", "/v1/decisions", `{"kind":"push","summary":"推送？"}`, map[string]int{"owner": 403, "sub": 403, "unknown": 403, "main": 201}},
		{"POST", "/v1/view", `{"op":"open","session":"x"}`, map[string]int{"main": 403, "sub": 403, "unknown": 403, "owner": 503}},
		{"GET", "/v1/events?after=x", "", map[string]int{"sub": 403, "unknown": 403}},
		{"POST", "/v1/decisions/x/answer", `{"option":"approve","version":1}`, map[string]int{"main": 403, "sub": 403, "unknown": 403, "owner": 400}},
		{"POST", "/v1/decisions/x/cancel", "", map[string]int{"owner": 403, "sub": 403, "unknown": 403, "main": 400}},
		{"DELETE", "/v1/trust?project=alpha&tool=codex", "", map[string]int{"main": 403, "sub": 403, "unknown": 403, "owner": 404}},
		{"GET", "/v1/panels", "", map[string]int{"main": 403, "sub": 403, "unknown": 403, "owner": 200}},
		{"POST", "/v1/panels/quota", `{"on":false}`, map[string]int{"main": 403, "sub": 403, "unknown": 403, "owner": 200}},
		{"GET", "/v1/view", "", map[string]int{"main": 403, "sub": 403, "unknown": 403, "owner": 503}},
		{"GET", "/v1/progress?project=alpha", "", map[string]int{"sub": 403, "unknown": 403, "owner": 200, "main": 200}},
		{"GET", "/v1/whoami", "", map[string]int{"owner": 200, "main": 200, "sub": 200, "unknown": 200}},
	}
	for _, c := range cases {
		for as, want := range c.want {
			if got, body := f.do(as, c.method, c.path, c.body); got != want {
				t.Errorf("%s %s 以 %s 调用：期望 %d，得到 %d（%s）", c.method, c.path, as, want, got, strings.TrimSpace(body))
			}
		}
	}
}

// 只有 owner 界面能回答决定；主 agent、子 agent 冒充都被拒。
func TestOnlyOwnerCanAnswer(t *testing.T) {
	f := newFixture(t)
	_, body := f.do("main", "POST", "/v1/decisions", `{"kind":"cost","summary":"多开一个 frontier"}`)
	var d deckstore.Decision
	json.Unmarshal([]byte(body), &d)
	ans := fmt.Sprintf(`{"option":"approve","version":%d}`, d.Version)
	for _, as := range []string{"main", "sub", "unknown", "betamain"} {
		if code, _ := f.do(as, "POST", "/v1/decisions/"+d.ID+"/answer", ans); code != 403 {
			t.Errorf("%s 回答决定应被拒：%d", as, code)
		}
	}
	if code, b := f.do("owner", "POST", "/v1/decisions/"+d.ID+"/answer", ans); code != 200 || !strings.Contains(b, `"state":"approved"`) {
		t.Fatalf("owner 应能回答：%d %s", code, b)
	}
	// 同一版本再答：409
	if code, _ := f.do("owner", "POST", "/v1/decisions/"+d.ID+"/answer", ans); code != 400 && code != 409 {
		t.Errorf("重复回答应被拒：%d", code)
	}
}

// 身份查询失败的调用者即使「像 owner」也不能写。
func TestIdentityErrorCannotWrite(t *testing.T) {
	f := newFixture(t)
	if code, _ := f.do("broken-id", "PUT", "/v1/settings", "{}"); code != 403 {
		t.Fatalf("身份查询失败应拒绝写：%d", code)
	}
}

// 跨项目：beta 的主 agent 看不到 alpha，主 agent 也不能指定别的项目。
func TestCrossProjectAccessIsRefused(t *testing.T) {
	f := newFixture(t)
	if code, _ := f.do("main", "GET", "/v1/decisions?project=beta", ""); code != 403 {
		t.Errorf("主 agent 指定别的项目应被拒：%d", code)
	}
	_, body := f.do("betamain", "GET", "/v1/sessions", "")
	if strings.Contains(body, "alpha") {
		t.Errorf("beta 的主 agent 不该看到 alpha 的会话：%s", body)
	}
	_, body = f.do("sub", "GET", "/v1/sessions", "")
	var list []deckstore.Session
	json.Unmarshal([]byte(body), &list)
	if len(list) != 1 || list[0].Key != f.sub.Key {
		t.Errorf("子 agent 只能看到自己：%s", body)
	}
}

// 整份替换设置缺字段不当成清空：{} 被拒，原设置不变。
func TestPutSettingsRequiresBothFields(t *testing.T) {
	f := newFixture(t)
	before := len(f.store.Settings().Projects)
	for _, body := range []string{`{}`, `{"agents":[]}`, `{"projects":[]}`} {
		if got, out := f.do("owner", "PUT", "/v1/settings", body); got != 400 {
			t.Fatalf("%s 应被拒：%d %s", body, got, out)
		}
	}
	if len(f.store.Settings().Projects) != before || before == 0 {
		t.Fatalf("设置不应被改：%d → %d", before, len(f.store.Settings().Projects))
	}
}

// 请求体里夹带 project、created_by：未知字段一律拒绝，项目与创建者只由服务端推导。
func TestForgedFieldsAreRejectedAndServerDerivesIdentity(t *testing.T) {
	f := newFixture(t)
	if code, _ := f.do("main", "POST", "/v1/decisions", `{"kind":"push","summary":"x","project":"beta"}`); code != 400 {
		t.Errorf("夹带 project 应被拒：%d", code)
	}
	if code, _ := f.do("main", "POST", "/v1/decisions", `{"kind":"push","summary":"x","created_by":"owner"}`); code != 400 {
		t.Errorf("夹带 created_by 应被拒：%d", code)
	}
	_, body := f.do("main", "POST", "/v1/decisions", `{"kind":"deviation","summary":"按状态分组？","options":["按项目","按状态"]}`)
	var d deckstore.Decision
	json.Unmarshal([]byte(body), &d)
	if d.Project != "alpha" || d.CreatedBy != f.main.Key {
		t.Fatalf("项目与创建者应由服务端推导：%+v", d)
	}
}

// 主 agent 只能上报五类；codex_trust、graph_gate 由系统生成。
func TestMainAgentCannotCreateSystemKinds(t *testing.T) {
	f := newFixture(t)
	for _, k := range []string{"codex_trust", "graph_gate", "session_wait", "whatever"} {
		if code, _ := f.do("main", "POST", "/v1/decisions", fmt.Sprintf(`{"kind":%q,"summary":"x"}`, k)); code != 400 {
			t.Errorf("主 agent 不能创建 %s：%d", k, code)
		}
	}
}

// 只有创建者能撤回。
func TestCancelOnlyByCreator(t *testing.T) {
	f := newFixture(t)
	_, body := f.do("main", "POST", "/v1/decisions", `{"kind":"push","summary":"推送"}`)
	var d deckstore.Decision
	json.Unmarshal([]byte(body), &d)
	if code, _ := f.do("betamain", "POST", "/v1/decisions/"+d.ID+"/cancel", ""); code == 200 {
		t.Error("别的主 agent 不能撤回")
	}
	if code, _ := f.do("main", "POST", "/v1/decisions/"+d.ID+"/cancel", ""); code != 200 {
		t.Errorf("创建者应能撤回：%d", code)
	}
}

// 存储停止后写接口返回 503。
func TestBrokenStoreReturns503(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 不受目录权限限制")
	}
	f := newFixture(t)
	state := filepath.Join(filepath.Dir(f.srv.cfg.Dirs.Config), "state")
	os.Chmod(state, 0o500)
	defer os.Chmod(state, 0o700)
	if code, _ := f.do("main", "POST", "/v1/decisions", `{"kind":"push","summary":"x"}`); code != 503 {
		t.Fatalf("存储写失败应 503：%d", code)
	}
	os.Chmod(state, 0o700)
	if code, body := f.do("owner", "GET", "/v1/health", ""); code != 200 || !strings.Contains(body, "broken") {
		t.Errorf("health 应报告存储停止：%s", body)
	}
}

// SSE：快照的 event_id 之后的变化能续接；代号不对就 resync。
func TestEventsResumeAfterSnapshotAndResyncOnUnknownID(t *testing.T) {
	f := newFixture(t)
	_, body := f.do("owner", "GET", "/v1/snapshot", "")
	var snap Snapshot
	json.Unmarshal([]byte(body), &snap)
	f.do("main", "POST", "/v1/decisions", `{"kind":"push","summary":"推送"}`)
	evs := readEvents(t, f, "owner", snap.EventID, 2)
	if len(evs) < 1 || evs[0] != "decision" {
		t.Fatalf("快照之后的变化应续接到：%v", evs)
	}
	evs = readEvents(t, f, "owner", "deadbeef:3", 1)
	if len(evs) != 1 || evs[0] != "resync" {
		t.Fatalf("代号不对应 resync：%v", evs)
	}
	// 主 agent 只收本项目事件
	f.do("betamain", "POST", "/v1/decisions", `{"kind":"push","summary":"beta 推送"}`)
	_, body = f.do("main", "GET", "/v1/snapshot", "")
	json.Unmarshal([]byte(body), &snap)
	for _, d := range snap.Decisions {
		if d.Project != "alpha" {
			t.Errorf("主 agent 的快照混入了别的项目：%+v", d)
		}
	}
}

func readEvents(t *testing.T, f *fixture, as, after string, n int) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", f.http.URL+"/v1/events", nil)
	req.Header.Set("X-Test-As", as)
	req.Header.Set("Last-Event-ID", after)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var kinds []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() && len(kinds) < n {
		if k, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
			kinds = append(kinds, k)
		}
	}
	return kinds
}

// 真实守护进程：unix socket、0600 权限、单实例锁、真实身份判定（测试进程不在任何窗格里 → 未知）。
func TestRealDaemonOverUnixSocket(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "deckd")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "xdgstate"))
	cfg := Config{Dirs: deckstore.Dirs{Config: filepath.Join(root, "cfg"), State: filepath.Join(root, "state")},
		LayoutSocket: filepath.Join(root, "no-layout"), AgentSocket: filepath.Join(root, "no-agent"), DashboardURL: "http://127.0.0.1:1"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, nil) }()
	sock := SocketPath(cfg.Dirs)
	var c *http.Client
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("socket 没出现：%v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("socket 权限应为 0600：%v", fi.Mode().Perm())
	}
	c = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	get := func(path string) (int, string) {
		resp, err := c.Get("http://deckd" + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get("/v1/whoami"); code != 200 || !strings.Contains(body, `"role":"unknown"`) {
		t.Errorf("测试进程不在任何窗格里，应判为未知：%s", body)
	}
	if code, _ := get("/v1/sessions"); code != 403 {
		t.Errorf("未知调用者不能读会话：%d", code)
	}
	if code, body := get("/v1/quota"); code != 200 || !strings.Contains(body, `"status":"unknown"`) {
		t.Errorf("看板取不到时额度应为未知：%d %s", code, body)
	}
	// 第二个实例起不来
	if err := Run(context.Background(), cfg, nil); err == nil || !strings.Contains(err.Error(), "已经在运行") {
		t.Errorf("第二个 deckd 应拒绝启动：%v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("deckd 退出出错：%v", err)
	}
	if _, err := os.Stat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Error("退出后应清掉 socket")
	}
}

// 请求体超长、对象后带多余内容：拒绝。
func TestStrictBodyParsing(t *testing.T) {
	f := newFixture(t)
	if code, _ := f.do("main", "POST", "/v1/decisions", `{"kind":"push","summary":"x"}{"kind":"push"}`); code != 400 {
		t.Errorf("对象后多余内容应被拒：%d", code)
	}
	big := `{"kind":"push","summary":"` + strings.Repeat("a", 70<<10) + `"}`
	if code, _ := f.do("main", "POST", "/v1/decisions", big); code != 400 {
		t.Errorf("超长请求体应被拒：%d", code)
	}
}
