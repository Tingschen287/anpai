package deckd

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 返工 4 第 1 条：伪造的打码标记在原始输入边界上换掉，真打码照旧显示。

func lastNote(t *testing.T, f *fixture) deckstore.Notification {
	t.Helper()
	ns := f.store.Notifications(0, "alpha")
	if len(ns) == 0 {
		t.Fatal("没有通知")
	}
	return ns[len(ns)-1]
}

func showNote(n deckstore.Notification, lang string) string {
	i18n.SetLang(lang)
	defer i18n.SetLang(i18n.ZH)
	return deckclient.Text(n.Code, []any(n.Args), n.Text)
}

func reportJSON(t *testing.T, done bool, text string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"done": done, "text": text})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Codex 复审 3 的反例（TestAuditN060R4ForgedReportMarker）：走真实汇报 Handler，输入带 5 个私用区字符。
func TestN060R4ForgedReportMarker(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	f := newFixture(t)
	raw := "external " + i18n.RedactMark + " literal"
	if code, b := f.do("sub", "POST", "/v1/reports", reportJSON(t, false, raw)); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	n := lastNote(t, f)
	for _, a := range n.Args {
		if s, ok := a.(string); ok && i18n.HasMark(s) {
			t.Fatalf("存下来的通知参数里还有伪造的标记：%q", s)
		}
	}
	if i18n.HasMark(n.Text) {
		t.Fatalf("通知中文原文里还有标记：%q", n.Text)
	}
	if en := showNote(n, i18n.EN); strings.Contains(en, "[redacted]") || !strings.Contains(en, "external") {
		t.Fatalf("英文把伪造标记显示成了打码：%q", en)
	}
	if zh := showNote(n, i18n.ZH); strings.Contains(zh, "[已打码]") {
		t.Fatalf("中文把伪造标记显示成了打码：%q", zh)
	}
}

// 补充汇报（同一次完成再报一次、正文不同）走另一条参数路径，同样要中和。
func TestN060R4ForgedSupplementReportMarker(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	w := newAgentWorld(t)
	spawnedSub(t, w)
	if code, b := w.do("sub", "POST", "/v1/reports", reportJSON(t, true, "first")); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if code, b := w.do("sub", "POST", "/v1/reports", reportJSON(t, true, "second "+i18n.RedactMark+" tail")); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	n := lastNote(t, w.fixture)
	if n.Code != "err.deckd.serverpostreport.4" {
		t.Fatalf("最后一条不是补充汇报通知：%+v", n)
	}
	for _, a := range n.Args {
		if s, ok := a.(string); ok && i18n.HasMark(s) {
			t.Fatalf("补充汇报的参数里还有伪造的标记：%q", s)
		}
	}
	if en := showNote(n, i18n.EN); strings.Contains(en, "[redacted]") {
		t.Fatalf("英文把伪造标记显示成了打码：%q", en)
	}
}

// 真打码（假 token）仍然是标记，中文显示「[已打码]」，英文显示「[redacted]」。
func TestN060R4RealRedactionStillShown(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	f := newFixture(t)
	if code, b := f.do("sub", "POST", "/v1/reports", reportJSON(t, false, "step done token=FAKEsecret99 end")); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	n := lastNote(t, f)
	found := false
	for _, a := range n.Args {
		if s, ok := a.(string); ok && i18n.HasMark(s) {
			found = true
		}
	}
	if !found {
		t.Fatalf("真打码的标记丢了：%+v", n.Args)
	}
	if strings.Contains(showNote(n, i18n.ZH), "FAKEsecret99") || strings.Contains(showNote(n, i18n.EN), "FAKEsecret99") {
		t.Fatal("密钥没有被打码")
	}
	if zh := showNote(n, i18n.ZH); !strings.Contains(zh, "[已打码]") {
		t.Fatalf("中文应显示 [已打码]：%q", zh)
	}
	if en := showNote(n, i18n.EN); !strings.Contains(en, "[redacted]") {
		t.Fatalf("英文应显示 [redacted]：%q", en)
	}
}

// 请求体里每个会被存下来的外部字段：汇报、起子 agent 的标题与任务、发话、决定摘要。
// 请求全部带伪造标记，整个状态目录里都不许出现标记字节（没有真密钥，所以任何标记都是伪造）。
func TestN060R4ForgedMarkNeverReachesTheStateDir(t *testing.T) {
	w := newAgentWorld(t)
	spawnedSub(t, w)
	m := i18n.RedactMark
	if code, b := w.do("sub", "POST", "/v1/reports", reportJSON(t, false, "r "+m)); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if code, b := w.do("sub", "POST", "/v1/reports", reportJSON(t, true, "d "+m)); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "title"+m, "task"+m, "req-forged", "/tmp/alpha", "", "claude", "claude-sonnet-5-5")); code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	w.leases.Grant(w.sub.AgentDeckID, "review")
	if code, b := w.do("owner", "POST", "/v1/agents/"+w.sub.Key+"/send", `{"text":"next `+m+`"}`); code != 202 && code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	sum, _ := json.Marshal(map[string]any{"kind": "push", "summary": "推送 " + m})
	if code, b := w.do("main", "POST", "/v1/decisions", string(sum)); code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	w.srv.WaitBackground()
	dir := w.dirs.State
	var files int
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		files++
		data, _ := os.ReadFile(p)
		if strings.Contains(string(data), "") {
			t.Errorf("%s 里还有伪造的标记", p)
		}
		return nil
	})
	if files == 0 {
		t.Fatal("状态目录是空的，测试没有测到东西")
	}
}

// 路径、查询串里伪造的标记：不带 X-Deck-Marks 头的客户端本来会把标记换成「[已打码]」，
// 所以响应里出现它就说明标记活着进了响应。
func TestN060R4ForgedMarkInPathAndQueryIsNeutralized(t *testing.T) {
	f := newFixture(t)
	forged := "x" + i18n.RedactMark + "y"
	for _, p := range []string{
		"/v1/agents/" + url.PathEscape(forged) + "/output",
		"/v1/tools/models?tool=" + url.QueryEscape(forged),
		"/v1/progress?project=" + url.QueryEscape(forged),
	} {
		code, body := f.do("owner", "GET", p, "")
		if code == 0 || body == "" {
			t.Fatalf("%s 没有响应", p)
		}
		if strings.Contains(body, "[已打码]") || strings.Contains(body, "") {
			t.Errorf("%s 的响应里伪造的标记还活着：%s", p, body)
		}
	}
}

// 返工 4 第 4 条：补码用的码和新建主 agent 时写的码是同一个。
func TestN060R4LegacyMainTitleBackfillMatchesNewMain(t *testing.T) {
	w := newAgentWorld(t)
	addGamma(t, w)
	if code, b := w.do("owner", "POST", "/v1/projects/gamma/main", `{}`); code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	x := mainOf(t, w, "gamma")
	if x.TitleCode == "" {
		t.Fatal("新建的主 agent 没有标题码")
	}
	// 把注册表改回 63f6247 的形状（只有中文 title，没有码），再用 Open 重读
	dirs := w.dirs
	reg := filepath.Join(dirs.State, "registry.json")
	data, err := os.ReadFile(reg)
	if err != nil {
		t.Fatal(err)
	}
	var all []map[string]any
	if err := json.Unmarshal(data, &all); err != nil {
		t.Fatal(err)
	}
	for _, e := range all {
		if e["key"] == x.Key {
			delete(e, "title_code")
			delete(e, "title_args")
		}
	}
	out, _ := json.Marshal(all)
	if err := os.WriteFile(reg, out, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := deckstore.Open(dirs)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := st.Session(x.Key)
	if got.TitleCode != x.TitleCode || got.Title != x.Title {
		t.Fatalf("补出来的码和新建时的不一样：新建 %q/%v，补码 %q/%v，title %q", x.TitleCode, x.TitleArgs, got.TitleCode, got.TitleArgs, got.Title)
	}
}

// 返工 4 第 3 条：Close 取消并等待后台任务。主 agent 停在启动确认上时，后台等 owner 处理的那一路会一直轮询；
// Close 必须让它结束，而且返回之后后台不再写盘（夹具清理时 TempDir 才删得干净）。
func TestN060R4ServerCloseCancelsAndWaitsBackground(t *testing.T) {
	w := newAgentWorld(t)
	addGamma(t, w)
	w.up.setOutput(claudeTrustScreen)
	if code, b := w.do("owner", "POST", "/v1/projects/gamma/main", `{}`); code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	done := make(chan struct{})
	go func() { w.srv.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Close 没有在取消之后等到后台任务结束")
	}
	w.srv.WaitBackground() // 已经没有后台任务：立刻返回
	before := len(w.store.Notifications(0, "gamma"))
	w.up.setOutput(claudeComposerScreen) // 如果后台还活着，这时会把 brief 送出去并写通知
	if after := len(w.store.Notifications(0, "gamma")); after != before {
		t.Fatalf("Close 之后后台还在写通知：%d -> %d", before, after)
	}
}
