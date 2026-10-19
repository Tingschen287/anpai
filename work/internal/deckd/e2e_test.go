package deckd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/wirecheck"
)

// 端到端：真实的两个 tmux server、真实 deckd、真实身份判定。请求从各窗格里用 curl 发出。
//   - 布局 server 窗格里的进程 → owner，能回答决定；
//   - agent server 上注册表里主 agent 的窗格 → main（项目 alpha），能上报，不能回答；
//   - agent server 上未登记的窗格 → unknown；测试进程本身 → unknown。
func TestE2EIdentityFromRealPanes(t *testing.T) {
	for _, bin := range []string{"tmux", "curl"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip("没有 " + bin)
		}
	}
	root, err := os.MkdirTemp("/tmp", "dke2e")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "xdgstate"))
	tmuxDir := filepath.Join(root, "tmux")
	os.MkdirAll(tmuxDir, 0o700)
	layoutSock, agentSock := filepath.Join(tmuxDir, "deck"), filepath.Join(tmuxDir, "default")
	dirs := deckstore.Dirs{Config: filepath.Join(root, "cfg"), State: filepath.Join(root, "state")}

	// 预置：项目 alpha 与一个已登记的主 agent 会话（n051 之前没有 spawn 接口，直接写存储做夹具）
	st, err := deckstore.Open(dirs)
	if err != nil {
		t.Fatal(err)
	}
	set := st.Settings()
	set.Projects = []deckstore.ProjectDef{{ID: "alpha", Name: "alpha", Path: "/tmp/alpha", Agents: []string{"claude"},
		Models: map[deckstore.Tier]deckstore.ModelList{deckstore.Frontier: {"claude-opus-5-5"}}, MainAgent: deckstore.MainAgent{Agent: "claude", Tier: deckstore.Frontier},
		Envelope: deckstore.DefaultEnvelope("alpha")}}
	if err := st.ReplaceSettings(set); err != nil {
		t.Fatal(err)
	}
	r, _ := st.Reserve(deckstore.Session{Project: "alpha", Role: "main", Agent: "claude", Tool: "claude", Model: "claude-opus-5-5", Tier: deckstore.Frontier, Title: "主 agent", CreatedBy: "owner",
		RequestID: "req-main", Dir: "/tmp/alpha", TaskSHA256: strings.Repeat("a", 64), ArgsSHA256: strings.Repeat("b", 64)}, "")
	st.LaunchSucceeded(r.Key, "ad-main", "agentdeck_alpha_main")
	wirecheck.External("按状态分组？", "按项目") // 夹具直接写进存储的决定，不经请求体：登记为外部原文
	dec, _ := st.CreateDecision(deckstore.NewDecision{Project: "alpha", Kind: deckstore.KindDeviation, Summary: "按状态分组？", Options: []string{"按项目"}, CreatedBy: r.Key})

	tm := func(sock string, a ...string) {
		t.Helper()
		if out, err := exec.Command("tmux", append([]string{"-S", sock, "-f", "/dev/null"}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("tmux %v：%v %s", a, err, out)
		}
	}
	tm(layoutSock, "new-session", "-d", "-s", "layout", "sleep 600")
	tm(agentSock, "new-session", "-d", "-s", "agentdeck_alpha_main", "sleep 600")
	tm(agentSock, "new-session", "-d", "-s", "agentdeck_stranger", "sleep 600")
	defer exec.Command("tmux", "-S", layoutSock, "kill-server").Run()
	defer exec.Command("tmux", "-S", agentSock, "kill-server").Run()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	cfg := Config{Dirs: dirs, LayoutSocket: layoutSock, AgentSocket: agentSock, DashboardURL: "http://127.0.0.1:1"}
	go func() { done <- Run(ctx, cfg, nil) }()
	defer func() { cancel(); <-done }()
	sock := SocketPath(dirs)
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 在某个窗格里运行 curl，把响应写进文件，等它出现
	n := 0
	inPane := func(paneSock, session, method, path, body string) (int, string) {
		t.Helper()
		n++
		out := filepath.Join(root, fmt.Sprintf("out%d", n))
		curl := fmt.Sprintf("curl -s -o %s.body -w '%%{http_code}' --unix-socket %s -X %s -H 'Content-Type: application/json' --data '%s' http://deckd%s > %s.code; mv %s.code %s",
			out, sock, method, body, path, out, out, out)
		if method == "GET" {
			curl = strings.Replace(curl, " --data '"+body+"'", "", 1)
		}
		tm(paneSock, "new-window", "-d", "-t", session, curl)
		for i := 0; i < 200; i++ {
			if code, err := os.ReadFile(out); err == nil {
				b, _ := os.ReadFile(out + ".body")
				var c int
				fmt.Sscan(string(code), &c)
				return c, string(b)
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("窗格 %s 里的请求没有结果", session)
		return 0, ""
	}
	role := func(body string) string {
		var m map[string]any
		json.Unmarshal([]byte(body), &m)
		return fmt.Sprint(m["role"], "/", m["project"])
	}

	if _, b := inPane(layoutSock, "layout", "GET", "/v1/whoami", ""); role(b) != "owner/" {
		t.Errorf("布局窗格应为 owner：%s", b)
	}
	if _, b := inPane(agentSock, "agentdeck_alpha_main", "GET", "/v1/whoami", ""); role(b) != "main/alpha" {
		t.Errorf("已登记的主 agent 窗格应为 main/alpha：%s", b)
	}
	if _, b := inPane(agentSock, "agentdeck_stranger", "GET", "/v1/whoami", ""); role(b) != "unknown/" {
		t.Errorf("未登记的窗格应为 unknown：%s", b)
	}
	ans := fmt.Sprintf(`{"option":"按项目","version":%d}`, dec.Version)
	if code, b := inPane(agentSock, "agentdeck_alpha_main", "POST", "/v1/decisions/"+dec.ID+"/answer", ans); code != 403 {
		t.Errorf("主 agent 不能回答 owner 的决定：%d %s", code, b)
	}
	if code, b := inPane(agentSock, "agentdeck_stranger", "POST", "/v1/decisions/"+dec.ID+"/answer", ans); code != 403 {
		t.Errorf("未知窗格不能回答：%d %s", code, b)
	}
	if code, b := inPane(agentSock, "agentdeck_alpha_main", "POST", "/v1/decisions", `{"kind":"push","summary":"要推送"}`); code != 201 {
		t.Errorf("主 agent 应能上报：%d %s", code, b)
	}
	if code, b := inPane(layoutSock, "layout", "POST", "/v1/decisions/"+dec.ID+"/answer", ans); code != 200 || !strings.Contains(b, `"state":"answered"`) {
		t.Errorf("布局窗格里的 owner 应能回答：%d %s", code, b)
	}
}
