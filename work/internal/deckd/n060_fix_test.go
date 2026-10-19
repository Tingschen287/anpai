package deckd

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/viewstate"
)

func TestN060LaunchFailureCarriesCode(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	w := newAgentWorld(t)
	w.up.launchErr = errors.New("upstream refused")
	w.up.launchOut = &agentdeck.LaunchResult{Error: "unknown tool", Code: "INVALID"}
	code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "x", "t", "nf1", "/tmp/alpha", "", "claude", "claude-sonnet-5-5"))
	if code != 502 {
		t.Fatalf("启动失败应是 502：%d %s", code, b)
	}
	m := decode(t, b)
	if m["code"] != "err.deckd.serverrunspawn.35" {
		t.Fatalf("启动失败应带码：%v", m["code"])
	}
	args, _ := m["args"].([]any)
	if len(args) == 0 {
		t.Fatalf("启动失败应带参数：%s", b)
	}
	i18n.SetLang(i18n.EN)
	en := deckclient.Text(m["code"].(string), args, m["error"].(string))
	if hasHan(en) || !strings.Contains(en, "launch failed") {
		t.Fatalf("英文启动失败不应是中文：%q", en)
	}
}

func TestN060SpawnNoteAndRoleAreCoded(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	w := newAgentWorld(t)
	w.up.sendErr = errors.New("send boom")
	code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "写测试", "t", "nf2", "/tmp/alpha", "", "claude", "claude-sonnet-5-5"))
	if code != 201 {
		t.Fatalf("送 brief 失败仍应 201：%d %s", code, b)
	}
	m := decode(t, b)
	noteCode, _ := m["note_code"].(string)
	if noteCode == "" {
		t.Fatalf("成功响应应带 note_code：%s", b)
	}
	noteArgs, _ := m["note_args"].([]any)
	note, _ := m["note"].(string)
	i18n.SetLang(i18n.EN)
	en := deckclient.Text(noteCode, noteArgs, note)
	if hasHan(en) {
		t.Fatalf("英文备注不应是中文：%q", en)
	}

	w.up.sendErr = nil
	code, b = w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "写测试", "t", "nf3", "/tmp/alpha", "", "claude", "claude-sonnet-5-5"))
	if code != 201 {
		t.Fatalf("第二次应开成：%d %s", code, b)
	}
	var role deckstore.Notification
	for _, n := range w.store.Notifications(0, "alpha") {
		if n.Code == "err.deckd.serverrunspawn.27" {
			role = n
		}
	}
	if role.Code == "" {
		t.Fatal("开成的通知应是带码的角色句")
	}
	roleEN := deckclient.Text(role.Code, []any(role.Args), role.Text)
	if !strings.Contains(roleEN, "main agent") || !strings.Contains(roleEN, "sub-agent") || strings.Contains(roleEN, "主 agent") || strings.Contains(roleEN, "子 agent") {
		t.Fatalf("角色名应是码，不应把中文压进英文：%q", roleEN)
	}
}

func TestN060SystemDecisionsStoreCodes(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	w := newAgentWorld(t)
	code, b := w.do("main", "POST", "/v1/agents", spawnJSON("frontier", "hard-task", "t", "nf4", "/tmp/alpha", "", "claude", "claude-opus-5-5"))
	if code != 202 {
		t.Fatalf("超包络应生成决定：%d %s", code, b)
	}
	dec := decode(t, b)["decision"].(map[string]any)
	if dec["summary_code"] == "" || dec["summary_code"] == nil {
		t.Fatalf("cost 决定应存码：%v", dec["summary_code"])
	}
	id := dec["id"].(string)
	stored, ok := w.store.Decision(id)
	if !ok || stored.SummaryCode == "" {
		t.Fatal("存储里的 cost 决定没有码")
	}
	raw, _ := json.Marshal(stored)
	var again deckstore.Decision
	if err := json.Unmarshal(raw, &again); err != nil {
		t.Fatal(err)
	}
	i18n.SetLang(i18n.EN)
	en := deckclient.Text(again.SummaryCode, []any(again.SummaryArgs), again.Summary)
	if hasHan(en) {
		t.Fatalf("英文决策摘要还有中文：%q", en)
	}

	code, b = w.do("main", "POST", "/v1/agents", spawnJSON("economy", "codex-task", "t", "nf5", "/tmp/alpha", "", "codex", "gpt-5.6-luna"))
	if code != 202 {
		t.Fatalf("应问 Codex trust：%d %s", code, b)
	}
	trust := decode(t, b)["decision"].(map[string]any)
	if trust["summary_code"] != "err.deckd.serverrunspawn.2" {
		t.Fatalf("信任决定应存码：%v", trust["summary_code"])
	}
	got, _ := w.store.Decision(trust["id"].(string))
	trustEN := deckclient.Text(got.SummaryCode, []any(got.SummaryArgs), got.Summary)
	if hasHan(trustEN) || !strings.Contains(trustEN, "rewrite") {
		t.Fatalf("英文信任摘要不对：%q", trustEN)
	}
}

func TestN060ViewNotOpenIsCoded(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	w := newAgentWorld(t)
	var notes []string
	c := &typingCenter{ro: true, shown: viewstate.Idle}
	lookup := func(id string) (viewstate.Target, bool) {
		if id == "s1" {
			return viewstate.Target{Session: "s1", Tmux: "tmux-s1", Role: "sub", Title: "s1"}, true
		}
		return viewstate.Target{}, false
	}
	m := viewstate.New(c, w.leases, lookup, func(n *i18n.Error) error {
		if n != nil {
			notes = append(notes, n.Error())
		}
		return nil
	}, nil)
	w.srv.view = m
	code, b := w.do("owner", "POST", "/v1/view", `{"op":"focus","session":"s1"}`)
	if code != 409 {
		t.Fatalf("未打开应是 409：%d %s", code, b)
	}
	mbody := decode(t, b)
	if mbody["code"] != "err.viewstate.notopen" {
		t.Fatalf("view 错误应带码：%s", b)
	}
	if strings.Contains(b, "会话不在打开集合里") && mbody["code"] == "" {
		t.Fatal("只有中文、没有码")
	}
	i18n.SetLang(i18n.EN)
	en := deckclient.Text(mbody["code"].(string), nil, mbody["error"].(string))
	if strings.Contains(en, "会话不在打开集合里") || hasHan(en) {
		t.Fatalf("英文 view 错误仍是中文：%q", en)
	}

	if _, err := w.store.NotifyCoded("alpha", "view", viewstate.ErrNotOpen.I18n(), "deckd"); err != nil {
		t.Fatal(err)
	}
	for _, n := range w.store.Notifications(0, "alpha") {
		if n.Code != "err.viewstate.notopen" {
			continue
		}
		shown := deckclient.Text(n.Code, []any(n.Args), n.Text)
		if strings.Contains(shown, "会话不在打开集合里") {
			t.Fatalf("带码通知的英文仍是中文：%q", shown)
		}
	}
}

func TestN060BusySubTextKeepsChineseAndCode(t *testing.T) {
	if i18n.E("err.deckd.busysubtext").Error() != busySubText {
		t.Fatal("忙时说明的中文必须和留给模型的常量逐字一样")
	}
	w := newAgentWorld(t)
	w.srv.activity = &activityCache{
		at:    time.Now(),
		byTmx: map[string]string{w.sub.TmuxSession: "running"},
		now:   time.Now,
	}
	code, b := w.do("owner", "POST", "/v1/agents/"+w.sub.Key+"/send", `{"text":"再做一件"}`)
	if code != 409 || !strings.Contains(b, busySubText) {
		t.Fatalf("owner 看到的忙时说明应仍是原文：%d %s", code, b)
	}
	m := decode(t, b)
	if m["code"] != "err.deckd.busysubtext" {
		t.Fatalf("同一响应应附码：%s", b)
	}
	if m["error"] != busySubText {
		t.Fatalf("error 字段应仍是旧中文：%v", m["error"])
	}

	w.srv.activity = nil
	w.up.subBusy = true
	w.leases.Grant(w.sub.AgentDeckID, "测试")
	code, b = w.do("owner", "POST", "/v1/agents/"+w.sub.Key+"/send", `{"text":"再做一件"}`)
	if code != 409 {
		t.Fatalf("上游忙也应拒绝：%d %s", code, b)
	}
	m = decode(t, b)
	if m["code"] != "err.deckd.busysubtext" || m["error"] != busySubText {
		t.Fatalf("上游忙的响应应同时有原文和码：%s", b)
	}
}

func TestN060ReportNoteSuccessReloads(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	w := newAgentWorld(t)
	code, b := w.do("sub", "POST", "/v1/reports", `{"done":false,"text":"hello"}`)
	if code != 200 {
		t.Fatalf("未完成汇报应记成通知：%d %s", code, b)
	}
	note := decode(t, b)["notification"].(map[string]any)
	if note["code"] != "err.deckd.serverpostreport.3" || note["text"] != "子 agent「sub」：hello" {
		t.Fatalf("成功响应的通知不对：%v", note)
	}
	var got deckstore.Notification
	for _, n := range w.store.Notifications(0, "alpha") {
		if n.Code == "err.deckd.serverpostreport.3" && strings.Contains(n.Text, "hello") {
			got = n
		}
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var again deckstore.Notification
	if err := json.Unmarshal(raw, &again); err != nil {
		t.Fatal(err)
	}
	if again.Text != got.Text || again.Code != got.Code {
		t.Fatalf("重读不一致：%+v", again)
	}
	i18n.SetLang(i18n.EN)
	en := deckclient.Text(again.Code, []any(again.Args), again.Text)
	if en != `sub-agent "sub": hello` {
		t.Fatalf("英文整句不对：%q", en)
	}
}

func TestN060StartupBannerNestsDialog(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	x := deckstore.Session{Role: "main", Title: "Main"}
	note := dialogCoded(x, i18n.E("err.deckd.startup.trust"), false)
	banner := i18n.E("err.deckd.startup.banner", x.Title, note)
	if !strings.Contains(banner.Error(), "目录信任确认") {
		t.Fatalf("中文启动说明变了：%q", banner.Error())
	}
	code, args, ok := i18n.Wire(banner)
	if !ok {
		t.Fatal("启动说明应有码")
	}
	i18n.SetLang(i18n.EN)
	en := deckclient.Text(code, args, banner.Error())
	if hasHan(en) || !strings.Contains(en, "directory trust prompt") {
		t.Fatalf("英文启动说明不应夹中文标签：%q", en)
	}
}

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF) {
			return true
		}
	}
	return false
}
