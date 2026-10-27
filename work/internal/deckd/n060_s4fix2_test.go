package deckd

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
)

// n060 段 4 返工 2（Codex 复审 2 的 R1b）：核对要用「当前有效身份真能执行」的语义。
//
// 旧的可执行判断只看权限位里有没有任意一个执行位。核准的文件在核对前被改成 0645（属主不能执行、其他人能），
// 绑定核对照样放行、派工返回 201，实际的查找（Go 的 exec.LookPath、shell）却跳过它，去跑后面目录里的同名文件。
// 设置里显示、trust 门、绑定核对用的是同一个 sessionenv.LookPath，所以一处改对，三处同时对。

// 以 root 运行时 0645 仍能执行（root 只要有任一执行位就行），反例的前提不成立；测试不依赖也不使用 root。
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root 的执行权限判断不同于普通用户（有任一执行位即可）；本测试只在普通用户下有意义")
	}
}

// Codex 复审 2 的反例（stage-4/review-2/review_permission_test.go）：核准之后、绑定核对之前，核准的文件被去掉 owner 执行位（0645）。
// 此时用同一份 PATH 实际查找会跳过它、去跑后面目录里的另一个 codex，所以必须 409 拒绝，不启动，也不留下会话或占位。
func TestN060S4Fix2ApprovedOwnerExecuteRemovedBeforeBind(t *testing.T) {
	skipIfRoot(t)
	root := t.TempDir()
	old, later := filepath.Join(root, "old"), filepath.Join(root, "later")
	trusted := fakeCLI(t, old, "codex", "codex-cli 0.164.0", filepath.Join(root, "calls.log"))
	shadow := fakeCLI(t, later, "codex", "codex-cli 9.9", filepath.Join(root, "later.log"))
	t.Setenv("PATH", old+":"+later+":/usr/bin:/bin")
	w := newAgentWorld(t)
	up := &ctxUp{fakeUp: w.up}
	w.srv.agents.upstream = up
	armed := false
	w.srv.agents.toolInfo = func(ctx context.Context, bin string) (string, string, error) {
		p, v, e := scopedToolInfo(ctx, bin)
		if armed { // 查完版本、绑定核对之前：核准的文件被去掉 owner 执行位
			if err := os.Chmod(trusted, 0o645); err != nil {
				t.Fatal(err)
			}
		}
		return p, v, e
	}
	if code, b := spawnCodex(w, "a"); code != 202 {
		t.Fatalf("第一次应问 Codex trust：%d %s", code, b)
	} else {
		approveTrust(t, w, b)
	}
	before, slots := len(w.srv.store.Sessions("alpha")), w.srv.store.Slots("alpha")
	armed = true
	code, b := spawnCodex(w, "b")
	if code != 409 || !strings.Contains(b, "err.deckd.serverrunspawn.bind") {
		t.Fatalf("核准的文件 owner 已不能执行：应 409 且带 err.deckd.serverrunspawn.bind：%d %s", code, b)
	}
	if len(w.up.launches) != 0 || len(up.paths) != 0 {
		t.Fatalf("拒绝时不能启动：launches=%d paths=%q", len(w.up.launches), up.paths)
	}
	if got := len(w.srv.store.Sessions("alpha")); got != before || w.srv.store.Slots("alpha") != slots {
		t.Fatalf("拒绝时不能留下会话或占位：sessions %d→%d slots %d→%d", before, got, slots, w.srv.store.Slots("alpha"))
	}
	// 前提（不依赖被测代码）：标准库的查找确实会跳过 0645 的核准文件、改去跑后面的 codex，也就是这次若放行，实际跑的就是 shadow
	t.Setenv("PATH", old+":"+later)
	if p, err := exec.LookPath("codex"); err != nil || p != shadow {
		t.Fatalf("前提不成立：exec.LookPath 应跳过 0645 的 %s 而找到 %s：%q %v", trusted, shadow, p, err)
	}
}

// 显示与 trust 一致：owner 不能执行的文件，在设置里的「已装」查找（GET /v1/tools）里也不算找到，
// 且那里显示的路径就是 trust 门（scopedToolInfo）会核准的那一个——两处用同一套「真能执行」的判断。
func TestN060S4Fix2ToolsListAndTrustAgreeOnWhatIsExecutable(t *testing.T) {
	skipIfRoot(t)
	root := t.TempDir()
	log := filepath.Join(root, "run.log")
	early, later := filepath.Join(root, "early"), filepath.Join(root, "later")
	skipped := fakeCLI(t, early, "codex", "codex-cli 1.0", log)
	good := fakeCLI(t, later, "codex", "codex-cli 2.0", log)
	only := fakeCLI(t, filepath.Join(root, "only"), "kimi", "kimi 3.0", log)
	for _, p := range []string{skipped, only} {
		if err := os.Chmod(p, 0o645); err != nil { // owner 不能执行，other 能
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	w := newAgentWorld(t)
	w.srv.agents.toolInfo = scopedToolInfo
	w.srv.cfg.SessionEnv = sessionResolver(t, early+":"+later+":"+filepath.Dir(only))
	code, b := w.do("owner", "GET", "/v1/tools", "")
	var tools []ToolStatus
	if err := json.Unmarshal([]byte(b), &tools); err != nil || code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	by := map[string]ToolStatus{}
	for _, x := range tools {
		by[x.Tool] = x
	}
	if c := by["codex"]; !c.Installed || c.Path != good || c.Version != "codex-cli 2.0" {
		t.Fatalf("前面目录里 owner 不能执行的 codex 不算找到，应显示后面那个：%+v", c)
	}
	if k := by["kimi"]; k.Installed || k.Path != "" {
		t.Fatalf("唯一的 kimi 是 owner 不能执行的文件，不能算已装：%+v", k)
	}
	// trust 门取的是同一个文件
	ctx := agentdeck.WithSessionPath(context.Background(), early+":"+later)
	if p, v, err := scopedToolInfo(ctx, "codex"); err != nil || p != good || v != "codex-cli 2.0" {
		t.Fatalf("trust 门应与显示一致：%q %q %v", p, v, err)
	}
	// 假 CLI 都没被当成「会跑的」那个运行过（owner 不能执行的两个，运行记录里不会有它们）
	if raw, err := os.ReadFile(log); err == nil && (strings.Contains(string(raw), skipped) || strings.Contains(string(raw), only)) {
		t.Fatalf("owner 不能执行的文件被运行了：%s", raw)
	}
}

// Codex 复审 2 的 TestReview2Bind409LeavesNoSession 转正式测试：核准的文件在绑定核对前被删，409 拒绝，无启动、无会话、无占位残留。
func TestN060S4Fix2BindRefusalLeavesNoSession(t *testing.T) {
	root := t.TempDir()
	old, later := filepath.Join(root, "old"), filepath.Join(root, "later")
	trusted := fakeCLI(t, old, "codex", "codex-cli 0.164.0", filepath.Join(root, "calls.log"))
	fakeCLI(t, later, "codex", "codex-cli 9.9", filepath.Join(root, "later.log"))
	t.Setenv("PATH", old+":"+later+":/usr/bin:/bin")
	w := newAgentWorld(t)
	up := &ctxUp{fakeUp: w.up}
	w.srv.agents.upstream = up
	armed := false
	w.srv.agents.toolInfo = func(ctx context.Context, bin string) (string, string, error) {
		p, v, e := scopedToolInfo(ctx, bin)
		if armed {
			if err := os.Remove(trusted); err != nil {
				t.Fatal(err)
			}
		}
		return p, v, e
	}
	if code, b := spawnCodex(w, "a"); code != 202 {
		t.Fatalf("%d %s", code, b)
	} else {
		approveTrust(t, w, b)
	}
	before, slots := len(w.srv.store.Sessions("alpha")), w.srv.store.Slots("alpha")
	armed = true
	code, b := spawnCodex(w, "b")
	if code != 409 || !strings.Contains(b, "err.deckd.serverrunspawn.bind") {
		t.Fatalf("核准的文件已被删：应 409 且带 err.deckd.serverrunspawn.bind：%d %s", code, b)
	}
	if len(w.up.launches) != 0 || len(up.paths) != 0 || len(w.srv.store.Sessions("alpha")) != before || w.srv.store.Slots("alpha") != slots {
		t.Fatal("拒绝时不能有启动、会话或占位残留")
	}
}
