package projectctl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// fakePython 在 dir 里写一个假解释器：被问版本（-c）时回 version，真跑脚本时往 marks 里记一笔再按 body 输出。
func fakePython(t *testing.T, dir, name, version, marks, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	script := "#!/bin/sh\nif [ \"$1\" = \"-c\" ]; then echo " + version + "; exit 0; fi\n" +
		"echo " + name + " >> '" + marks + "'\n" + body + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// 假的版本探测：从文件名里认版本，便于不起进程测选择顺序。
func fakeProbe(versions map[string]pyVersion) probeFunc {
	return func(exe string) (pyVersion, error) {
		if v, ok := versions[exe]; ok {
			return v, nil
		}
		return pyVersion{}, errors.New("exec format error")
	}
}

func touchExec(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func noSkip(string) bool { return false }

// 3.9 的 python3 被跳过，同目录里更高版本的 python3.N 中版本最高的被选中。
func TestResolvePythonSkipsOldPython3AndPicksHighestInTheSameDir(t *testing.T) {
	d := t.TempDir()
	for _, n := range []string{"python3", "python3.10", "python3.11", "python3.12", "python3.13"} {
		touchExec(t, filepath.Join(d, n))
	}
	probe := fakeProbe(map[string]pyVersion{
		filepath.Join(d, "python3"):    {3, 9},
		filepath.Join(d, "python3.11"): {3, 11},
		filepath.Join(d, "python3.12"): {3, 12},
		filepath.Join(d, "python3.13"): {3, 13},
	})
	got, err := resolvePython("", d, probe, noSkip)
	if err != nil || got != filepath.Join(d, "python3.13") {
		t.Fatalf("应选 python3.13（python3 是 3.9 要跳过，python3.10 低于 3.11 不探测）：%q %v", got, err)
	}
}

// PATH 顺序优先：前面目录里够新的 python3 排在后面目录的更高版本之前（「PATH 上第一个」）。
func TestResolvePythonFollowsPathOrder(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	touchExec(t, filepath.Join(a, "python3"))
	touchExec(t, filepath.Join(b, "python3.13"))
	probe := fakeProbe(map[string]pyVersion{filepath.Join(a, "python3"): {3, 11}, filepath.Join(b, "python3.13"): {3, 13}})
	got, err := resolvePython("", a+string(os.PathListSeparator)+b, probe, noSkip)
	if err != nil || got != filepath.Join(a, "python3") {
		t.Fatalf("PATH 里靠前的目录先：%q %v", got, err)
	}
}

// 前面目录只有 3.9，后面目录有 3.12：跳过 3.9，选后面的。
func TestResolvePythonFallsThroughOldOnesToALaterDir(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	touchExec(t, filepath.Join(a, "python3"))
	touchExec(t, filepath.Join(b, "python3"))
	probe := fakeProbe(map[string]pyVersion{filepath.Join(a, "python3"): {3, 9}, filepath.Join(b, "python3"): {3, 12}})
	got, err := resolvePython("", a+string(os.PathListSeparator)+b, probe, noSkip)
	if err != nil || got != filepath.Join(b, "python3") {
		t.Fatalf("应跳过 3.9 选后面目录的 python3：%q %v", got, err)
	}
}

// 都不够新：报清楚的错，说出探测到了什么、怎么指定；中英文都有。
func TestResolvePythonNothingNewEnoughSaysSo(t *testing.T) {
	a := t.TempDir()
	touchExec(t, filepath.Join(a, "python3"))
	probe := fakeProbe(map[string]pyVersion{filepath.Join(a, "python3"): {3, 9}})
	_, err := resolvePython("", a, probe, noSkip)
	if err == nil {
		t.Fatal("没有 3.11 及以上的解释器应报错")
	}
	var ce *i18n.Error
	if !errors.As(err, &ce) {
		t.Fatalf("应是带码的错误：%T", err)
	}
	zh := err.Error()
	for _, want := range []string{"3.11", "(3.9)", EnvPython} {
		if !strings.Contains(zh, want) {
			t.Errorf("中文错误缺 %q：%s", want, zh)
		}
	}
	code, args, ok := i18n.Wire(err)
	if !ok {
		t.Fatal("没有码")
	}
	i18n.SetLang(i18n.EN)
	en := deckclient.Text(code, args, err.Error())
	i18n.SetLang(i18n.ZH)
	for _, want := range []string{"3.11", "(3.9)", EnvPython, "projectctl"} {
		if !strings.Contains(en, want) {
			t.Errorf("英文错误缺 %q：%s", want, en)
		}
	}
	if hanIn(en) {
		t.Errorf("英文错误里有中文：%s", en)
	}
	// PATH 上一个 python3 都没有
	_, err = resolvePython("", t.TempDir(), probe, noSkip)
	if err == nil || !strings.Contains(err.Error(), "没有 python3") {
		t.Fatalf("空 PATH 应说 PATH 上没有 python3：%v", err)
	}
}

// 显式指定的优先于 PATH；指定了但不行就报错，不悄悄换别的。
func TestResolvePythonOverrideWinsAndIsNeverSilentlyReplaced(t *testing.T) {
	d := t.TempDir()
	good := filepath.Join(d, "python3")
	touchExec(t, good)
	probe := fakeProbe(map[string]pyVersion{good: {3, 12}, "/opt/py/python3.9": {3, 9}, "/opt/py/python3.12": {3, 12}})
	if got, err := resolvePython("/opt/py/python3.12", d, probe, noSkip); err != nil || got != "/opt/py/python3.12" {
		t.Fatalf("指定的优先：%q %v", got, err)
	}
	if _, err := resolvePython("/opt/py/python3.9", d, probe, noSkip); err == nil || !strings.Contains(err.Error(), "3.9") {
		t.Fatalf("指定的版本不够应报错（即使 PATH 上有好的）：%v", err)
	}
	if _, err := resolvePython("/no/such/python", d, probe, noSkip); err == nil {
		t.Fatal("指定的跑不起来应报错（即使 PATH 上有好的）")
	}
}

// 相对路径、空项的 PATH 条目不探测：不去执行当前目录里的东西。
func TestResolvePythonIgnoresRelativePathEntries(t *testing.T) {
	d := t.TempDir()
	touchExec(t, filepath.Join(d, "python3"))
	t.Chdir(d)
	called := false
	probe := func(string) (pyVersion, error) { called = true; return pyVersion{3, 12}, nil }
	_, err := resolvePython("", ".:"+"", probe, noSkip)
	if err == nil || called {
		t.Fatalf("相对路径与空项不该被探测：called=%v err=%v", called, err)
	}
}

// 真起进程：假解释器按 -c 回版本。python3 是 3.9、python3.12 是 3.12 时，选 python3.12，而且 3.9 那个从没被拿去跑 projectctl。
func TestClientSkipsPython39AndRunsProjectctlWithPython312(t *testing.T) {
	d := t.TempDir()
	marks := filepath.Join(t.TempDir(), "marks")
	fakePython(t, d, "python3", "3.9", marks, "echo '用 3.9 跑 projectctl 会出错' >&2; exit 1")
	fakePython(t, d, "python3.12", "3.12", marks, `echo '{"ok":true,"result":{}}'`)
	t.Setenv("PATH", d)
	t.Setenv(EnvPython, "")
	SetPythonOverride("")
	c := NewClient(t.TempDir(), "/x/projectctl.py")
	if _, err := c.run(context.Background(), "validate"); err != nil {
		t.Fatalf("应由 3.12 跑通：%v", err)
	}
	b, _ := os.ReadFile(marks)
	if strings.TrimSpace(string(b)) != "python3.12" {
		t.Fatalf("只有 python3.12 该被拿去跑脚本：%q", b)
	}
}

// 没有够新的解释器时，run 与 Start 都报清楚的错，不去跑 3.9。
func TestClientReportsClearlyWhenNoPythonIsNewEnough(t *testing.T) {
	d := t.TempDir()
	marks := filepath.Join(t.TempDir(), "marks")
	fakePython(t, d, "python3", "3.9", marks, "exit 1")
	t.Setenv("PATH", d)
	t.Setenv(EnvPython, "")
	SetPythonOverride("")
	c := NewClient(t.TempDir(), "/x/projectctl.py")
	_, err := c.run(context.Background(), "validate")
	if err == nil || !strings.Contains(err.Error(), "3.11") || !strings.Contains(err.Error(), EnvPython) {
		t.Fatalf("应说清要 3.11 及怎么指定：%v", err)
	}
	if b, _ := os.ReadFile(marks); len(b) != 0 {
		t.Fatalf("3.9 不该被拿去跑脚本：%q", b)
	}
	if _, err := c.Start(context.Background(), StartRequest{NodeID: "n", Actor: "a", Role: "r", RouteClass: "c", Model: "m", Reason: "x"}); err == nil {
		t.Fatal("Start 在没有可用解释器时应报错")
	}
}

// 环境变量与 SetPythonOverride 都能指定；SetPythonOverride 优先。
func TestPythonOverrideFromEnvAndFromSetting(t *testing.T) {
	d := t.TempDir()
	marks := filepath.Join(t.TempDir(), "marks")
	envPy := fakePython(t, d, "py-env", "3.12", marks, `echo '{"ok":true,"result":{}}'`)
	setPy := fakePython(t, d, "py-set", "3.13", marks, `echo '{"ok":true,"result":{}}'`)
	t.Setenv("PATH", t.TempDir())
	t.Setenv(EnvPython, envPy)
	SetPythonOverride("")
	if got, err := ResolvePython(); err != nil || got != envPy {
		t.Fatalf("环境变量：%q %v", got, err)
	}
	SetPythonOverride(setPy)
	defer SetPythonOverride("")
	if got, err := ResolvePython(); err != nil || got != setPy {
		t.Fatalf("设置优先于环境变量：%q %v", got, err)
	}
}

// 命令文字里的解释器：默认还是 python3（文字逐字不变）；换成路径时，含空格或引号的按 shell 规则引用，可以原样复制去跑。
func TestPythonForText(t *testing.T) {
	old := PythonInText
	defer func() { PythonInText = old }()
	for in, want := range map[string]string{
		"":                                 "python3",
		"python3":                          "python3",
		"/Users/joy/.local/bin/python3.12": "/Users/joy/.local/bin/python3.12",
		"/opt/my py/python3.12":            "'/opt/my py/python3.12'",
		"/opt/it's/python3":                `'/opt/it'"'"'s/python3'`,
	} {
		PythonInText = in
		if got := PythonForText(); got != want {
			t.Errorf("%q → %q，期望 %q", in, got, want)
		}
	}
}
