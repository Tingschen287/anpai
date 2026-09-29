package dispatch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/arbiter"
	"github.com/Tingschen287/terminal-deck/work/internal/binding"
	"github.com/Tingschen287/terminal-deck/work/internal/lease"
)

// 端到端：档位表 → 派工器 → adapter → 真实的 upstream agent-deck。
//
// 隔离同集成测试其余部分：一次性 profile（随机后缀）、临时项目目录、
// 测完清理 tmux 会话与 profile 目录。不驱动任何真实业务项目。
func TestIntegrationEndToEndDispatchAgainstRealUpstream(t *testing.T) {
	bin := os.Getenv("WORKDECK_AGENTDECK_BIN")
	if bin == "" {
		t.Skip("跳过：没有设 WORKDECK_AGENTDECK_BIN")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("跳过：WORKDECK_AGENTDECK_BIN 不可用（%v）", err)
	}

	raw := make([]byte, 4)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	profile := "_n036e2e" + hex.EncodeToString(raw)
	t.Cleanup(func() {
		out, err := exec.Command("tmux", "ls", "-F", "#{session_name}").Output()
		if err == nil {
			for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if strings.Contains(name, "n036e2e") {
					_ = exec.Command("tmux", "kill-session", "-t", name).Run()
				}
			}
		}
		if home, err := os.UserHomeDir(); err == nil {
			_ = os.RemoveAll(filepath.Join(home, ".local", "share", "agent-deck", "profiles", profile))
		}
	})

	// 上游的用户配置放临时目录（n044）：只在这里声明一个自定义 agent 工具 n044agent（命令是 cat，会把收到的任务文本
	// 原样显示在画面上），不碰 owner 的上游配置。上游先读 $XDG_CONFIG_HOME/agent-deck/config.toml（agentpaths.EffectiveConfigPath）。
	cfgHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfgHome, "agent-deck"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgHome, "agent-deck", "config.toml"), []byte("[tools.n044agent]\ncommand = \"cat\"\n\n[tools.n044sh]\ncommand = \"bash\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", cfgHome)

	client, err := agentdeck.NewClient(bin, profile, "", "")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	store, err := binding.Open(filepath.Join(dir, "bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "dispatch-log.jsonl")

	// 这些工具都没有模型可选，用 economy 档占位；本测试验的是链路，不是模型能力。
	tbl, err := NewRouteTable([]Model{
		{ID: "shell", Tool: "shell", Class: Economy, NoModel: true},
		{ID: "pretend-frontier", Tool: "shell", Class: Frontier, NoModel: true},
		// 配成 bash 的会话，上游起出来是 shell：派工器要按 launch 回执认出来（n044）。故意声明成 agent，才走得到那一道。
		{ID: "bash", Tool: "bash", Class: Economy, NoModel: true, DeclaredAgent: true},
		// 自定义工具 n044sh 的命令是 bash：上游回执写的是 n044sh，实际命令暴露它是 shell（n044 attempt 1 评审 blocker 1）。
		{ID: "custom-shell", Tool: "n044sh", Class: Economy, NoModel: true, DeclaredAgent: true},
		{ID: "undeclared", Tool: "n044agent", Class: Economy, NoModel: true},
		{ID: "agent", Tool: "n044agent", Class: Economy, NoModel: true, DeclaredAgent: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	leases, err := lease.Open(filepath.Join(t.TempDir(), "leases.json"))
	if err != nil {
		t.Fatal(err)
	}
	d := &Dispatcher{Deck: arbiter.Deck{Client: client, Leases: leases, Clients: client}, Store: store, Table: tbl, LogPath: logPath,
		RoutingLog: filepath.Join(t.TempDir(), "routing-decisions.jsonl")}

	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte("# 夹具项目\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 先试五次必须被拒的，确认拒绝路径在真实链路上同样生效，而且都拒在起会话之前。
	refusals := []struct {
		name string
		req  Request
		want string
	}{
		{"模型未知", Request{NodeID: "e2e", Attempt: 1, RequiredClass: Economy, ModelID: "查无此模型", ProjectPath: project}, "不猜默认值"},
		{"降档", Request{NodeID: "e2e", Attempt: 1, RequiredClass: Frontier, ModelID: "shell", ProjectPath: project}, "拒绝降档"},
		{"route 未知", Request{NodeID: "e2e", Attempt: 1, RequiredClass: RouteClass("瞎写的档"), ModelID: "shell", ProjectPath: project}, "不认识"},
		{"shell 不接任务", Request{NodeID: "e2e", Attempt: 1, RequiredClass: Economy, ModelID: "shell", ProjectPath: project, Message: "继续"}, "不是 agent"},
		{"自定义工具没声明 agent", Request{NodeID: "e2e", Attempt: 1, RequiredClass: Economy, ModelID: "undeclared", ProjectPath: project, Message: "继续"}, "没声明"},
	}
	for _, c := range refusals {
		if _, err := d.Dispatch(context.Background(), c.req); err == nil {
			t.Errorf("%s 应当被拒绝", c.name)
		} else if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s 的拒绝理由是 %q，期望含 %q", c.name, err.Error(), c.want)
		}
	}
	if len(store.List()) != 0 {
		t.Error("被拒绝的派工不该留下绑定")
	}
	if listed, err := client.ListSessions(context.Background()); err == nil && len(listed) != 0 {
		t.Errorf("拒在起会话之前的派工不该留下任何上游会话：%+v", listed)
	}

	// 伪装成 bash 的 shell：真的起了会话，按回执认出来，真的停掉；Graph 开工一次都没调。
	starts := 0
	_, err = d.Dispatch(context.Background(), Request{
		NodeID: "e2e_bash", RequiredClass: Economy, ModelID: "bash", ProjectPath: project, Title: "n036e2e-bash",
		Message: "继续", DispatchedBy: "integration-test", ChosenBy: "owner", ChoiceReason: "测试里指定",
		Start: func(context.Context) (int, error) { starts++; return 1, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "已停掉") || !strings.Contains(err.Error(), "Graph 没有开工") {
		t.Fatalf("伪装的 shell 应当被停掉并说明 Graph 没动：%v", err)
	}
	if starts != 0 {
		t.Errorf("伪装的 shell 不该触发 Graph 开工：%d 次", starts)
	}
	bashLog := readLog(t, logPath)
	stopped := bashLog[len(bashLog)-1]
	if !stopped.SessionStopped || stopped.SessionID == "" {
		t.Fatalf("流水应当记下被停掉的会话：%+v", stopped)
	}
	listed, err := client.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 停掉的判据是 tmux 会话没了。上游 list 对停掉的 shell 会话报 unknown（实测；自定义工具报 stopped），
	// 所以只要求它不再是在跑的状态。
	found := false
	for _, s := range listed {
		if s.ID != stopped.SessionID {
			continue
		}
		found = true
		if s.TmuxSession == "" {
			t.Fatalf("上游没给会话 %s 的 tmux 会话名，核不了停没停", s.ID)
		}
		if exec.Command("tmux", "has-session", "-t", "="+s.TmuxSession).Run() == nil {
			t.Errorf("会话 %s 的 tmux 会话 %s 还在，没停掉", s.ID, s.TmuxSession)
		}
		if s.Status == "running" || s.Status == "idle" || s.Status == "waiting" {
			t.Errorf("上游还把会话 %s 记成 %q", s.ID, s.Status)
		}
	}
	if !found {
		t.Errorf("上游存储里找不到被停掉的会话 %s", stopped.SessionID)
	}

	// 自定义工具的命令是 bash：上游回执写 n044sh，实际命令是 bash。认出来，真的停掉，不开工。
	starts = 0
	_, err = d.Dispatch(context.Background(), Request{
		NodeID: "e2e_customsh", RequiredClass: Economy, ModelID: "custom-shell", ProjectPath: project, Title: "n036e2e-customsh",
		Message: "继续", DispatchedBy: "integration-test", ChosenBy: "owner", ChoiceReason: "测试里指定",
		Start: func(context.Context) (int, error) { starts++; return 1, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "shell 解释器") || !strings.Contains(err.Error(), "已停掉") || starts != 0 {
		t.Fatalf("命令是 bash 的自定义工具应当被认出、停掉、不开工：%v starts=%d", err, starts)
	}
	csLog := readLog(t, logPath)
	if cs := csLog[len(csLog)-1]; !cs.SessionStopped {
		t.Fatalf("流水应当记下被停掉的会话：%+v", cs)
	} else if listed, err := client.ListSessions(context.Background()); err == nil {
		for _, s := range listed {
			if s.ID == cs.SessionID && exec.Command("tmux", "has-session", "-t", "="+s.TmuxSession).Run() == nil {
				t.Errorf("命令是 bash 的会话 %s 的 tmux 会话还在", s.ID)
			}
		}
	}

	// Graph 开工失败：真起来的 agent 会话没拿到任务，真的停掉；任务不送、不落绑定。
	_, err = d.Dispatch(context.Background(), Request{
		NodeID: "e2e_startfail", RequiredClass: Economy, ModelID: "agent", ProjectPath: project, Title: "n036e2e-startfail",
		Message: "继续", DispatchedBy: "integration-test", ChosenBy: "owner", ChoiceReason: "测试里指定",
		Start: func(context.Context) (int, error) { return 0, fmt.Errorf("控制器拒绝开工") },
	})
	if err == nil || !strings.Contains(err.Error(), "Graph 开工失败") || !strings.Contains(err.Error(), "已停掉") {
		t.Fatalf("开工失败应当停掉刚起的会话：%v", err)
	}
	sfLog := readLog(t, logPath)
	if sf := sfLog[len(sfLog)-1]; !sf.SessionStopped || sf.SessionID == "" {
		t.Fatalf("流水应当记下开工失败后停掉的会话：%+v", sf)
	} else if listed, err := client.ListSessions(context.Background()); err == nil {
		for _, s := range listed {
			if s.ID == sf.SessionID && exec.Command("tmux", "has-session", "-t", "="+s.TmuxSession).Run() == nil {
				t.Errorf("开工失败后会话 %s 的 tmux 会话还在", s.ID)
			}
		}
	}
	if _, ok := store.Get("e2e_startfail", 1); ok {
		t.Error("开工失败不该落绑定")
	}

	// 再派一次真的：自定义 agent 工具，先起会话、再开工（测试里的开工回调给 attempt 1）、最后送任务。
	var order []string
	b, err := d.Dispatch(context.Background(), Request{
		NodeID: "e2e_node", RequiredClass: Economy,
		ModelID: "agent", ProjectPath: project, Title: "n036e2e",
		MessageFor:   func(a int) string { return fmt.Sprintf("继续第%d次", a) },
		DispatchedBy: "integration-test", ChosenBy: "owner", ChoiceReason: "测试里指定",
		Start: func(context.Context) (int, error) { order = append(order, "start"); return 1, nil },
	})
	if err != nil {
		t.Fatalf("真实派工失败：%v", err)
	}
	if b.SessionID == "" || b.Attempt != 1 || len(order) != 1 {
		t.Fatalf("应当开工一次并绑定 attempt 1：%+v %v", b, order)
	}
	if b.Profile != profile {
		t.Errorf("会话落在 profile %q，期望 %q", b.Profile, profile)
	}
	// 上游对自定义工具只回 unverified（实测），所以看画面：cat 会把收到的任务文本显示出来。
	deadline := time.Now().Add(60 * time.Second)
	for {
		pane, _ := client.PaneOutputPlain(context.Background(), b.SessionID)
		if strings.Contains(pane, "继续第1次") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("会话画面里没有看到任务文本（delivery=%s）：%q", b.Delivery, pane)
		}
		time.Sleep(500 * time.Millisecond)
	}

	// 绑定要经得起重启。
	reopened, err := binding.Open(filepath.Join(dir, "bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.Get("e2e_node", 1)
	if !ok || got.SessionID != b.SessionID {
		t.Errorf("重启后绑定对不上：%+v", got)
	}

	// 同一次 attempt 不许再派。
	if _, err := d.Dispatch(context.Background(), Request{
		NodeID: "e2e_node", Attempt: 1, RequiredClass: Economy,
		ModelID: "agent", ProjectPath: project, Message: "继续",
	}); err == nil {
		t.Error("重复派工应当被拒绝")
	}

	// 派工流水要记全：五条拒绝 + 两条停掉的 shell（伪装、自定义命令）+ 一条开工失败 + 一条成功 + 一条重复拒绝。
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 10 {
		t.Errorf("派工流水应有 10 行，实际 %d 行", len(lines))
	}
	if !strings.Contains(string(data), b.SessionID) {
		t.Error("流水里没有成功那次的会话号")
	}

	// 把这份真实流水留给证据目录。设了环境变量才写，平时跑测试不落盘。
	if out := os.Getenv("WORKDECK_DISPATCH_LOG_OUT"); out != "" {
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, data, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("派工流水已留到 %s", out)
	}
}

// n042 之前这里有一个 shellLauncher 替身：它在 Launch 里把 shell 的模型参数改成「不适用」，
// 于是这条集成测试绿着，真实界面派 shell 却被上游拒绝（n040 终审断点 3）。
// 现在不经替身：派工器自己按配置的 NoModel 不传 --model。
