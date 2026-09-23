package projectctl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 这些子命令会写事件账本或重写状态投影。Work Deck 本轮一个都不能碰。
var writeCommands = []string{
	"init", "status", "render", "start", "submit", "review", "block", "resume", "fail",
}

func TestReadOnlyAllowListIsExactlyTheFourSafeCommands(t *testing.T) {
	want := map[string]bool{"validate": true, "next": true, "brief": true, "verify-chain": true}
	if len(ReadOnlyCommands) != len(want) {
		t.Fatalf("只读白名单有 %d 条，期望 %d 条：%v", len(ReadOnlyCommands), len(want), ReadOnlyCommands)
	}
	for _, c := range ReadOnlyCommands {
		if !want[c] {
			t.Errorf("白名单里混进了非预期的子命令 %q", c)
		}
	}
}

func TestWriteCommandsAreRefused(t *testing.T) {
	c := NewClient(t.TempDir(), "/nonexistent/projectctl.py")
	for _, command := range writeCommands {
		_, err := c.run(context.Background(), command)
		var refused *ErrWriteCommandRefused
		if !errors.As(err, &refused) {
			t.Errorf("子命令 %q 应当被拒绝，实际得到 %v", command, err)
			continue
		}
		if refused.Command != command {
			t.Errorf("拒绝信息里的命令是 %q，期望 %q", refused.Command, command)
		}
	}
}

// 派工、接管和写账本这三件事，本轮不做。
// 不是靠约定，而是包里根本没有对应的方法：这里按名字挨个查一遍。
func TestPackageExposesNoDispatchAttachOrWriteMethods(t *testing.T) {
	forbidden := []string{
		"Start", "Submit", "Review", "Block", "Resume", "Fail", "Init", "Status", "Render",
		"Dispatch", "Attach", "Send", "Write", "Append",
	}
	src := packageSources(t, ".")
	for _, name := range forbidden {
		needle := ") " + name + "("
		if strings.Contains(src, needle) {
			t.Errorf("包里出现了 %s 方法，本轮不允许派工、接管或写账本", name)
		}
	}
}

// exec 只能出现在 client.go 一处。多一处就意味着绕过了白名单。
//
// 按连续文本找 "exec.Command"：别名导入或选择器中间带空白的写法看不见。
// 跟 TestEachAdapterOnlyTouchesItsOwnUpstream 是同一种查法（那条有对照 GAPS K6、K7），这条本身没做对照。
func TestExecHappensOnlyInClient(t *testing.T) {
	dir := "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "exec.Command") && name != "client.go" {
			t.Errorf("%s 里出现了 exec.Command，对 projectctl 的调用只能写在 client.go", name)
		}
	}
}

// 每个 adapter 只许碰自己那个上游，别处一个都不许碰。
//
// 仓里有两个 adapter：本包对 Graph 控制器，internal/agentdeck 对 upstream agent-deck。
// 它们各自是唯一的出口，互相之间也不越界——agentdeck 不许碰账本，
// 本包不许 exec 上游那个二进制。除这两个包之外，任何地方都不准出现 exec。
//
// 这是规则；测试执行到的是「非测试源码里按连续文本找不到 exec.Command」，
// 不进 testdata 目录。拦不住的写法见对照脚本 GAPS K6、K7。
func TestEachAdapterOnlyTouchesItsOwnUpstream(t *testing.T) {
	root := moduleRoot(t)

	// 接口标识 -> 只允许出现在这个包里。
	//
	// 这里查的是「真的在调它」的证据：账本文件名、控制器脚本、上游的 HTTP 路径
	// 和环境变量。光在注释或错误文案里提到上游的名字不算越界，那反而是在说明
	// 边界在哪；直接写 exec.Command 去 exec 它，会被下面那条 exec 检查抓住。
	// 那条检查按文本找 "exec.Command"：给 os/exec 起别名（ex.Command）它就看不见，
	// 对照脚本 GAPS K6 注入的正是这种写法，见 boundary-limits.md。
	owner := map[string]string{
		"events.jsonl":     "internal/projectctl",
		"ledger-head.json": "internal/projectctl",
		"projectctl.py":    "internal/projectctl",
		// 用上游真实的变量名，不用 AGENTDECK_ 前缀：Work Deck 自己的
		// WORKDECK_AGENTDECK_TOKEN 也含那个前缀，那是本方配置不是在碰上游。
		"AGENTDECK_PROFILE": "internal/agentdeck",
		"/api/sessions":    "internal/agentdeck",
		"/events/menu":     "internal/agentdeck",
	}
	// exec 只许出现在这两个 adapter 包**内部**。
	//
	// 早先这里按文件卡（只许 client.go），那是 n035/n036 时每个包恰好只有一个
	// exec 点的巧合。边界的本意是包：adapter 包对外只暴露 workbench 自己的接口，
	// 包内怎么分文件是实现细节。包外依然一个都不许。
	execAllowedPkg := []string{"internal/projectctl/", "internal/agentdeck/"}

	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "testdata" || info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(data)

		if strings.Contains(text, "exec.Command") {
			var inAdapter bool
			for _, pkg := range execAllowedPkg {
				if strings.HasPrefix(rel, pkg) {
					inAdapter = true
					break
				}
			}
			if !inAdapter {
				offenders = append(offenders, rel+" 含 exec.Command，只有两个 adapter 包内部可以")
			}
		}
		for needle, pkg := range owner {
			if strings.Contains(text, needle) && !strings.HasPrefix(rel, pkg+"/") {
				offenders = append(offenders, rel+" 含 "+needle+"，那是 "+pkg+" 的地盘")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Errorf("有代码越过了 adapter 边界：%v", offenders)
	}
}

func packageSources(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
	}
	return b.String()
}

// moduleRoot 从当前测试文件的位置回推到 work/ 根。
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("拿不到测试文件路径")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
