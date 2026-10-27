package sessionenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// n060 段 4：会话 PATH 解析。登录 shell 一律用假的（Run 注入，或临时目录里的假 shell 脚本），不运行 owner 的 shell 启动文件。

// markFrom 从传给 shell 的脚本里取出这次随机的边界（脚本里 printf 的两个边界参数）。
func markFrom(t *testing.T, args []string) string {
	t.Helper()
	if len(args) != 4 || args[0] != "-l" || args[1] != "-i" || args[2] != "-c" {
		t.Fatalf("应以登录交互 shell 运行：%v", args)
	}
	i := strings.Index(args[3], "'ANPAI")
	if i < 0 {
		t.Fatalf("脚本里没有边界：%q", args[3])
	}
	rest := args[3][i+1:]
	return rest[:strings.Index(rest, "B'")]
}

func fakeResolver(env map[string]string, run func(ctx context.Context, shell string, args, env []string) ([]byte, error)) *Resolver {
	return &Resolver{
		Getenv:  func(k string) string { return env[k] },
		Environ: func() []string { return nil },
		Run:     run,
		Now:     time.Now,
		Wait:    time.Second,
	}
}

func okShell(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "zsh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// 「deckd 自己的 PATH 里没有、会话 PATH 里有」：登录 shell 的目录在前，deckd 自己有而它没有的目录补在后面，去重保序。
func TestN060S4LoginPathIsMergedAheadOfTheProcessPath(t *testing.T) {
	shell := okShell(t)
	r := fakeResolver(map[string]string{"SHELL": shell, "PATH": "/usr/bin:/opt/own:/bin"}, func(_ context.Context, _ string, args, _ []string) ([]byte, error) {
		m := markFrom(t, args)
		return []byte("\nrc banner\n\n" + m + "B/home/o/.kimi-code/bin:/usr/bin:/home/o/.local/bin" + m + "E\n"), nil
	})
	s := r.Get(context.Background())
	if s.Source != SourceLoginShell || s.Fallback != nil || s.Shell != shell {
		t.Fatalf("应取到登录 shell 的 PATH：%+v", s)
	}
	if want := "/home/o/.kimi-code/bin:/home/o/.local/bin:/usr/bin:/opt/own:/bin"; s.Path != want {
		t.Fatalf("登录 shell 里 deckd 没有的目录在最前（按登录 shell 的顺序），deckd 自己的按原顺序跟在后面、去重：\n%s\n%s", s.Path, want)
	}
}

// deckd 自己 PATH 的顺序保持：owner 起 deck 时把某个目录排在 /usr/bin 前面，登录 shell 把系统目录排最前（macOS 的 path_helper），
// 合并后这个顺序不能被压到后面（旧 e2e 的假 CLI 用 env python3，被 /usr/bin/python3 顶掉；e2e60、e2e55、e2e57 实测）。
func TestN060S4OwnOrderIsKeptWhenTheLoginShellReordersSystemDirs(t *testing.T) {
	shell := okShell(t)
	r := fakeResolver(map[string]string{"SHELL": shell, "PATH": "/o/bin:/usr/bin:/bin"}, func(_ context.Context, _ string, args, _ []string) ([]byte, error) {
		m := markFrom(t, args)
		return []byte(m + "B/usr/local/bin:/usr/bin:/bin:/usr/sbin:/o/bin:/h/.kimi-code/bin" + m + "E"), nil
	})
	if s := r.Get(context.Background()); s.Path != "/usr/local/bin:/usr/sbin:/h/.kimi-code/bin:/o/bin:/usr/bin:/bin" {
		t.Fatalf("deckd 的 /o/bin 仍在 /usr/bin 前：%q", s.Path)
	}
}

// rc 文件打印的杂音（含伪造的 PATH 与旧的边界）不会被当成结果：边界是每次随机的，取最后一对。
func TestN060S4ShellNoiseAndForgedMarkersAreIgnored(t *testing.T) {
	shell := okShell(t)
	r := fakeResolver(map[string]string{"SHELL": shell, "PATH": "/usr/bin"}, func(_ context.Context, _ string, args, _ []string) ([]byte, error) {
		m := markFrom(t, args)
		forged := "ANPAI000000000000000000000000B/tmp/evil" + "ANPAI000000000000000000000000E"
		return []byte("PATH=/tmp/evil2\n" + forged + "\n" + m + "B/real/bin" + m + "E\ntrailing noise PATH=/tmp/evil3\n"), nil
	})
	s := r.Get(context.Background())
	if s.Path != "/real/bin:/usr/bin" || strings.Contains(s.Path, "evil") {
		t.Fatalf("只认本次随机边界之间的内容：%+v", s)
	}
	// 两次解析的边界不同（rc 文件预先知道不了）
	var marks []string
	r2 := fakeResolver(map[string]string{"SHELL": shell}, func(_ context.Context, _ string, args, _ []string) ([]byte, error) {
		m := markFrom(t, args)
		marks = append(marks, m)
		return []byte(m + "B/a" + m + "E"), nil
	})
	r2.Get(context.Background())
	refreshAndWait(t, r2) // 返工：不再有「过期重读」，第二次解析只能由刷新键触发
	if len(marks) != 2 || marks[0] == marks[1] {
		t.Fatalf("每次解析的边界应不同：%v", marks)
	}
}

// 不合格的目录一律丢掉：相对路径（含空串、.）会让查找跟着当前目录走；控制字符、打码标记字符、过长的也不要。
func TestN060S4UnsafeEntriesAreDropped(t *testing.T) {
	shell := okShell(t)
	long := "/" + strings.Repeat("a", maxEntry+1)
	r := fakeResolver(map[string]string{"SHELL": shell, "PATH": ".:rel/bin::/usr/bin"}, func(_ context.Context, _ string, args, _ []string) ([]byte, error) {
		m := markFrom(t, args)
		return []byte(m + "B.:bin:/ok/one::/bad\x01dir:/mark" + i18n.RedactMark + ":" + long + ":/ok/two" + m + "E"), nil
	})
	s := r.Get(context.Background())
	if s.Path != "/ok/one:/ok/two:/usr/bin" {
		t.Fatalf("只留绝对、干净、不过长的目录：%q", s.Path)
	}
	if s.Dropped != 6+3 { // 登录 shell 里 6 条不合格，deckd 自己的 PATH 里 3 条（. rel/bin 空串）
		t.Fatalf("丢了多少条：%d", s.Dropped)
	}
}

// 解析有超时：卡住的 rc（读输入、等网络）不会卡住 deckd。超时回退为 deckd 自己的 PATH，并带可译的原因。
func TestN060S4TimeoutFallsBackToTheProcessPath(t *testing.T) {
	shell := okShell(t)
	r := fakeResolver(map[string]string{"SHELL": shell, "PATH": "/usr/bin:/bin"}, func(ctx context.Context, _ string, _, _ []string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	r.Wait = 50 * time.Millisecond
	start := time.Now()
	s := r.Get(context.Background())
	if time.Since(start) > 2*time.Second {
		t.Fatalf("没有按超时返回：%v", time.Since(start))
	}
	var ce *i18n.Error
	if s.Source != SourceProcess || s.Path != "/usr/bin:/bin" || !errors.As(s.Fallback, &ce) || ce.Key != "err.sessionenv.timeout" {
		t.Fatalf("超时应回退并说明原因：%+v", s)
	}
}

// 输出里找不到边界、没有可用目录：回退并说明。
func TestN060S4BadOutputFallsBack(t *testing.T) {
	shell := okShell(t)
	for name, out := range map[string]string{"没有边界": "just noise\n", "只有开头": "ANPAIxB/a", "空": "", "全是相对路径": "BAD"} {
		r := fakeResolver(map[string]string{"SHELL": shell, "PATH": "/usr/bin"}, func(_ context.Context, _ string, args, _ []string) ([]byte, error) {
			m := markFrom(t, args)
			if out == "BAD" {
				return []byte(m + "B.:rel" + m + "E"), nil
			}
			return []byte(out), nil
		})
		s := r.Get(context.Background())
		var ce *i18n.Error
		if s.Source != SourceProcess || s.Path != "/usr/bin" || !errors.As(s.Fallback, &ce) || ce.Key != "err.sessionenv.nooutput" {
			t.Errorf("%s：应回退（nooutput）：%+v", name, s)
		}
	}
	// 运行本身失败
	r := fakeResolver(map[string]string{"SHELL": shell, "PATH": "/usr/bin"}, func(context.Context, string, []string, []string) ([]byte, error) {
		return nil, errors.New("exit status 7")
	})
	var ce *i18n.Error
	if s := r.Get(context.Background()); !errors.As(s.Fallback, &ce) || ce.Key != "err.sessionenv.failed" || s.Source != SourceProcess {
		t.Fatalf("运行失败：%+v", s)
	}
}

// 没有可用的 shell：回退并说明；$SHELL 不是绝对路径、不存在、名字不认得，都不用（不能被环境变量指到任意程序）。
func TestN060S4OnlyKnownShellsAreUsed(t *testing.T) {
	dir := t.TempDir()
	evil := filepath.Join(dir, "evil")
	os.WriteFile(evil, []byte("#!/bin/sh\n"), 0o755)
	gone := filepath.Join(dir, "zsh-missing", "zsh")
	old := fallbackShells
	fallbackShells = nil
	t.Cleanup(func() { fallbackShells = old })
	for name, sh := range map[string]string{"空": "", "相对": "zsh", "没有这个文件": gone, "名字不认得": evil} {
		called := false
		r := fakeResolver(map[string]string{"SHELL": sh, "PATH": "/usr/bin"}, func(context.Context, string, []string, []string) ([]byte, error) {
			called = true
			return nil, nil
		})
		s := r.Get(context.Background())
		var ce *i18n.Error
		if called || !errors.As(s.Fallback, &ce) || ce.Key != "err.sessionenv.noshell" || s.Path != "/usr/bin" {
			t.Errorf("%s：不该运行任何程序：called=%v %+v", name, called, s)
		}
	}
	// $SHELL 不可用时退到系统常见的 shell
	fallbackShells = []string{okShell(t)}
	r := fakeResolver(map[string]string{"SHELL": evil}, func(_ context.Context, shell string, args, _ []string) ([]byte, error) {
		m := markFrom(t, args)
		return []byte(m + "B/x" + m + "E"), nil
	})
	if s := r.Get(context.Background()); s.Source != SourceLoginShell || s.Shell != fallbackShells[0] {
		t.Fatalf("应退到常见的 shell：%+v", s)
	}
}

// 解析期间 rc 里又起了 deck（递归）：环境里已有 ANPAI_PATH_PROBE 就不再解析。进程的环境里带它，说明自己就是被解析出来的那个。
func TestN060S4ProbeEnvStopsRecursion(t *testing.T) {
	called := false
	var sawEnv []string
	r := fakeResolver(map[string]string{"SHELL": okShell(t), "PATH": "/usr/bin"}, func(_ context.Context, _ string, args, env []string) ([]byte, error) {
		called = true
		sawEnv = env
		m := markFrom(t, args)
		return []byte(m + "B/a" + m + "E"), nil
	})
	r.Environ = func() []string { return []string{"HOME=/h"} }
	r.Get(context.Background())
	if !called || !contains(sawEnv, ProbeEnv+"=1") || !contains(sawEnv, "HOME=/h") {
		t.Fatalf("解析时子进程环境要带 %s：%v", ProbeEnv, sawEnv)
	}
	called = false
	r2 := fakeResolver(map[string]string{"SHELL": okShell(t), "PATH": "/usr/bin", ProbeEnv: "1"}, func(context.Context, string, []string, []string) ([]byte, error) {
		called = true
		return nil, nil
	})
	s := r2.Get(context.Background())
	var ce *i18n.Error
	if called || !errors.As(s.Fallback, &ce) || ce.Key != "err.sessionenv.recursion" || s.Path != "/usr/bin" {
		t.Fatalf("递归时不再解析：called=%v %+v", called, s)
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// refreshAndWait 按一次刷新键并等它读完（轮询状态，不 sleep 固定时间）。
func refreshAndWait(t *testing.T, r *Resolver) {
	t.Helper()
	r.Refresh()
	waitIdle(t, r)
}

func waitIdle(t *testing.T, r *Resolver) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if !r.State(context.Background()).Refreshing {
			return
		}
	}
	t.Fatal("读一直没有结束")
}

// 快照：启动时读一次，之后无论过多久、被问多少次都不再运行登录 shell（返工，owner 2026-10-27 定；原来的「成功 5 分钟、失败 30 秒、Fresh 10 秒」过期重读已去掉）。
// 只有刷新键才重读；先拿到的快照是值，不随后来的读变（同一次派工里 trust 门和启动用的是同一份）。
// （原测试 TestN060S4CacheAndFresh 断言的正是被去掉的过期重读，按 owner 的决定改成下面的断言，其余检查点——快照是值、失败结果换成新回退——保留。）
func TestN060S4SnapshotNeverExpiresOnItsOwn(t *testing.T) {
	shell := okShell(t)
	now := time.Unix(1_000_000, 0)
	n := 0
	fail := false
	r := fakeResolver(map[string]string{"SHELL": shell, "PATH": "/usr/bin"}, func(_ context.Context, _ string, args, _ []string) ([]byte, error) {
		n++
		if fail {
			return nil, errors.New("boom")
		}
		m := markFrom(t, args)
		return []byte(m + "B/dir" + string(rune('0'+n)) + m + "E"), nil
	})
	r.Now = func() time.Time { return now }
	first := r.Get(context.Background())
	for _, d := range []time.Duration{0, 11 * time.Second, 6 * time.Minute, 24 * time.Hour} {
		now = now.Add(d)
		r.Get(context.Background())
		r.State(context.Background())
	}
	if n != 1 {
		t.Fatalf("时间过去多久都不重读：解析了 %d 次", n)
	}
	// 失败的启动读：同样只读一次，不会 30 秒后自己再试
	fail = true
	rf := fakeResolver(map[string]string{"SHELL": shell, "PATH": "/usr/bin"}, func(context.Context, string, []string, []string) ([]byte, error) {
		n++
		return nil, errors.New("boom")
	})
	rf.Now = func() time.Time { return now }
	n = 0
	if s := rf.Get(context.Background()); s.Fallback == nil || n != 1 {
		t.Fatalf("启动读失败：%d %+v", n, s)
	}
	now = now.Add(time.Hour)
	rf.Get(context.Background())
	if n != 1 {
		t.Fatalf("失败也只读一次，要等刷新键：%d", n)
	}
	// 刷新键：读一次，拿到新的；先拿到的快照不变
	fail = false
	refreshAndWait(t, r)
	if s := r.Get(context.Background()); n != 2 || s.Path == first.Path {
		t.Fatalf("刷新键触发一次重读：%d %q %q", n, s.Path, first.Path)
	}
	if !strings.HasPrefix(first.Path, "/dir1") {
		t.Fatalf("先拿到的快照不随后来的解析变：%q", first.Path)
	}
}

// NewStatic 不解析，一直是进程自己的 PATH；零值之外的 Resolver 没开解析（Run 为 nil）同样。
func TestN060S4StaticResolverNeverRunsAShell(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:rel:/bin")
	s := NewStatic().Get(context.Background())
	if s.Source != SourceProcess || s.Path != "/usr/bin:/bin" || s.Fallback != nil || s.Dropped != 1 {
		t.Fatalf("静态：%+v", s)
	}
}

// LookPath 只看给定的 PATH：进程自己的 PATH 里的同名程序不算；相对目录跳过；不可执行的、目录不算；带斜杠只认绝对路径。
func TestN060S4LookPathOnlyUsesTheGivenPath(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	mk := func(dir, name string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte("#!/bin/sh\n"), mode)
		return p
	}
	inA := mk(a, "codex", 0o755)
	inB := mk(b, "codex", 0o755)
	mk(a, "noexec", 0o644)
	os.Mkdir(filepath.Join(a, "adir"), 0o755)
	t.Setenv("PATH", b) // 进程自己的 PATH 里有另一份 codex
	if got := LookPath(a, "codex"); got != inA {
		t.Fatalf("只在给定的 PATH 里找：%q 应为 %q", got, inA)
	}
	if got := LookPath(a+":"+b, "codex"); got != inA {
		t.Fatalf("先命中的在前：%q", got)
	}
	if got := LookPath(b+":"+a, "codex"); got != inB {
		t.Fatalf("先命中的在前（反过来）：%q", got)
	}
	if LookPath(a, "noexec") != "" || LookPath(a, "adir") != "" || LookPath(a, "missing") != "" || LookPath(a, "") != "" {
		t.Fatal("不可执行的、目录、不存在的都找不到")
	}
	if LookPath("rel/dir:"+a, "codex") != inA || LookPath("rel", "codex") != "" {
		t.Fatal("相对目录跳过")
	}
	if LookPath(a, inB) != inB || LookPath(a, "rel/codex") != "" || LookPath(a, "./codex") != "" {
		t.Fatal("带斜杠只认绝对路径，不在 PATH 里再找")
	}
}

func TestN060S4EnvReplacesPath(t *testing.T) {
	got := Env([]string{"A=1", "PATH=/old", "B=2", "PATHEXT=x"}, "/new")
	if strings.Join(got, "|") != "A=1|B=2|PATHEXT=x|PATH=/new" {
		t.Fatalf("%v", got)
	}
	if got := Env(nil, "/p"); len(got) != 1 || got[0] != "PATH=/p" {
		t.Fatalf("没有 PATH 就加上：%v", got)
	}
}

// 调用方（一次 HTTP 请求）在解析中途取消了：解析自己有超时，不跟着调用方的 ctx 走，也不把「被取消」当解析失败缓存起来（否则之后 30 秒都回退）。
func TestN060S4CallerCancelDoesNotPoisonTheCache(t *testing.T) {
	shell := okShell(t)
	n := 0
	r := fakeResolver(map[string]string{"SHELL": shell, "PATH": "/usr/bin"}, func(ctx context.Context, _ string, args, _ []string) ([]byte, error) {
		n++
		if err := ctx.Err(); err != nil {
			return nil, err // 若解析的 ctx 跟着调用方走，这里就失败
		}
		m := markFrom(t, args)
		return []byte(m + "B/ok/bin" + m + "E"), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s := r.Get(ctx); s.Source != SourceLoginShell || s.Path != "/ok/bin:/usr/bin" {
		t.Fatalf("调用方已取消，解析仍应完成：%+v", s)
	}
	if s := r.Get(context.Background()); s.Source != SourceLoginShell || n != 1 {
		t.Fatalf("结果被缓存、没有重算：%d %+v", n, s)
	}
}
