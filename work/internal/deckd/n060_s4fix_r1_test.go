package deckd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/sessionenv"
)

// n060 段 4 返工 R1：Codex trust 门核准的文件，必须就是实际启动的文件。
//
// 上游 agent-deck 起会话时按工具名在交给它的 PATH 里再找一次可执行文件。所以只让 PATH 字符串「相同」不够：
// 核准之后、启动之前，PATH 里更靠前的目录新装了同名文件，字符串一字没变，实际跑的却是另一个文件。
// 修法是把核准的那个文件所在的目录放到交给上游的 PATH 的最前面（并在启动前核对：按这份 PATH 找到的就是核准的文件）。
// 范围说明：这里只管「路径绑定」。同一个路径上的文件被替换（内容变了、符号链接被改指）是既有的信任边界，本轮不管，也不在测试里声称管了。

// approve 批准一个待办的 Codex trust 决定。
func approveTrust(t *testing.T, w *agentWorld, body string) {
	t.Helper()
	dec := decode(t, body)["decision"].(map[string]any)
	if code, b := w.do("owner", "POST", "/v1/decisions/"+dec["id"].(string)+"/answer",
		fmt.Sprintf(`{"option":"approve","version":%v}`, dec["version"])); code != 200 {
		t.Fatalf("批准：%d %s", code, b)
	}
}

func spawnCodex(w *agentWorld, req string) (int, string) {
	return w.do("main", "POST", "/v1/agents", spawnJSON("economy", "task", "t", req, "/tmp/alpha", "", "codex", "gpt-6-luna"))
}

// Codex 第 4 段审查 1 的反例（review-1/review_trust_test.go）：核准 old/codex 之后、启动之前，前面的目录里新增了同名 codex。
// 交给上游的 PATH 解析出的 codex 必须还是核准的那一个。
func TestN060S4FixTrustPathShadowBetweenCheckAndLaunch(t *testing.T) {
	root := t.TempDir()
	early, old := filepath.Join(root, "early"), filepath.Join(root, "old")
	if err := os.MkdirAll(early, 0o755); err != nil {
		t.Fatal(err)
	}
	trusted := fakeCLI(t, old, "codex", "codex-cli 0.164.0", filepath.Join(root, "calls.log"))
	t.Setenv("PATH", old+":/usr/bin:/bin")
	w := newAgentWorld(t)
	up := &ctxUp{fakeUp: w.up}
	w.srv.agents.upstream = up
	w.srv.cfg.SessionEnv = sessionResolver(t, early) // 会话 PATH：early 在最前（登录 shell 里有、deckd 自己没有），early 里起初没有 codex
	armed := false
	w.srv.agents.toolInfo = func(ctx context.Context, bin string) (string, string, error) {
		p, v, e := scopedToolInfo(ctx, bin)
		if armed { // 查完版本、交回之前：模拟并发安装，在更靠前的目录落下另一个 codex
			fakeCLI(t, early, "codex", "codex-cli 9.9", filepath.Join(root, "shadow.log"))
		}
		return p, v, e
	}
	code, b := spawnCodex(w, "r1")
	if code != 202 {
		t.Fatalf("第一次应问 Codex trust：%d %s", code, b)
	}
	approveTrust(t, w, b)
	armed = true
	if code, b = spawnCodex(w, "r2"); code != 201 {
		t.Fatalf("批准过的版本再开应直接开：%d %s", code, b)
	}
	if len(up.paths) != 1 {
		t.Fatalf("应启动一次：%q", up.paths)
	}
	if got := sessionenv.LookPath(up.paths[0], "codex"); got != trusted {
		t.Fatalf("交给上游的 PATH 解析出 %q，核准的是 %q：核准之后前面的目录新增的同名文件不能顶替它", got, trusted)
	}
	if early9 := filepath.Join(early, "codex"); sessionenv.LookPath(up.paths[0], "codex") == early9 {
		t.Fatal("被前置的新文件顶替了")
	}
	if _, err := os.Stat(filepath.Join(root, "shadow.log")); err == nil {
		t.Fatal("前置的新文件被运行过")
	}
}

// 没批准就不启动：未授权时即使前面的目录里有别的同名文件，也不会有 launch；核准目录不变也不会因为绑定逻辑绕过询问。
func TestN060S4FixUnauthorizedNeverLaunches(t *testing.T) {
	root := t.TempDir()
	early, old := filepath.Join(root, "early"), filepath.Join(root, "old")
	fakeCLI(t, early, "codex", "codex-cli 9.9", filepath.Join(root, "shadow.log"))
	fakeCLI(t, old, "codex", "codex-cli 0.164.0", filepath.Join(root, "calls.log"))
	t.Setenv("PATH", old+":/usr/bin:/bin")
	w := newAgentWorld(t)
	up := &ctxUp{fakeUp: w.up}
	w.srv.agents.upstream = up
	w.srv.agents.toolInfo = scopedToolInfo
	w.srv.cfg.SessionEnv = sessionResolver(t, early)
	for i := 0; i < 2; i++ {
		code, b := spawnCodex(w, fmt.Sprintf("u%d", i))
		if code != 202 {
			t.Fatalf("没批准应只生成决定：%d %s", code, b)
		}
	}
	if len(up.paths) != 0 || len(w.up.launches) != 0 {
		t.Fatalf("没批准不能启动：%q %d", up.paths, len(w.up.launches))
	}
	// 拒绝的决定同样不启动
	dec := decode(t, func() string { _, b := spawnCodex(w, "u2"); return b }())["decision"].(map[string]any)
	if code, b := w.do("owner", "POST", "/v1/decisions/"+dec["id"].(string)+"/answer",
		fmt.Sprintf(`{"option":"reject","version":%v}`, dec["version"])); code != 200 {
		t.Fatalf("拒绝：%d %s", code, b)
	}
	if code, _ := spawnCodex(w, "u3"); code == 201 || len(w.up.launches) != 0 {
		t.Fatalf("拒绝之后更不能启动：%d %d", code, len(w.up.launches))
	}
}

// 启动前核对：按要交给上游的 PATH 找出来的，必须就是核准的文件。核准的文件在启动前不再是可执行文件时（被删、被改了权限），
// 按这份 PATH 会找到后面目录里的另一个同名文件，必须拒绝启动，而不是让上游去启动那个。
func TestN060S4FixLaunchRefusedWhenTheApprovedFileIsNoLongerWhatLaunchResolves(t *testing.T) {
	root := t.TempDir()
	old, later := filepath.Join(root, "old"), filepath.Join(root, "later")
	trusted := fakeCLI(t, old, "codex", "codex-cli 0.164.0", filepath.Join(root, "calls.log"))
	fakeCLI(t, later, "codex", "codex-cli 7.7", filepath.Join(root, "later.log"))
	t.Setenv("PATH", old+":"+later+":/usr/bin:/bin")
	w := newAgentWorld(t)
	up := &ctxUp{fakeUp: w.up}
	w.srv.agents.upstream = up
	w.srv.cfg.SessionEnv = sessionResolver(t, old)
	vanish := false
	w.srv.agents.toolInfo = func(ctx context.Context, bin string) (string, string, error) {
		p, v, e := scopedToolInfo(ctx, bin)
		if vanish {
			os.Chmod(trusted, 0o644) // 查完之后核准的文件不再可执行
		}
		return p, v, e
	}
	code, b := spawnCodex(w, "v1")
	if code != 202 {
		t.Fatalf("%d %s", code, b)
	}
	approveTrust(t, w, b)
	vanish = true
	code, b = spawnCodex(w, "v2")
	if code == 201 || code == 202 {
		t.Fatalf("核准的文件已经不是会被启动的那个：应拒绝：%d %s", code, b)
	}
	if len(up.paths) != 0 || len(w.up.launches) != 0 {
		t.Fatalf("不能启动：%q", up.paths)
	}
	if strings.Contains(b, "later") && !strings.Contains(b, trusted) {
		t.Logf("提示里应说明核准的文件与要启动的不一致：%s", b)
	}
}

// 没开会话 PATH 解析时（测试、deckd 的 Config.SessionPath 为假）：不读登录 shell，但绑定照样做——核准的目录放到进程自己的 PATH 的最前面再交给上游。
func TestN060S4FixBindingAlsoWorksWithoutTheLoginShellProbe(t *testing.T) {
	root := t.TempDir()
	early, old := filepath.Join(root, "early"), filepath.Join(root, "old")
	if err := os.MkdirAll(early, 0o755); err != nil {
		t.Fatal(err)
	}
	trusted := fakeCLI(t, old, "codex", "codex-cli 0.164.0", filepath.Join(root, "calls.log"))
	t.Setenv("PATH", early+":"+old+":/usr/bin:/bin") // deckd 自己的 PATH：early 在前（起初没有 codex）
	w := newAgentWorld(t)
	up := &ctxUp{fakeUp: w.up}
	w.srv.agents.upstream = up
	armed := false
	w.srv.agents.toolInfo = func(ctx context.Context, bin string) (string, string, error) {
		p, v, e := trustedInfoAt(trusted)
		if armed {
			fakeCLI(t, early, "codex", "codex-cli 9.9", filepath.Join(root, "shadow.log"))
		}
		return p, v, e
	}
	code, b := spawnCodex(w, "n1")
	if code != 202 {
		t.Fatalf("%d %s", code, b)
	}
	approveTrust(t, w, b)
	armed = true
	if code, b = spawnCodex(w, "n2"); code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	if len(up.paths) != 1 || up.paths[0] == "" {
		t.Fatalf("没开会话 PATH 解析也要把核准的目录前置后交给上游：%q", up.paths)
	}
	if got := sessionenv.LookPath(up.paths[0], "codex"); got != trusted {
		t.Fatalf("解析出 %q，核准的是 %q", got, trusted)
	}
}

func trustedInfoAt(p string) (string, string, error) { return p, "codex-cli 0.164.0", nil }
