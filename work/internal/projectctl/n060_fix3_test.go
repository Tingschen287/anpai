package projectctl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/wirecheck"
)

func hanIn(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF) {
			return true
		}
	}
	return false
}

// 适配层的本地中文改成码：Error() 中文逐字不变，英文界面按码翻译，只有 stderr 是外部原文。
func TestN060AdapterErrorsAreCoded(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	// 一个会失败的脚本：python3 打不开这个文件，stderr 是上游（python）的原文
	c := NewClient(t.TempDir(), filepath.Join(t.TempDir(), "no-such-projectctl.py"))
	_, err := c.Start(context.Background(), StartRequest{NodeID: "n099_x", Actor: "deck:s1", Role: "workbench_executor", RouteClass: "balanced", Model: "m", Reason: "r"})
	var ns *ErrStartNotStarted
	if !errors.As(err, &ns) {
		t.Fatalf("开工前核对不了账本应是确定没开工：%v", err)
	}
	zh := err.Error()
	for _, want := range []string{"开工前核对不了账本（projectctl verify-chain 执行失败：", "（stderr: ", "），不开工：开工出岔子时没法对账"} {
		if !strings.Contains(zh, want) {
			t.Fatalf("中文原文变了，少了 %q：%s", want, zh)
		}
	}
	code, args, ok := i18n.Wire(err)
	if !ok || code != "err.projectctl.start.noverify" || len(args) != 1 {
		t.Fatalf("应带码，外壳 ErrStartNotStarted 把原因的码交出来：%q %v %v", code, args, ok)
	}
	nest, isNest := args[0].(i18n.Nest)
	if !isNest || nest.Code != "err.projectctl.run.1" {
		t.Fatalf("原因应是嵌套码，不是压成字符串的中文：%#v", args[0])
	}
	// stderr 是外部原文，作为字符串参数；其余都是码
	i18n.SetLang(i18n.EN)
	en := deckclient.Text(code, args, zh)
	i18n.SetLang(i18n.ZH)
	if hanIn(en) || !strings.Contains(en, "cannot verify the ledger before starting") || !strings.Contains(en, "failed to run") {
		t.Fatalf("英文不应有本地中文：%q", en)
	}
}

// 各条错误的中文原文、码、英文一一对上，参数个数正确。
func TestN060AdapterErrorTemplates(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	cause := errors.New("exit status 2")
	cases := []struct {
		name string
		err  error
		zh   string
		en   string
	}{
		{"拒绝写命令", &ErrWriteCommandRefused{Command: "submit"}, `拒绝执行非只读子命令 "submit"：Work Deck 对 Graph 控制器只读，唯一的写入口是派工前的 Start`, `refusing to run non-read-only subcommand "submit"`},
		{"执行失败", i18n.E("err.projectctl.run.1", "validate", cause, "boom"), "projectctl validate 执行失败：exit status 2（stderr: boom）", "projectctl validate failed to run: exit status 2 (stderr: boom)"},
		{"不是合法 JSON", i18n.E("err.projectctl.run.2", "next", cause), "projectctl next 的输出不是合法 JSON：exit status 2", "projectctl next printed output that is not valid JSON"},
		{"返回失败", i18n.E("err.projectctl.run.3", "brief", "bad"), "projectctl brief 返回失败：bad", "projectctl brief reported failure: bad"},
		{"回执对不上", i18n.E("err.projectctl.start.mismatch", "n1", 2), `projectctl start 回执对不上：节点 "n1" attempt 2`, `projectctl start's receipt does not match: node "n1" attempt 2`},
		{"对账：确定没开工", i18n.E("err.projectctl.reconcile.notstarted", cause, 7), "exit status 2（账本事件数开工前后都是 7：确定没开工）", "(the ledger had 7 events before and after the start: it did not start)"},
		{"对账：结果不明", i18n.E("err.projectctl.reconcile.unknown", cause, 7, 8), "exit status 2；账本事件数从 7 变成 8，但状态投影里对不上这次开工：开工结果不明，可能已经开工", "the ledger event count went from 7 to 8"},
		{"对账：开工后核对不了", i18n.E("err.projectctl.reconcile.noverify", cause, cause), "exit status 2；开工后也核对不了账本（exit status 2）：开工结果不明，可能已经开工", "the ledger also could not be verified after the start"},
		{"解析 validate", i18n.E("err.projectctl.validate.parse", cause), "解析 validate 输出失败：exit status 2", "cannot parse the validate output"},
		{"找不到状态行", i18n.E("err.projectctl.briefstate.nostateline", "n1"), "brief 输出里找不到节点 n1 的状态行", "the brief output has no state line for node n1"},
		{"找不到脚本", i18n.E("err.projectctl.defaultscript.3", "projectctl.py", cause), "找不到 projectctl.py，请用 WORKDECK_PROJECTCTL 指定：exit status 2", "cannot find projectctl.py; set WORKDECK_PROJECTCTL"},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.zh {
			t.Errorf("%s 中文原文：%q，想要 %q", c.name, got, c.zh)
		}
		code, args, ok := i18n.Wire(c.err)
		if !ok {
			t.Errorf("%s 没有码", c.name)
			continue
		}
		i18n.SetLang(i18n.EN)
		got := deckclient.Text(code, args, c.err.Error())
		i18n.SetLang(i18n.ZH)
		if !strings.Contains(got, c.en) || hanIn(got) {
			t.Errorf("%s 英文：%q，想要含 %q 且无中文", c.name, got, c.en)
		}
	}
}

// 开工缺字段：缺的「节点」用码，不是压进参数的中文。
func TestN060StartMissingFieldIsCoded(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	_, err := NewClient(t.TempDir(), "x.py").Start(context.Background(), StartRequest{Actor: "a", Role: "r", RouteClass: "c", Model: "m", Reason: "r"})
	if err == nil || err.Error() != "开工缺少 节点，不调 projectctl start" {
		t.Fatalf("中文原文不变：%v", err)
	}
	code, args, ok := i18n.Wire(err)
	if !ok || code != "err.projectctl.start.missing" {
		t.Fatalf("应带码：%q", code)
	}
	if n, isNest := args[0].(i18n.Nest); !isNest || n.Code != "err.projectctl.start.node" {
		t.Fatalf("缺的字段名应是嵌套码：%#v", args[0])
	}
	i18n.SetLang(i18n.EN)
	en := deckclient.Text(code, args, err.Error())
	i18n.SetLang(i18n.ZH)
	if hanIn(en) || en != "start is missing the node; not calling projectctl start" {
		t.Fatalf("英文：%q", en)
	}
	// 其它字段名本来就是英文，原样
	_, err = NewClient(t.TempDir(), "x.py").Start(context.Background(), StartRequest{NodeID: "n", Role: "r", RouteClass: "c", Model: "m", Reason: "r"})
	if err == nil || err.Error() != "开工缺少 actor，不调 projectctl start" {
		t.Fatalf("缺 actor：%v", err)
	}
}

// 真脚本一次失败的往返：脚本退出非零、stdout 不是 JSON，stderr 作外部原文。
func TestN060RunFailureKeepsStderrAsExternalText(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	dir := t.TempDir()
	script := filepath.Join(dir, "fake.py")
	wirecheck.External("stderr 里的原文 EXTERNAL") // 夹具脚本的 stderr：外部原文
	if err := os.WriteFile(script, []byte("import sys\nsys.stderr.write('stderr 里的原文 EXTERNAL')\nsys.exit(3)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewClient(dir, script).Validate(context.Background())
	if err == nil {
		t.Fatal("应失败")
	}
	code, args, ok := i18n.Wire(err)
	if !ok || code != "err.projectctl.run.1" || len(args) != 3 || args[0] != "validate" {
		t.Fatalf("码和参数：%q %v", code, args)
	}
	if s, _ := args[2].(string); !strings.Contains(s, "stderr 里的原文 EXTERNAL") {
		t.Fatalf("stderr 应作为外部原文字符串参数：%#v", args[2])
	}
	i18n.SetLang(i18n.EN)
	en := deckclient.Text(code, args, err.Error())
	i18n.SetLang(i18n.ZH)
	if !strings.HasPrefix(en, "projectctl validate failed to run: exit status 3 (stderr: stderr 里的原文 EXTERNAL") || strings.Contains(strings.ReplaceAll(en, "stderr 里的原文", ""), "执行失败") {
		t.Fatalf("英文：%q", en)
	}
}

// 进度面板的「问题」是 projectctl 适配层取数失败的原因，owner 看得到：每条带码，Problems 仍是中文原文。
func TestN060SnapshotProblemsAreCoded(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	root := t.TempDir() // 没有 work-graph.yaml
	snap := NewReader(root, filepath.Join(root, "x.py")).FastSnapshot(context.Background())
	if len(snap.Problems) != 1 || len(snap.ProblemErrs) != 1 {
		t.Fatalf("应有一条问题，文字和错误一一对应：%v / %v", snap.Problems, snap.ProblemErrs)
	}
	if !strings.HasPrefix(snap.Problems[0], "读不到 "+filepath.Join(root, "work-graph.yaml")+"：") || snap.Problems[0] != snap.ProblemErrs[0].Error() {
		t.Fatalf("中文原文不变：%q", snap.Problems[0])
	}
	code, args, ok := i18n.Wire(snap.ProblemErrs[0])
	if !ok || code != "err.projectctl.graph.read" {
		t.Fatalf("应带码：%q", code)
	}
	i18n.SetLang(i18n.EN)
	en := deckclient.Text(code, args, snap.Problems[0])
	i18n.SetLang(i18n.ZH)
	if hanIn(en) || !strings.HasPrefix(en, "cannot read ") {
		t.Fatalf("英文：%q", en)
	}
	// 预算单位有语言无关的写法
	g := Graph{}
	lines := budgets(g, Projection{}, Freshness{})
	want := map[string]string{"worker rounds": "round", "frontier calls": "call", "elapsed": "hour", "tokens": ""}
	for _, l := range lines {
		if l.UnitKey != want[l.Label] {
			t.Fatalf("%s 的 UnitKey：%q", l.Label, l.UnitKey)
		}
	}
}
