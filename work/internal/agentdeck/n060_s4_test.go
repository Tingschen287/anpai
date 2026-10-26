package agentdeck

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/sessionenv"
)

// n060 段 4：会话 PATH 交给 agent-deck、按会话 PATH 解出的可执行文件查版本与列模型、Kimi（模型经命令传）。

// writeExe 写一个假的可执行脚本。
func writeExe(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// ToolVersionAt 跑的就是给定的绝对路径，子进程的 PATH 就是会话 PATH（脚本经 env 找解释器也按它）。
func TestN060S4ToolVersionAtRunsTheGivenExecutableWithTheSessionPath(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "path.log")
	exe := writeExe(t, dir, "codex", `if [ "$1" = "--version" ]; then echo "codex-cli 0.164.0"; echo "second line"; echo "$PATH" > `+log+`; exit 0; fi; exit 3`+"\n")
	t.Setenv("PATH", "/usr/bin:/bin")
	v, err := ToolVersionAt(context.Background(), exe, "/session/bin:/usr/bin:/bin")
	if err != nil || v != "codex-cli 0.164.0" {
		t.Fatalf("版本取第一行：%q %v", v, err)
	}
	if b, _ := os.ReadFile(log); strings.TrimSpace(string(b)) != "/session/bin:/usr/bin:/bin" {
		t.Fatalf("子进程的 PATH 应是会话 PATH：%q", b)
	}
	bad := writeExe(t, dir, "bad", "exit 5\n")
	if _, err := ToolVersionAt(context.Background(), bad, "/usr/bin:/bin"); err == nil {
		t.Fatal("退出码非零应报错")
	}
}

// ListModelsAt：跑给定的可执行文件、PATH 是会话 PATH；输出超过 8 MiB、超时各自有码；都不吞掉别的错误。
func TestN060S4ListModelsAtRunsTheGivenExecutable(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "env.log")
	exe := writeExe(t, dir, "codex", `echo "$1 $2" > `+log+`; echo "$PATH" >> `+log+`; printf '{"models":[]}'`+"\n")
	t.Setenv("PATH", "/usr/bin:/bin")
	out, err := ListModelsAt(context.Background(), exe, []string{"debug", "models"}, "/session/bin:/usr/bin:/bin")
	if err != nil || out != `{"models":[]}` {
		t.Fatalf("%q %v", out, err)
	}
	if b, _ := os.ReadFile(log); string(b) != "debug models\n/session/bin:/usr/bin:/bin\n" {
		t.Fatalf("参数与 PATH：%q", b)
	}
	// 超过 8 MiB：当失败（不是无限读、也不是把一半当结果）
	big := writeExe(t, dir, "big", "head -c 9000000 /dev/zero | tr '\\000' x\n")
	_, err = ListModelsAt(context.Background(), big, nil, "/usr/bin:/bin")
	var ce *i18n.Error
	if !errors.As(err, &ce) || ce.Key != "err.agentdeck.listmodels.toolarge" {
		t.Fatalf("太大应有码 toolarge：%v", err)
	}
	// 超时：有自己的码
	slow := writeExe(t, dir, "slow", "sleep 30\n")
	old := listWait
	listWait = 150 * time.Millisecond
	t.Cleanup(func() { listWait = old })
	start := time.Now()
	_, err = ListModelsAt(context.Background(), slow, nil, "/usr/bin:/bin")
	if time.Since(start) > 5*time.Second || !errors.As(err, &ce) || ce.Key != "err.agentdeck.listmodels.timeout" {
		t.Fatalf("超时应有码 timeout 且不拖：%v %v", time.Since(start), err)
	}
	// 退出码非零：把已读到的输出和错误都给调用方
	fail := writeExe(t, dir, "fail", "echo partial; exit 4\n")
	if out, err := ListModelsAt(context.Background(), fail, nil, "/usr/bin:/bin"); err == nil || !strings.Contains(out, "partial") {
		t.Fatalf("非零退出：%q %v", out, err)
	}
}

// 旧入口不变：ListModels / ToolInfo 仍按进程自己的 PATH、给 binary 名字。
func TestN060S4LegacyEntriesStillUseTheProcessPath(t *testing.T) {
	dir := t.TempDir()
	writeExe(t, dir, "faketool", `if [ "$1" = "--version" ]; then echo "faketool 9.9"; else echo "a b"; fi`+"\n")
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	path, ver, err := ToolInfo(context.Background(), "faketool")
	if err != nil || path != filepath.Join(dir, "faketool") || ver != "faketool 9.9" {
		t.Fatalf("%q %q %v", path, ver, err)
	}
	if out, err := ListModels(context.Background(), "faketool", []string{"models"}); err != nil || strings.TrimSpace(out) != "a b" {
		t.Fatalf("%q %v", out, err)
	}
}

// 会话 PATH 只随 launch 交给 agent-deck（会话由它起，PATH 取自启动者）；别的子命令不带；ctx 里没有就不动环境。
func TestN060S4LaunchHandsTheSessionPathToAgentDeck(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	bin := writeExe(t, dir, "agent-deck", `echo "$1|$PATH" >> `+log+`
case "$1" in
  launch) printf '{"success":true,"session_id":"ad-1","tool":"kimi"}';;
  list) printf '[]';;
esac
`)
	c, err := NewClient(bin, "deck", "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	ctx := WithSessionPath(context.Background(), "/home/o/.kimi-code/bin:/usr/bin:/bin")
	if _, err := c.Launch(ctx, LaunchRequest{ProjectPath: "/tmp/p", Tool: "kimi", Model: "kimi-code/k3", NoWait: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListSessions(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Launch(context.Background(), LaunchRequest{ProjectPath: "/tmp/p", Tool: "kimi", Model: "kimi-code/k3", NoWait: true}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	want := "launch|/home/o/.kimi-code/bin:/usr/bin:/bin\nlist|/usr/bin:/bin\nlaunch|/usr/bin:/bin\n"
	if string(b) != want {
		t.Fatalf("launch 带会话 PATH、list 不带、没有会话 PATH 时不动：\n%q\n%q", b, want)
	}
}

// Kimi 与 grok 同一路：-c "kimi --model '<模型>'"，不再另给 launch 一个 --model（上游对自定义工具不接受）。
func TestN060S4KimiModelGoesThroughTheCommand(t *testing.T) {
	c, argv := fakeLaunch(t)
	if _, err := c.Launch(context.Background(), LaunchRequest{ProjectPath: "/tmp/p", Tool: "kimi", Model: "kimi-code/k3", NoWait: true}); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(argv(), "|"), "launch|/tmp/p|-c|kimi --model 'kimi-code/k3'|--json|--no-wait"; got != want {
		t.Fatalf("\n%s\n%s", got, want)
	}
	if ToolsModelViaCommand["kimi"] != "--model" {
		t.Fatalf("kimi 应在经命令传模型的工具表里：%v", ToolsModelViaCommand)
	}
	// 声明了没有模型就不传
	c, argv = fakeLaunch(t)
	if _, err := c.Launch(context.Background(), LaunchRequest{ProjectPath: "/tmp/p", Tool: "kimi", ModelNotApplicable: true}); err == nil {
		t.Fatal("kimi 有模型：声明不适用应被拒绝（与 grok 同）")
	}
	// 模型 id 里的引号被转义
	c, argv = fakeLaunch(t)
	if _, err := c.Launch(context.Background(), LaunchRequest{ProjectPath: "/tmp/p", Tool: "kimi", Model: "a'b", NoWait: true}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(argv(), "|"); !strings.Contains(got, `-c|kimi --model 'a'\''b'|`) {
		t.Fatalf("模型要 shell 转义：%s", got)
	}
}

// 启动后核对 kimi 进程的参数：deck 经命令传的 --model <模型> 放行；别的（--yolo、--auto、-y）一律拒绝。
func TestN060S4KimiProcessArgsAreWhitelisted(t *testing.T) {
	want := LaunchArgs{Tool: "kimi", Binary: "kimi"}
	for _, c := range []struct {
		name string
		argv string
		ok   bool
	}{
		{"deck 传的模型", "/home/o/.kimi-code/bin/kimi|--model|kimi-code/k3", true},
		{"没有参数", "kimi", true},
		{"--model=", "kimi|--model=kimi-for-coding", true},
		{"模型值是参数", "kimi|--model|--yolo", false},
		{"--yolo", "kimi|--yolo", false},
		{"--auto（Never Ask）", "kimi|--auto", false},
		{"-y", "kimi|-y", false},
		{"-m 简写不在白名单里", "kimi|-m|kimi-code/k3", false},
		{"提示词位置参数", "kimi|--model|kimi-code/k3|做点什么", false},
		{"-p 非交互", "kimi|-p|hi", false},
	} {
		args, isCLI := cliArgs(strings.Split(c.argv, "|"), want)
		if !isCLI {
			t.Errorf("%s：应认作 kimi 进程", c.name)
			continue
		}
		if err := CheckCLIArgs(args, want); (err == nil) != c.ok {
			t.Errorf("%s：期望通过=%v，得到 %v", c.name, c.ok, err)
		}
	}
}

// [tools.kimi] 的写法（stage-4/kimi/tools-kimi.toml 原文）：只有 command 与 icon，通过上游配置的绕过检查；
// 对自定义工具写上 dangerous_mode、wrapper、extra_args 一律被拒。
func TestN060S4ToolsKimiSectionPassesTheConfigCheck(t *testing.T) {
	safe := "[claude]\ndangerous_mode = false\n\n[tools.grok]\ncommand = \"grok\"\nicon = \"X\"\n\n"
	section := "[tools.kimi]\ncommand = \"kimi\"\nicon = \"K\"\n"
	for _, tool := range []string{"claude", "codex", "kimi"} {
		if err := checkConfigText(tool, safe+section); err != nil {
			t.Errorf("%s：只写 command 与 icon 应通过：%v", tool, err)
		}
	}
	for name, extra := range map[string]string{
		"dangerous_mode": "dangerous_mode = true\n",
		"wrapper":        "wrapper = \"sh -c {command}\"\n",
		"extra_args":     "extra_args = [\"--yolo\"]\n",
		"带参数的命令":         "", // 下面单独写
	} {
		text := safe + section + extra
		if name == "带参数的命令" {
			text = safe + "[tools.kimi]\ncommand = \"kimi --yolo\"\nicon = \"K\"\n"
		}
		// 起的是 kimi 时：command 只能是裸命令、wrapper 不能写、extra_args 必须空（checkToolKey）；dangerous_mode 任何工具起时都查
		if err := checkConfigText("kimi", text); err == nil {
			t.Errorf("%s：起 kimi 时应被拒绝", name)
		}
		if name == "dangerous_mode" {
			if err := checkConfigText("claude", text); err == nil {
				t.Errorf("%s：起 claude 时也应被拒绝", name)
			}
		}
	}
	if !customToolNotCodex("kimi", safe+section) {
		t.Error("[tools.kimi] 没有声明 compatible_with：它不是 Codex 兼容工具，不走 Codex trust 门")
	}
}

// 以下四条用真的 RunLoginShell 跑临时目录里的假 shell（sessionenv 不自己起进程，exec 只许出现在 adapter 包里）。

// 真的运行一个假 shell：stdin 是 /dev/null，参数是 -l -i -c，rc 的杂音（stdout、stderr）不影响结果。
func TestN060S4RealRunWithAFakeShell(t *testing.T) {
	dir := t.TempDir()
	sh := filepath.Join(dir, "bash")
	os.WriteFile(sh, []byte("#!/bin/sh\necho \"noise from rc\" >&2\necho \"PATH=/forged\"\n[ \"$1$2$3\" = \"-l-i-c\" ] || exit 9\n[ -n \"$ANPAI_PATH_PROBE\" ] || exit 8\nread x </dev/stdin; [ -z \"$x\" ] || exit 7\nPATH=/from/login/shell:$PATH\neval \"$4\"\n"), 0o755)
	r := sessionenv.NewLoginResolver(RunLoginShell)
	r.Getenv = func(k string) string {
		return map[string]string{"SHELL": sh, "PATH": "/usr/bin:/bin"}[k]
	}
	r.Environ = func() []string { return []string{"PATH=/usr/bin:/bin"} }
	s := r.Get(context.Background())
	if s.Source != sessionenv.SourceLoginShell || !strings.HasPrefix(s.Path, "/from/login/shell:") || strings.Contains(s.Path, "forged") {
		t.Fatalf("假 shell 的真实运行：%+v", s)
	}
}

// 卡住的 shell 在超时后整个进程组被杀（rc 里的后台进程也一起收拾），Get 不等它。
func TestN060S4HungShellIsKilledWithItsChildren(t *testing.T) {
	dir := t.TempDir()
	pidf := filepath.Join(dir, "child.pid")
	sh := filepath.Join(dir, "zsh")
	os.WriteFile(sh, []byte("#!/bin/sh\nsleep 300 &\necho $! > "+pidf+"\nsleep 300\n"), 0o755)
	r := sessionenv.NewLoginResolver(RunLoginShell)
	r.Wait = 300 * time.Millisecond
	r.Getenv = func(k string) string { return map[string]string{"SHELL": sh, "PATH": "/usr/bin"}[k] }
	r.Environ = func() []string { return []string{"PATH=/usr/bin:/bin"} }
	start := time.Now()
	s := r.Get(context.Background())
	if time.Since(start) > 3*time.Second {
		t.Fatalf("卡住的 shell 没有按超时返回：%v", time.Since(start))
	}
	var ce *i18n.Error
	if s.Source != sessionenv.SourceProcess || !errors.As(s.Fallback, &ce) || ce.Key != "err.sessionenv.timeout" {
		t.Fatalf("应回退并说超时：%+v", s)
	}
	b, err := os.ReadFile(pidf)
	if err != nil {
		t.Fatal(err)
	}
	pid := strings.TrimSpace(string(b))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if exec.Command("kill", "-0", pid).Run() != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("shell 的后台子进程 %s 还活着", pid)
}

// 输出超过 64 KiB 当失败（不无限读）。
func TestN060S4HugeOutputIsRefused(t *testing.T) {
	dir := t.TempDir()
	sh := filepath.Join(dir, "sh")
	os.WriteFile(sh, []byte("#!/bin/sh\nhead -c 200000 /dev/zero | tr '\\0' x\n"), 0o755)
	r := sessionenv.NewLoginResolver(RunLoginShell)
	r.Getenv = func(k string) string { return map[string]string{"SHELL": sh, "PATH": "/usr/bin"}[k] }
	r.Environ = func() []string { return []string{"PATH=/usr/bin"} }
	s := r.Get(context.Background())
	var ce *i18n.Error
	if s.Source != sessionenv.SourceProcess || !errors.As(s.Fallback, &ce) || ce.Key != "err.sessionenv.failed" {
		t.Fatalf("输出太大应失败回退：%+v", s)
	}
}

// 解析 shell 在自己的新会话里跑（没有控制终端）：deckd 跑在 tmux 窗格里，有控制终端；交互式 zsh 如果只是进了后台进程组、
// 还留在原会话里，碰终端会被 SIGTTOU 停住，一直等到超时（e2e61 实测）。假 shell 用 python 查自己（父进程 sh）是不是会话首进程。
func TestN060S4ShellRunsInItsOwnSessionWithoutAControllingTerminal(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("没有 python3")
	}
	dir := t.TempDir()
	sh := filepath.Join(dir, "bash")
	os.WriteFile(sh, []byte("#!/bin/sh\n"+py+" -c 'import os,sys\nok=os.getsid(0)==os.getppid()==os.getpgid(0)\nsys.exit(0 if ok else 9)' || exit 9\neval \"$4\"\n"), 0o755)
	r := sessionenv.NewLoginResolver(RunLoginShell)
	r.Getenv = func(k string) string { return map[string]string{"SHELL": sh, "PATH": "/usr/bin:/bin"}[k] }
	r.Environ = func() []string { return []string{"PATH=/usr/bin:/bin"} }
	if s := r.Get(context.Background()); s.Source != sessionenv.SourceLoginShell {
		t.Fatalf("shell 应是自己会话的首进程：%+v", s)
	}
}
