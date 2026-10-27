package deckd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/sessionenv"
)

// n060 段 4：CLI 发现用会话 PATH、Codex 候选经 codex debug models、Codex 默认三档、加入 Kimi Code。
// 登录 shell 一律用假的（Resolver.Run 注入），不运行 owner 的 shell 启动文件；CLI 一律是临时目录里的假脚本，不调真 CLI。

const codexSample = "testdata/codex-debug-models-0.164.0.trimmed.json"

// fakeCLI 在 dir 里写一个假的 agent CLI：--version 打印 version，`debug models` 打印 codex 的样本，每次运行把「argv0|PATH|参数」记进 log。
func fakeCLI(t *testing.T, dir, name, version, log string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sample, err := filepath.Abs(codexSample)
	if err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\necho \"$0|$PATH|$*\" >> " + log + "\n" +
		"case \"$*\" in\n  --version) echo \"" + version + "\";;\n  \"debug models\") cat " + sample + ";;\nesac\n"
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// sessionResolver 是一个「登录 shell 的 PATH 是 login」的 Resolver（Run 注入，不运行任何 shell）。
func sessionResolver(t *testing.T, login string) *sessionenv.Resolver {
	t.Helper()
	sh := filepath.Join(t.TempDir(), "zsh")
	if err := os.WriteFile(sh, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := sessionenv.NewLoginResolver(nil)
	r.Getenv = func(k string) string {
		if k == "SHELL" {
			return sh
		}
		return os.Getenv(k)
	}
	r.Environ = func() []string { return nil }
	r.Run = func(_ context.Context, _ string, args, _ []string) ([]byte, error) {
		if login == "" {
			return nil, errors.New("exit status 1")
		}
		i := strings.Index(args[3], "'ANPAI")
		rest := args[3][i+1:]
		m := rest[:strings.Index(rest, "B'")]
		return []byte("rc noise\n" + m + "B" + login + m + "E\n"), nil
	}
	return r
}

type ctxUp struct {
	*fakeUp
	mu    sync.Mutex
	paths []string
}

func (u *ctxUp) Launch(ctx context.Context, req agentdeck.LaunchRequest) (agentdeck.LaunchResult, error) {
	p, _ := agentdeck.SessionPathOf(ctx)
	u.mu.Lock()
	u.paths = append(u.paths, p)
	u.mu.Unlock()
	return u.fakeUp.Launch(ctx, req)
}

// 「deckd 启动环境里没有、会话 PATH 里有」「两处版本不同时以会话那份为准」：设置里显示的路径与版本是会话 PATH 上的那份，
// 不是 deckd 自己 PATH 上的旧版；deckd 自己的 PATH 里没有的 Kimi 也认得出来。
func TestN060S4ToolsShowTheSessionExecutableNotTheProcessOne(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(root, "run.log")
	oldDir, newDir := filepath.Join(root, "old"), filepath.Join(root, "kimi-code", "bin")
	fakeCLI(t, oldDir, "codex", "codex-cli 0.139.0", log)
	sessionCodex := fakeCLI(t, newDir, "codex", "codex-cli 0.164.0", log)
	sessionKimi := fakeCLI(t, newDir, "kimi", "kimi 2.3.2", log)
	t.Setenv("PATH", oldDir+":/usr/bin:/bin") // deckd 自己的 PATH：旧版 codex、没有 kimi

	w := newAgentWorld(t)
	w.srv.agents.toolInfo = scopedToolInfo
	w.srv.cfg.SessionEnv = sessionResolver(t, newDir)
	code, b := w.do("owner", "GET", "/v1/tools", "")
	var tools []ToolStatus
	if err := json.Unmarshal([]byte(b), &tools); err != nil || code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	by := map[string]ToolStatus{}
	for _, x := range tools {
		by[x.Tool] = x
	}
	if c := by["codex"]; !c.Installed || c.Path != sessionCodex || c.Version != "codex-cli 0.164.0" || c.PathSource != "login-shell" || c.NoteCode != "" {
		t.Fatalf("codex 应是会话 PATH 上的 0.164.0：%+v", c)
	}
	if k := by["kimi"]; !k.Installed || k.Path != sessionKimi || k.Version != "kimi 2.3.2" || k.CanList {
		t.Fatalf("deckd 自己的 PATH 里没有的 kimi 应认得出（不可列模型）：%+v", k)
	}
	// 对照：不开会话 PATH（旧行为）时 codex 是旧版、kimi 找不到
	w.srv.cfg.SessionEnv = nil
	w.srv.agents.toolInfo = agentdeck.ToolInfo
	_, b = w.do("owner", "GET", "/v1/tools", "")
	var tools2 []ToolStatus
	json.Unmarshal([]byte(b), &tools2)
	by = map[string]ToolStatus{}
	for _, x := range tools2 {
		by[x.Tool] = x
	}
	if by["codex"].Version != "codex-cli 0.139.0" || by["kimi"].Installed || by["codex"].PathSource != "" {
		t.Fatalf("没开会话 PATH：按 deckd 自己的 PATH（旧行为）：%+v %+v", by["codex"], by["kimi"])
	}
}

// Codex trust 门记下的可执行文件与版本，就是会话里实际跑的那一个：同一次派工里 toolInfo 与 agent-deck launch 用同一份 PATH。
func TestN060S4TrustGateRecordsTheExecutableTheSessionRuns(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(root, "run.log")
	oldDir, newDir := filepath.Join(root, "old"), filepath.Join(root, "new")
	fakeCLI(t, oldDir, "codex", "codex-cli 0.139.0", log)
	sessionCodex := fakeCLI(t, newDir, "codex", "codex-cli 0.164.0", log)
	t.Setenv("PATH", oldDir+":/usr/bin:/bin")

	w := newAgentWorld(t)
	up := &ctxUp{fakeUp: w.up}
	w.srv.agents.upstream = up
	w.srv.agents.toolInfo = scopedToolInfo
	res := sessionResolver(t, newDir)
	w.srv.cfg.SessionEnv = res
	spawn := func(req string) (int, string) {
		return w.do("main", "POST", "/v1/agents", spawnJSON("economy", "codex 任务", "t", req, "/tmp/alpha", "", "codex", "gpt-6-luna"))
	}
	code, b := spawn("s4a")
	if code != 202 {
		t.Fatalf("第一次应问 Codex trust：%d %s", code, b)
	}
	dec := decode(t, b)["decision"].(map[string]any)
	scope := dec["scope"].(map[string]any)
	if scope["tool_path"] != sessionCodex || scope["tool_version"] != "codex-cli 0.164.0" {
		t.Fatalf("授权记的应是会话 PATH 上的 codex 与它的版本：%v", scope)
	}
	if code, b := w.do("owner", "POST", "/v1/decisions/"+dec["id"].(string)+"/answer", fmt.Sprintf(`{"option":"approve","version":%v}`, dec["version"])); code != 200 {
		t.Fatalf("批准：%d %s", code, b)
	}
	if code, b := spawn("s4b"); code != 201 {
		t.Fatalf("批准过的版本再开应直接开：%d %s", code, b)
	}
	snap := res.Get(context.Background())
	// 返工 R1：PATH 字符串相同并不保证上游找到的是同一个文件（核准之后前面的目录里新装一个同名文件，字符串一字不变）。
	// 所以这里不再拿「launch 收到的 PATH == 查可执行文件用的 PATH」当凭据，而是看绑定：授权文件所在目录在最前面，且从中解析出的就是授权的文件。
	// （此处授权文件的目录本来就排在快照最前，所以与快照仍相等；目录不在最前时的情形见 n060_s4fix_r1_test.go。）
	if len(up.paths) != 1 || up.paths[0] != snap.Path || !strings.HasPrefix(up.paths[0], filepath.Dir(sessionCodex)+":") {
		t.Fatalf("agent-deck launch 收到的会话 PATH 应以授权文件所在目录开头：%q（快照 %q）", up.paths, snap.Path)
	}
	// 实际会话里按这份 PATH 找 codex，找到的就是授权里记的那个
	if got := sessionenv.LookPath(up.paths[0], "codex"); got != scope["tool_path"] {
		t.Fatalf("会话 PATH 上的 codex 是 %q，授权记的是 %v", got, scope["tool_path"])
	}
	// 没开会话 PATH 解析时：不读登录 shell，但授权文件所在目录同样前置后交给上游（返工 R1；此前这里 launch 不带 PATH）
	w.srv.cfg.SessionEnv = nil
	w.srv.agents.toolInfo = func(context.Context, string) (string, string, error) { return sessionCodex, "codex-cli 0.164.0", nil }
	if code, b := spawn("s4c"); code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	if len(up.paths) != 2 || !strings.HasPrefix(up.paths[1], filepath.Dir(sessionCodex)+":") || sessionenv.LookPath(up.paths[1], "codex") != sessionCodex {
		t.Fatalf("没开会话 PATH 解析：launch 带的 PATH 应以授权文件所在目录开头并解析到授权的文件：%q", up.paths)
	}
}

// 会话 PATH 取不到：回退 deckd 自己的 PATH，并给出可译的原因（界面提示 owner），中英各一遍。
func TestN060S4SessionPathFallbackIsExplainedAndTranslated(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(root, "run.log")
	own := filepath.Join(root, "own")
	fakeCLI(t, own, "codex", "codex-cli 0.150.0", log)
	t.Setenv("PATH", own+":/usr/bin:/bin")
	w := newAgentWorld(t)
	w.srv.agents.toolInfo = scopedToolInfo
	w.srv.cfg.SessionEnv = sessionResolver(t, "") // 登录 shell 运行失败
	code, b := w.do("owner", "GET", "/v1/tools", "")
	var tools []ToolStatus
	if err := json.Unmarshal([]byte(b), &tools); err != nil || code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	for _, x := range tools {
		if x.PathSource != "process" || x.NoteCode != "err.deckd.servergettools.nopath" || x.Note == "" {
			t.Fatalf("回退时每项都带来源与原因：%+v", x)
		}
	}
	if c := tools[1]; c.Tool != "codex" || c.Version != "codex-cli 0.150.0" || !c.Installed {
		t.Fatalf("回退时仍按 deckd 自己的 PATH 找：%+v", c)
	}
	x := tools[0]
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	i18n.SetLang(i18n.ZH)
	zh := deckclient.Text(x.NoteCode, x.NoteArgs, x.Note)
	if !strings.Contains(zh, "没取到会话的 PATH") || !strings.Contains(zh, "exit status 1") {
		t.Fatalf("中文：%q", zh)
	}
	i18n.SetLang(i18n.EN)
	en := deckclient.Text(x.NoteCode, x.NoteArgs, x.Note)
	if hasHan(en) || !strings.Contains(en, "Couldn't get the session PATH") || !strings.Contains(en, "exit status 1") {
		t.Fatalf("英文要翻译、没有汉字：%q", en)
	}
}

// 每一种取不到会话 PATH 的原因都有码、中英各一句、英文没有汉字。
func TestN060S4EverySessionPathErrorCodeHasBothLanguages(t *testing.T) {
	for _, e := range []*i18n.Error{
		i18n.E("err.sessionenv.noshell", ""), i18n.E("err.sessionenv.timeout", "/bin/zsh", "5s"), i18n.E("err.sessionenv.failed", "/bin/zsh", "boom"),
		i18n.E("err.sessionenv.nooutput", "/bin/zsh"), i18n.E("err.sessionenv.toolong", "/bin/zsh"), i18n.E("err.sessionenv.recursion", sessionenv.ProbeEnv),
		i18n.E("err.agentdeck.toolinfo.3", "codex"), i18n.E("err.agentdeck.listmodels.toolarge", "codex"), i18n.E("err.agentdeck.listmodels.timeout", "codex", "20s"),
		i18n.E("err.deckd.codexmodels.badjson", errors.New("bad")), i18n.E("err.deckd.codexmodels.empty"),
		i18n.E("err.deckd.servergettools.nopath", i18n.E("err.sessionenv.timeout", "/bin/zsh", "5s")),
	} {
		code, args, ok := i18n.Wire(e)
		if !ok {
			t.Fatalf("%v 没有码", e.Key)
		}
		t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
		i18n.SetLang(i18n.ZH)
		zh := deckclient.Text(code, args, e.Error())
		i18n.SetLang(i18n.EN)
		en := deckclient.Text(code, args, e.Error())
		if zh == en || hasHan(en) || en == "" || strings.Contains(en, "err.") {
			t.Errorf("%s：中文 %q，英文 %q", code, zh, en)
		}
	}
}

func listedSlugs(t *testing.T) []string {
	t.Helper()
	return []string{"gpt-6.1-sol", "gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"}
}

// Codex 加模型时候选来自 `codex debug models`：只取 visibility == "list"，用会话 PATH 上的那个 codex 跑（不是 deckd 自己 PATH 上的）。
func TestN060S4CodexCandidatesComeFromDebugModelsThroughTheSessionExecutable(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(root, "run.log")
	oldDir, newDir := filepath.Join(root, "old"), filepath.Join(root, "new")
	fakeCLI(t, oldDir, "codex", "codex-cli 0.139.0", log)
	fakeCLI(t, newDir, "codex", "codex-cli 0.164.0", log)
	t.Setenv("PATH", oldDir+":/usr/bin:/bin")
	w := newAgentWorld(t)
	w.srv.cfg.SessionEnv = sessionResolver(t, newDir)
	code, b := w.do("owner", "GET", "/v1/tools/models?tool=codex", "")
	var out struct {
		Models []string `json:"models"`
	}
	if err := json.Unmarshal([]byte(b), &out); err != nil || code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if got, want := strings.Join(out.Models, ","), strings.Join(listedSlugs(t), ","); got != want {
		t.Fatalf("只取 visibility 为 list 的（hide 的 gpt-reserve、codex-auto-review 不列）：\n%s\n%s", got, want)
	}
	for _, hidden := range []string{"gpt-reserve", "codex-auto-review"} {
		if strings.Contains(b, hidden) {
			t.Fatalf("hide 的不能出现：%s", b)
		}
	}
	l, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(l)), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], filepath.Join(newDir, "codex")+"|"+newDir+":") || !strings.HasSuffix(lines[0], "|debug models") {
		t.Fatalf("跑的应是会话 PATH 上的 codex、PATH 是会话 PATH、参数是 debug models：%q", lines)
	}
	if strings.Contains(string(l), oldDir+"/codex") {
		t.Fatalf("不能跑 deckd 自己 PATH 上的旧版：%s", l)
	}
	// 目录项标明能列
	_, b = w.do("owner", "GET", "/v1/tools", "")
	var tools []ToolStatus
	json.Unmarshal([]byte(b), &tools)
	for _, x := range tools {
		if want := x.Tool == "codex" || x.Tool == "opencode" || x.Tool == "grok" || x.Tool == "antigravity"; x.CanList != want {
			t.Errorf("%s 能不能列模型：%v，应为 %v", x.Tool, x.CanList, want)
		}
	}
}

// 输出异常、非 JSON、超时、超大：都退回手填并给出可译的原因（502 + 码 + 嵌套码），英文没有汉字；Claude、Kimi 照旧手填不跑任何命令。
func TestN060S4CodexCandidateFailuresFallBackToManualWithTranslatedReasons(t *testing.T) {
	sample, err := os.ReadFile(codexSample)
	if err != nil {
		t.Fatal(err)
	}
	old := listModels
	t.Cleanup(func() { listModels = old; i18n.SetLang(i18n.ZH) })
	for _, c := range []struct {
		name   string
		out    string
		err    error
		nested string
	}{
		{"不是 JSON", "Error: not logged in\n", nil, "err.deckd.codexmodels.badjson"},
		{"空输出", "", nil, "err.deckd.codexmodels.badjson"},
		{"JSON 但没有 models", `{"data":[]}`, nil, "err.deckd.codexmodels.empty"},
		{"models 全是 hide", `{"models":[{"slug":"gpt-x","visibility":"hide"}]}`, nil, "err.deckd.codexmodels.empty"},
		{"visibility 写法不对不算 list", `{"models":[{"slug":"a-1","visibility":"List"},{"slug":"a-2","visibility":"listed"},{"slug":"a-3"}]}`, nil, "err.deckd.codexmodels.empty"},
		{"slug 不合规的丢掉、没有剩下", `{"models":[{"slug":"x;rm -rf ~","visibility":"list"},{"slug":"","visibility":"list"}]}`, nil, "err.deckd.codexmodels.empty"},
		{"截断的 JSON", string(sample[:len(sample)/2]), nil, "err.deckd.codexmodels.badjson"},
		{"命令超时", "", i18n.E("err.agentdeck.listmodels.timeout", "codex", "20s"), "err.agentdeck.listmodels.timeout"},
		{"输出超大", "", i18n.E("err.agentdeck.listmodels.toolarge", "codex"), "err.agentdeck.listmodels.toolarge"},
		{"找不到 codex", "", i18n.E("err.agentdeck.toolinfo.3", "codex"), "err.agentdeck.toolinfo.3"},
		{"命令失败", "", errors.New("exit status 2"), ""},
	} {
		w := newAgentWorld(t)
		listModels = func(context.Context, string, []string) (string, error) { return c.out, c.err }
		code, b := w.do("owner", "GET", "/v1/tools/models?tool=codex", "")
		var e struct {
			Error string `json:"error"`
			Code  string `json:"code"`
			Args  []any  `json:"args"`
		}
		json.Unmarshal([]byte(b), &e)
		if code != 502 || e.Code != "err.deckd.servergettoolmodels.2" || len(e.Args) != 3 {
			t.Errorf("%s：应 502 并带码：%d %s", c.name, code, b)
			continue
		}
		inner, _ := e.Args[2].(map[string]any)
		if c.nested != "" && (inner == nil || inner["code"] != c.nested) {
			t.Errorf("%s：参数里嵌套的码应是 %s：%v", c.name, c.nested, e.Args[2])
		}
		if !strings.Contains(e.Error, "手填") {
			t.Errorf("%s：兜底的中文要说手填：%q", c.name, e.Error)
		}
		// 界面按语言翻译：中文说手填、英文没有汉字
		i18n.SetLang(i18n.ZH)
		if zh := deckclient.Text(e.Code, e.Args, e.Error); !strings.Contains(zh, "手填") {
			t.Errorf("%s：中文 %q", c.name, zh)
		}
		i18n.SetLang(i18n.EN)
		if en := deckclient.Text(e.Code, e.Args, e.Error); hasHan(en) || !strings.Contains(en, "type the model id by hand") {
			t.Errorf("%s：英文 %q", c.name, en)
		}
		i18n.SetLang(i18n.ZH)
	}
	// Claude、Kimi 没有列模型的命令：仍手填，一个命令都不跑
	ran := 0
	listModels = func(context.Context, string, []string) (string, error) { ran++; return "", nil }
	w := newAgentWorld(t)
	for _, tool := range []string{"claude", "kimi"} {
		if code, b := w.do("owner", "GET", "/v1/tools/models?tool="+tool, ""); code != 200 || !strings.Contains(b, "手填") || !strings.Contains(b, `"models":[]`) {
			t.Errorf("%s：%d %s", tool, code, b)
		}
	}
	if ran != 0 {
		t.Fatalf("没有列模型的命令不该跑任何东西：%d", ran)
	}
}

// parseCodexModels 本身：样本的 7 个 list、顺序保持、去重、上限、非法 slug、伪造打码标记都不会流到候选里。
func TestN060S4ParseCodexModels(t *testing.T) {
	sample, err := os.ReadFile(codexSample)
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseCodexModels(string(sample))
	if err != nil || strings.Join(got, ",") != strings.Join(listedSlugs(t), ",") {
		t.Fatalf("%v %v", got, err)
	}
	dup := `{"models":[{"slug":"a","visibility":"list"},{"slug":"a","visibility":"list"},{"slug":"b","visibility":"list"}]}`
	if got, _ := parseCodexModels(dup); strings.Join(got, ",") != "a,b" {
		t.Fatalf("去重：%v", got)
	}
	var many strings.Builder
	many.WriteString(`{"models":[`)
	for i := 0; i < maxCodexModels+50; i++ {
		if i > 0 {
			many.WriteString(",")
		}
		fmt.Fprintf(&many, `{"slug":"m-%d","visibility":"list"}`, i)
	}
	many.WriteString("]}")
	if got, _ := parseCodexModels(many.String()); len(got) != maxCodexModels {
		t.Fatalf("最多 %d 个：%d", maxCodexModels, len(got))
	}
	forged := `{"models":[{"slug":"` + "\\" + `ue000x","visibility":"list"},{"slug":"ok-1","visibility":"list"}]}`
	if got, _ := parseCodexModels(forged); strings.Join(got, ",") != "ok-1" {
		t.Fatalf("转义写法的打码标记字符进不了候选：%v", got)
	}
	// 额外字段（很长的 base_instructions 之类）不碍事
	extra := `{"models":[{"slug":"a","visibility":"list","base_instructions":"` + strings.Repeat("x", 1<<20) + `","other":{"n":[1,2,3]}}],"extra":1}`
	if got, err := parseCodexModels(extra); err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
}

// 目录里的默认：Codex 三档 astra / 6.1-sol / 6-luna，去掉 gpt-5.6-terra；加入 Kimi Code（命令 kimi，k3 日常、for-coding 便宜）。
func TestN060S4CatalogDefaults(t *testing.T) {
	codex, ok := CatalogTool("codex")
	if !ok || fmt.Sprint(codex.Models) != fmt.Sprint([]deckstore.ModelDef{{ID: "gpt-6-astra", Tier: deckstore.Frontier}, {ID: "gpt-6.1-sol", Tier: deckstore.Balanced}, {ID: "gpt-6-luna", Tier: deckstore.Economy}}) {
		t.Fatalf("Codex 默认三档：%+v", codex.Models)
	}
	kimi, ok := CatalogTool("kimi")
	if !ok || kimi.Name != "Kimi Code" || kimi.Binary != "kimi" || !kimi.Agent || len(kimi.listArgs) != 0 || kimi.parse != nil || kimi.parseErr != nil ||
		fmt.Sprint(kimi.Models) != fmt.Sprint([]deckstore.ModelDef{{ID: "kimi-code/k3", Tier: deckstore.Balanced}, {ID: "kimi-for-coding", Tier: deckstore.Economy}}) {
		t.Fatalf("Kimi Code：%+v", kimi)
	}
	for _, e := range Catalog {
		for _, m := range e.Models {
			if m.ID == "gpt-5.6-terra" || m.ID == "gpt-5.6-luna" {
				t.Errorf("%s 里不该还有 %s", e.Tool, m.ID)
			}
			if !deckstore.ValidModelID(m.ID) {
				t.Errorf("%s 的默认模型 id 不合规：%q", e.Tool, m.ID)
			}
		}
	}
}

func kimiWorld(t *testing.T) *agentWorld {
	w := newAgentWorld(t)
	set := w.store.Settings()
	for i := range set.Projects {
		set.Projects[i].Agents = []string{"claude", "codex", "kimi"}
		set.Projects[i].Models[deckstore.Balanced] = deckstore.ModelList{"kimi-code/k3"}
	}
	if err := w.store.ReplaceSettings(set); err != nil {
		t.Fatal(err)
	}
	b := w.srv.agents.bypass
	b.NotCodex = func(tool string) bool { return tool == "kimi" }
	w.srv.WithBypassChecks(b)
	return w
}

// Kimi 的派工：模型经命令传（LaunchRequest 的 Tool 是 kimi、Model 是所选模型，agentdeck 拼 -c "kimi --model …"），
// 不是 Codex 兼容的不问 Codex 授权；上游把它起成了别的工具就停掉；没确认不是 Codex 兼容就照旧上报。
func TestN060S4KimiSpawnPassesTheModelAndChecksTheTool(t *testing.T) {
	w := kimiWorld(t)
	if code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "试 kimi", "t", "k4a", "/tmp/alpha", "", "kimi", "kimi-code/k3")); code != 201 {
		t.Fatalf("登记过、不是 Codex 兼容的 kimi：直接开：%d %s", code, b)
	}
	l := w.up.launches[len(w.up.launches)-1]
	if l.Tool != "kimi" || l.Model != "kimi-code/k3" || l.ModelNotApplicable {
		t.Fatalf("启动请求：%+v", l)
	}
	if agentdeck.ToolsModelViaCommand[l.Tool] != "--model" {
		t.Fatalf("模型经命令传（拼成 -c \"kimi --model '<模型>'\" 见 agentdeck.TestN060S4KimiModelGoesThroughTheCommand）：%v", agentdeck.ToolsModelViaCommand)
	}
	// 启动后核对进程参数时给的白名单依据是 kimi
	if want := w.procWant[len(w.procWant)-1]; want.Tool != "kimi" || want.Binary != "kimi" {
		t.Fatalf("核对依据：%+v", want)
	}
	// 上游没登记 [tools.kimi] 时会把它认成 shell 起：停掉、不送任务
	w.up.launchOut = &agentdeck.LaunchResult{SessionID: "ad-shell4", Success: true, Tool: "shell", Command: "kimi --model 'kimi-code/k3'"}
	code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "试 kimi", "t", "k4b", "/tmp/alpha", "", "kimi", "kimi-code/k3"))
	if code != 502 || !strings.Contains(b, "[tools.kimi]") || !strings.Contains(strings.Join(w.up.stops, ","), "ad-shell4") {
		t.Fatalf("没登记时上游起成 shell：停掉并说明要登记 [tools.kimi]：%d %s stops=%v", code, b, w.up.stops)
	}
	// 没确认不是 Codex 兼容：照旧问 owner
	b2 := w.srv.agents.bypass
	b2.NotCodex = nil
	w.srv.WithBypassChecks(b2)
	w.up.launchOut = nil
	if code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "试 kimi", "t", "k4c", "/tmp/alpha", "", "kimi", "kimi-code/k3")); code != 202 || !strings.Contains(b, "codex_trust") {
		t.Fatalf("确认不了时照旧上报 Codex 授权：%d %s", code, b)
	}
}

// 不开会话 PATH（测试、没传）时 deckd.Run 不会去运行 owner 的 shell；deck daemon 才开。
func TestN060S4OnlyDeckDaemonTurnsOnTheLoginShellProbe(t *testing.T) {
	var c Config
	c.startSessionEnv(context.Background())
	if c.SessionEnv != nil {
		t.Fatal("没设 SessionPath 就不该建解析登录 shell 的 Resolver")
	}
	c = Config{SessionPath: true, SessionEnv: sessionenv.NewStatic()}
	st := c.SessionEnv
	c.startSessionEnv(context.Background())
	if c.SessionEnv != st {
		t.Fatal("已有 Resolver 不覆盖")
	}
	b, err := os.ReadFile("../../cmd/deck/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "SessionPath: true") {
		t.Fatal("deck daemon 要把 Config.SessionPath 设为 true")
	}
	run, err := os.ReadFile("daemon.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(run), "cfg.startSessionEnv(ctx)") || !strings.Contains(string(run), "srv.WithAgents(up, leases, scopedToolInfo, gs)") {
		t.Fatal("deckd.Run 要先建 Resolver，并用会话 PATH 版的 toolInfo")
	}
}
