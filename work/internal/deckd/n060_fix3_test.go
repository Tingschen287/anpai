package deckd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/wirecheck"
)

func reset(t *testing.T) {
	t.Helper()
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
}

// doHdr 和 do 一样，多带一个请求头（界面用 i18n.MarkHeader 声明自己会翻译打码标记）。
func (f *fixture) doHdr(as, method, path, body string, hdr map[string]string) (int, string) {
	f.t.Helper()
	req, _ := http.NewRequest(method, f.http.URL+path, strings.NewReader(body))
	req.Header.Set("X-Test-As", as)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, sb.String()
}

// markedOption 取决定里带中性标记的那个选项。
func markedOption(d deckstore.Decision) string {
	for _, o := range d.Options {
		if strings.Contains(o, i18n.RedactMark) {
			return o
		}
	}
	return ""
}

var uiHdr = map[string]string{i18n.MarkHeader: i18n.MarkNeutral}

// 任务 2：Graph 开工失败里「开工前核对不了账本」「执行失败」是本项目适配层写的中文，改成码；
// 只有 projectctl 的 stderr 是外部原文。走真实 Handler、真实 projectctl 适配层（脚本不存在），用英文断言。
func TestN060GraphStartFailureIsCodedThroughHandler(t *testing.T) {
	reset(t)
	w, _ := graphWorld(t)
	set := w.store.Settings()
	for i := range set.Projects {
		set.Projects[i].GraphRoot = t.TempDir()
	}
	if err := w.store.ReplaceSettings(set); err != nil {
		t.Fatal(err)
	}
	w.srv.agents.graphStart = projectctlGraphStart(filepath.Join(t.TempDir(), "no-such-projectctl.py"))

	code, b := w.do("main", "POST", "/v1/agents", spawnGraphJSON("balanced", "node-job", "do n099", "gf1", "/tmp/alpha", "n099_x", "claude", "claude-sonnet-5-5"))
	if code != 409 {
		t.Fatalf("开不成一轮应是 409：%d %s", code, b)
	}
	m := decode(t, b)
	zh, _ := m["error"].(string)
	// 中文原文逐字不变
	for _, want := range []string{"Graph 节点 n099_x 开工失败，刚起的会话已停掉、不送任务：", "开工前核对不了账本（projectctl verify-chain 执行失败：", "不开工：开工出岔子时没法对账"} {
		if !strings.Contains(zh, want) {
			t.Fatalf("中文 error 少了 %q：%s", want, zh)
		}
	}
	c, _ := m["code"].(string)
	args, _ := m["args"].([]any)
	if c != "err.deckd.serverrunspawn.19" {
		t.Fatalf("应带码：%v", m)
	}
	i18n.SetLang(i18n.EN)
	en := deckclient.Text(c, args, zh)
	if hasHan(en) || !strings.Contains(en, "cannot verify the ledger before starting") || !strings.Contains(en, "failed to run") {
		t.Fatalf("英文响应不应留下本地中文：%q", en)
	}
	// owner 看到的通知（落盘的码和参数，再按英文渲染）同样没有本地中文
	i18n.SetLang(i18n.ZH)
	var seen int
	for _, n := range w.store.Notifications(0, "alpha") {
		if !strings.Contains(n.Text, "开工前核对不了账本") {
			continue
		}
		seen++
		if n.Code == "" {
			t.Fatalf("通知应带码：%+v", n)
		}
		i18n.SetLang(i18n.EN)
		got := deckclient.Text(n.Code, []any(n.Args), n.Text)
		i18n.SetLang(i18n.ZH)
		if hasHan(got) {
			t.Fatalf("英文通知不应留下本地中文：%q", got)
		}
	}
	if seen == 0 {
		t.Fatal("应有一条带「开工前核对不了账本」的通知")
	}
}

// 任务 1：线路上的打码标记按客户端翻译。界面（带 MarkHeader）拿到中性标记，自己按语言换；
// 没带头的客户端（deck agent 命令、模型）拿到的是中文「[已打码]」，和改码之前逐字一样。
func TestN060MarkFilterByClient(t *testing.T) {
	reset(t)
	f := newFixture(t)
	code, b := f.do("main", "POST", "/v1/decisions", `{"kind":"cost","summary":"ship sk-FAKETOKEN99 now","options":["go sk-FAKETOKEN99"],"recommend":"go sk-FAKETOKEN99"}`)
	if code != 201 {
		t.Fatalf("开决定：%d %s", code, b)
	}
	// 没带头：中文记号，没有标记字符
	_, plain := f.do("owner", "GET", "/v1/decisions", "")
	if strings.Contains(plain, "sk-FAKE") || strings.Contains(plain, i18n.RedactMark) || !strings.Contains(plain, "ship [已打码] now") || !strings.Contains(plain, "go [已打码]") {
		t.Fatalf("没带头的客户端应拿到中文记号：%s", plain)
	}
	// 带头：留着中性标记，不出现任何语言的记号
	_, neutral := f.doHdr("owner", "GET", "/v1/decisions", "", uiHdr)
	if strings.Contains(neutral, "sk-FAKE") || strings.Contains(neutral, "[已打码]") || strings.Count(neutral, i18n.RedactMark) < 3 {
		t.Fatalf("界面应拿到中性标记：%s", neutral)
	}
	// 通知：同一条规则
	_, notes := f.do("owner", "GET", "/v1/notifications", "")
	if strings.Contains(notes, i18n.RedactMark) || strings.Contains(notes, "sk-FAKE") {
		t.Fatalf("没带头的客户端的通知不应有标记字符：%s", notes)
	}
	// 界面拿到的通知：参数里是中性标记（英文界面据此显示 [redacted]）；正文 text 是中文兜底，永远是中文记号
	_, notesUI := f.doHdr("owner", "GET", "/v1/notifications", "", uiHdr)
	if strings.Contains(notesUI, "sk-FAKE") || !strings.Contains(notesUI, `"args":["ship `+i18n.RedactMark+` now"]`) || !strings.Contains(notesUI, "ship [已打码] now") {
		t.Fatalf("界面的通知参数应是中性标记、正文是中文兜底：%s", notesUI)
	}
	// 回答决定：两种写法的选项都认（界面回传带标记的，没带头的回传中文记号的）
	var d deckstore.Decision
	for _, x := range f.store.Decisions("alpha") {
		d = x
	}
	opt := markedOption(d)
	if opt == "" {
		t.Fatalf("存下来的选项应有一条带中性标记：%q", d.Options)
	}
	zhOpt := i18n.PresentZH(opt)
	if c, body := f.do("owner", "POST", "/v1/decisions/"+d.ID+"/answer", `{"option":"`+zhOpt+`","version":1}`); c != 200 {
		t.Fatalf("没带头的客户端回传中文记号的选项应被认：%d %s", c, body)
	}
	got, _ := f.store.Decision(d.ID)
	if got.State != deckstore.StateAnswered {
		t.Fatalf("应记为已回答：%s", got.State)
	}
	// 另一条：界面回传带标记的选项
	_, b = f.do("main", "POST", "/v1/decisions", `{"kind":"cost","summary":"two","options":["pick sk-FAKETOKEN99"]}`)
	var d2 deckstore.Decision
	for _, x := range f.store.Decisions("alpha") {
		if x.Summary == "two" {
			d2 = x
		}
	}
	if c, body := f.doHdr("owner", "POST", "/v1/decisions/"+d2.ID+"/answer", `{"option":"`+markedOption(d2)+`","version":1}`, uiHdr); c != 200 {
		t.Fatalf("界面回传带标记的选项应被认：%d %s", c, body)
	}
}

// 给模型读的文字在出口统一换成中文「[已打码]」：转告正文、子 agent 的任务说明、汇报提醒，和 63f6247 逐字一致。
func TestN060ModelFacingTextsHaveChineseMark(t *testing.T) {
	reset(t)
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	sub.Title = deckstore.Clean("题 sk-FAKETOKEN99", 80)
	rep := deckstore.Report{Sub: sub.Key, Source: deckstore.ReportActive, Text: deckstore.Clean("结论 sk-FAKETOKEN99 完", 2000)}
	if !strings.Contains(rep.Text, i18n.RedactMark) {
		t.Fatalf("前提：存下来的汇报带中性标记：%q", rep.Text)
	}
	got := w.srv.reportText(rep)
	if strings.Contains(got, i18n.RedactMark) || strings.Contains(got, "sk-FAKE") || !strings.Contains(got, "结论 [已打码] 完") {
		t.Fatalf("转告正文应是中文记号：%q", got)
	}
	p, _ := w.store.Settings().Project("alpha")
	brief := subBrief(p, sub, "任务")
	if strings.Contains(brief, i18n.RedactMark) || !strings.Contains(brief, "题 [已打码]") {
		t.Fatalf("任务说明里的标题应是中文记号：%q", brief)
	}
}

// 主 agent 的标题「主 agent · 项目名」是本项目拼的中文，英文界面不能直接显示：存码，界面按码翻译。
func TestN060MainTitleIsCoded(t *testing.T) {
	reset(t)
	w := newAgentWorld(t)
	old, _ := w.store.Session(w.main.Key)
	w.gone = map[string]bool{old.TmuxSession: true}
	code, b := w.do("owner", "POST", "/v1/projects/alpha/main", `{}`)
	if code != 201 {
		t.Fatalf("起新主 agent：%d %s", code, b)
	}
	sess := decode(t, b)["session"].(map[string]any)
	if sess["title"] != "主 agent · alpha" || sess["title_code"] != "err.deckd.serverpostmain.4" {
		t.Fatalf("标题中文不变、另带码：%v", sess)
	}
	x, ok := w.store.Session(sess["key"].(string))
	if !ok || x.TitleCode == "" {
		t.Fatal("存储里的会话应带标题的码")
	}
	i18n.SetLang(i18n.EN)
	if en := deckclient.Text(x.TitleCode, []any(x.TitleArgs), x.Title); hasHan(en) || en != "main agent: alpha" {
		t.Fatalf("英文标题：%q", en)
	}
	// 通知里提到这个标题（作为参数）时也翻成英文
	i18n.SetLang(i18n.ZH)
	var seen bool
	for _, n := range w.store.Notifications(0, "alpha") {
		if strings.Contains(n.Text, "主 agent · alpha") {
			seen = true
			i18n.SetLang(i18n.EN)
			en := deckclient.Text(n.Code, []any(n.Args), n.Text)
			i18n.SetLang(i18n.ZH)
			if hasHan(en) {
				t.Fatalf("英文通知里的标题不应是中文：%q", en)
			}
		}
	}
	if !seen {
		t.Fatal("应有一条提到主 agent 标题的通知")
	}
	// 用户自己起的标题没有码，当外部原文
	sub := spawnedSub(t, w)
	if sub.TitleCode != "" {
		t.Fatalf("自己起的标题不带码：%q", sub.TitleCode)
	}
}

// 运行时保险有牙：故意让 Handler 返回本地中文 error，钩子必须抓到；带码的、外部原文的不抓。
func TestN060WireHookCatchesLocalChinese(t *testing.T) {
	reset(t)
	serve := func(h http.HandlerFunc) {
		req := httptest.NewRequest("GET", "/v1/x", nil)
		markFilter{next: h}.ServeHTTP(httptest.NewRecorder(), req)
	}
	// 1. 本地中文 error，没有码：命中
	hits := wirecheck.Capture(func() {
		serve(func(w http.ResponseWriter, r *http.Request) {
			writeErr(w, http.StatusBadGateway, errors.New("本地写的中文错误：连不上上游"))
		})
	})
	if len(hits) != 1 || hits[0].Path != "$.error" || !strings.Contains(hits[0].Text, "本地写的中文错误") {
		t.Fatalf("本地中文 error 应被抓到：%+v", hits)
	}
	// 2. 带码的错误：error 是兜底中文，不抓
	if hits := wirecheck.Capture(func() {
		serve(func(w http.ResponseWriter, r *http.Request) { writeErr(w, 500, i18n.E("err.deckd.serverevents.1")) })
	}); len(hits) != 0 {
		t.Fatalf("带码的错误不该命中：%+v", hits)
	}
	// 3. 本地中文被压成字符串当参数：命中（严格口径）
	hits = wirecheck.Capture(func() {
		serve(func(w http.ResponseWriter, r *http.Request) {
			writeErr(w, 500, i18n.E("err.deckd.serverrunspawn.47", "本地写的中文参数"))
		})
	})
	if len(hits) == 0 || !strings.Contains(hits[0].Path, "args[]") {
		t.Fatalf("参数里的本地中文应被抓到：%+v", hits)
	}
	// 4. 登记为外部原文的参数不抓
	wirecheck.External("上游自己说的话 XYZ")
	if hits := wirecheck.Capture(func() {
		serve(func(w http.ResponseWriter, r *http.Request) {
			writeErr(w, 500, i18n.E("err.deckd.serverrunspawn.47", "上游自己说的话 XYZ"))
		})
	}); len(hits) != 0 {
		t.Fatalf("登记过的外部原文不该命中：%+v", hits)
	}
	// 5. 真实 Handler：whoami 把调用者身份错误原样放进 error，没有码
	f := newFixture(t)
	f.srv.callerFn = func(r *http.Request) Caller {
		return Caller{Role: RoleOwner, Err: errors.New("身份解析失败：本地写的中文")}
	}
	hits = wirecheck.Capture(func() {
		req := httptest.NewRequest("GET", "/v1/whoami", nil)
		f.srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
	})
	if len(hits) != 1 || hits[0].Path != "$.error" || !strings.Contains(hits[0].Kind, "/v1/whoami") {
		t.Fatalf("真实 Handler 返回本地中文 error 应被抓到：%+v", hits)
	}
	// 6. 构造带码错误时，参数是本地中文的无码错误：命中
	hits = wirecheck.Capture(func() { _ = i18n.E("err.deckd.serverrunspawn.47", errors.New("本地写的中文错误")) })
	if len(hits) != 1 {
		t.Fatalf("参数里的无码中文错误应被抓到：%+v", hits)
	}
	// 7. errors.Join 的结果拆成列表，每条各自看；英文的不抓
	if hits := wirecheck.Capture(func() { _ = i18n.E("err.deckd.serverrunspawn.47", errors.Join(errors.New("a"), errors.New("b"))) }); len(hits) != 0 {
		t.Fatalf("纯英文的 Join 不该命中：%+v", hits)
	}
}

// 回给界面的响应经 markFilter 时，请求体里客户端自己写的中文（标题、汇报）是外部原文，原样回显不算本地中文。
func TestN060WireHookTreatsRequestBodyAsExternal(t *testing.T) {
	reset(t)
	hits := wirecheck.Capture(func() {
		f := newFixture(t)
		f.do("main", "POST", "/v1/decisions", `{"kind":"cost","summary":"客户端自己写的中文摘要 QRS"}`)
		f.do("owner", "GET", "/v1/decisions", "")
	})
	if len(hits) != 0 {
		t.Fatalf("请求体里的文字回显不该命中：%+v", hits)
	}
}

// 进度接口：取数问题带码（problems_coded）才算过关；只有中文 problems 的响应被运行时保险抓到。
func TestN060ProgressProblemsAreCodedOnTheWire(t *testing.T) {
	reset(t)
	f := newFixture(t)
	set := f.store.Settings()
	for i := range set.Projects {
		set.Projects[i].GraphRoot = t.TempDir()
	}
	if err := f.store.ReplaceSettings(set); err != nil {
		t.Fatal(err)
	}
	read := i18n.E("err.projectctl.graph.read", "/x/work-graph.yaml", "no such file")
	f.srv.progress = func(ctx context.Context, p deckstore.ProjectDef) (any, error) {
		return Progress{Project: p.ID, Graph: true, Counts: map[string]int{}, Problems: []string{read.Error()},
			ProblemsCoded: []i18n.Nest{{Code: string(read.Key), Args: i18n.WireArgs(read.Args), Error: read.Error()}}}, nil
	}
	code, b := f.do("owner", "GET", "/v1/progress?project=alpha", "")
	if code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	m := decode(t, b)
	coded, _ := m["problems_coded"].([]any)
	if len(coded) != 1 || coded[0].(map[string]any)["code"] != "err.projectctl.graph.read" {
		t.Fatalf("应带 problems_coded：%s", b)
	}
	// 没带码的版本：运行时保险必须抓到
	f.srv.progress = func(ctx context.Context, p deckstore.ProjectDef) (any, error) {
		return Progress{Project: p.ID, Graph: true, Counts: map[string]int{}, Problems: []string{"本地写的中文问题"}}, nil
	}
	hits := wirecheck.Capture(func() { f.do("owner", "GET", "/v1/progress?project=alpha", "") })
	if len(hits) != 1 || hits[0].Path != "$.problems[]" {
		t.Fatalf("只有中文 problems 应被抓到：%+v", hits)
	}
}
