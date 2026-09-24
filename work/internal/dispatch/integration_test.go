package dispatch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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

	// shell 没有模型可选，用 economy 档占位；本测试验的是链路，不是模型能力。
	tbl, err := NewRouteTable([]Model{
		{ID: "shell", Tool: "shell", Class: Economy},
		{ID: "pretend-frontier", Tool: "shell", Class: Frontier},
	})
	if err != nil {
		t.Fatal(err)
	}
	leases, err := lease.Open(filepath.Join(t.TempDir(), "leases.json"))
	if err != nil {
		t.Fatal(err)
	}
	d := &Dispatcher{Deck: shellLauncher{arbiter.Deck{Client: client, Leases: leases, Clients: client}}, Store: store, Table: tbl, LogPath: logPath,
		RoutingLog: filepath.Join(t.TempDir(), "routing-decisions.jsonl")}

	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte("# 夹具项目\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 先试三次必须被拒的，确认拒绝路径在真实链路上同样生效。
	refusals := []struct {
		name string
		req  Request
		want string
	}{
		{"模型未知", Request{NodeID: "e2e", Attempt: 1, RequiredClass: Economy, ModelID: "查无此模型", ProjectPath: project}, "不猜默认值"},
		{"降档", Request{NodeID: "e2e", Attempt: 1, RequiredClass: Frontier, ModelID: "shell", ProjectPath: project}, "拒绝降档"},
		{"route 未知", Request{NodeID: "e2e", Attempt: 1, RequiredClass: RouteClass("瞎写的档"), ModelID: "shell", ProjectPath: project}, "不认识"},
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

	// 再派一次真的。
	b, err := d.Dispatch(context.Background(), Request{
		NodeID: "e2e_node", Attempt: 1, RequiredClass: Economy,
		ModelID: "shell", ProjectPath: project, Title: "n036e2e",
		DispatchedBy: "integration-test", ChosenBy: "owner", ChoiceReason: "测试里指定",
	})
	if err != nil {
		t.Fatalf("真实派工失败：%v", err)
	}
	if b.SessionID == "" {
		t.Fatal("没拿到真实会话号")
	}
	if b.Profile != profile {
		t.Errorf("会话落在 profile %q，期望 %q", b.Profile, profile)
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
		ModelID: "shell", ProjectPath: project,
	}); err == nil {
		t.Error("重复派工应当被拒绝")
	}

	// 派工流水要记全：三条拒绝 + 一条成功 + 一条重复拒绝。
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 5 {
		t.Errorf("派工流水应有 5 行，实际 %d 行", len(lines))
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

// shellLauncher 把 shell 这种没有模型的工具接到派工器上。
//
// 派工器坚持要模型，adapter 允许显式声明「这工具没有模型」，两者在这里对接：
// 模型 ID 仍然记进绑定与流水，只是不往上游传 --model。
//
// 投递与起会话都经 arbiter.Deck：新会话登记写入权，投递用「自动」许可，与发布代码同一条路。
type shellLauncher struct{ d arbiter.Deck }

func (s shellLauncher) SendMessage(ctx context.Context, sessionID, message string) error {
	return s.d.SendMessage(ctx, sessionID, message)
}

func (s shellLauncher) Launch(ctx context.Context, req agentdeck.LaunchRequest) (agentdeck.LaunchResult, error) {
	if req.Tool == "shell" {
		req.Model = ""
		req.ModelNotApplicable = true
	}
	return s.d.Launch(ctx, req)
}
