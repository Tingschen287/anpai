package agentdeck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// n067 返工 2：非 CLI 进程的绕过参数检查认出「绕过参数连着环境变量样式文字」的串（argallow.go checkGluedTail；返工 3 起只查最后一个 argv 元素，见 argallow_fix3_test.go）。
// 经 procArgsOK 走 /proc 形状的假目录测，两个平台都能跑。

func fakeProcWith(t *testing.T, argv ...string) string {
	t.Helper()
	proc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proc, "4242"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proc, "4242", "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	return proc
}

func TestGluedBypassFlagIsCaughtOnNonCLIProcesses(t *testing.T) {
	want := LaunchArgs{Tool: "claude", Binary: "claude"}
	for _, argv := range [][]string{
		{"bash", "-c", "x", "--dangerously-skip-permissionsPATH=/usr/bin"},
		{"mcp-server", "--dangerously-skip-permissionsXSYNTHETIC_SECRET=review-only"},
		{"mcp-server", "--allow-dangerously-skip-permissionsHOME=/x"},
		{"mcp-server", "--DANGEROUSLY-skip-permissions_a1=b"},
		{"mcp-server", "--dangerously-skip-permissionsfoo=bar"}, // 变量名样式不限大小写
		{"tool", "--permission-mode", "bypassPermissionsPATH=/x"},
		{"tool", "--permission-mode=bypassPermissionsHOME=/x"},
		{"tool", "--sandbox", "danger-full-accessHOME=/x"},
		{"tool", "--ask-for-approval", "neverLANG=C"},
	} {
		if _, err := procArgsOK(fakeProcWith(t, argv...), 4242, want); err == nil {
			t.Errorf("连串应被拦住：%q", argv)
		}
	}
}

// 原有的逐字相等照旧拦；正常参数不误判。
func TestGluedBypassCheckDoesNotMisjudgeOrdinaryArguments(t *testing.T) {
	want := LaunchArgs{Tool: "claude", Binary: "claude"}
	for _, c := range []struct {
		argv []string
		ok   bool
	}{
		{[]string{"bash", "-c", "x", "--dangerously-skip-permissions"}, false}, // 逐字相等：原语义
		{[]string{"bash", "--dangerously-skip-permissions=1"}, false},          // 原语义
		{[]string{"tool", "--permission-mode", "bypassPermissions"}, false},    // 原语义
		// 以绕过参数为前缀、但后面不是「变量名=」：按原语义放行
		{[]string{"tool", "--dangerously-skip-permissions-docs"}, true},
		{[]string{"tool", "--dangerously-skip-permissionsfoo"}, true},
		{[]string{"tool", "--dangerously-skip-permissions-x=1"}, true},
		{[]string{"tool", "--dangerously-skip-permissions2=1"}, true},
		{[]string{"tool", "--dangerously-skip-permissions.x=1"}, true},
		// 绕过参数不在词的开头：不算
		{[]string{"tool", "/opt/--dangerously-skip-permissionsPATH=x"}, true},
		{[]string{"tool", "--permission-mode", "bypassPermissions-x"}, true},
		{[]string{"tool", "--permission-mode", "default"}, true},
		{[]string{"bash", "-c", "export AWS_PROFILE=default && sleep 1"}, true},
	} {
		if _, err := procArgsOK(fakeProcWith(t, c.argv...), 4242, want); (err == nil) != c.ok {
			t.Errorf("%q：通过=%v，期望 %v（%v）", c.argv, err == nil, c.ok, err)
		}
	}
}

// CLI 进程仍走参数白名单，不经过连串检查：合法的 claude 命令行照常通过，连串在白名单里因「不认识的参数」被拒。
func TestCLIProcessStillUsesTheWhitelist(t *testing.T) {
	want := LaunchArgs{Tool: "claude", Binary: "claude"}
	if ok, err := procArgsOK(fakeProcWith(t, "/usr/bin/claude", "--session-id", "u-1", "--model", "m", "--add-dir", "/x/--dangerously-skip-permissions-docs"), 4242, want); !ok || err != nil {
		t.Errorf("合法的 CLI 命令行应通过：%v %v", ok, err)
	}
	if ok, err := procArgsOK(fakeProcWith(t, "/usr/bin/claude", "--session-id", "u-1", "--dangerously-skip-permissionsPATH=/x"), 4242, want); !ok || err == nil {
		t.Errorf("CLI 进程里的连串应被白名单拒绝：%v %v", ok, err)
	}
}
