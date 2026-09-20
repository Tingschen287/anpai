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

// 包外的代码一律不许自己去 exec projectctl，也不许直接读账本文件。
func TestNoOneOutsideThisPackageTouchesTheLedger(t *testing.T) {
	root := moduleRoot(t)
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
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.Contains(filepath.ToSlash(path), "/internal/projectctl/") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(data)
		for _, bad := range []string{"exec.Command", "events.jsonl", "ledger-head.json", "projectctl.py"} {
			if strings.Contains(text, bad) {
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, rel+" 含 "+bad)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Errorf("projectctl 包之外出现了对控制器或账本的直接访问：%v", offenders)
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
