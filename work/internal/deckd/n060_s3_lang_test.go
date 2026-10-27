package deckd

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 段 3：界面语言。deckd 是唯一的真相：每个响应带 X-Deck-Lang，GET /v1/lang 回此刻的语言；
// 选择存在设置里（PUT /v1/settings 的 ui_lang）；没有 ui_lang 是升级的老用户，中文。全新安装在第一次创建 agents.yaml 时
// 把系统语言（deck up 传给 deckd 的 Config.SystemLang）写进 ui_lang（返工：owner 定，老用户中文、新装跟系统）。
// deckd 自己不换语言：给模型、落盘、日志的文字仍是中文。

func langOf(t *testing.T, f *fixture) (lang string, saved bool, hdr string) {
	t.Helper()
	req, _ := http.NewRequest("GET", f.http.URL+"/v1/lang", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out struct {
		Lang  string `json:"lang"`
		Saved bool   `json:"saved"`
	}
	if err := json.Unmarshal(b, &out); err != nil || resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	return out.Lang, out.Saved, resp.Header.Get(i18n.LangHeader)
}

func putS3Settings(t *testing.T, f *fixture, mutate func(body map[string]any)) (int, string) {
	t.Helper()
	set := f.store.Settings()
	body := map[string]any{"agents": set.Agents, "projects": set.Projects, "generation": set.Generation}
	if mutate != nil {
		mutate(body)
	}
	b, _ := json.Marshal(body)
	return f.do("owner", "PUT", "/v1/settings", string(b))
}

func TestN060S3LangEndpointHeaderAndSystemFallback(t *testing.T) {
	f := newFixture(t)
	// 没选过、没传系统语言：中文（和以前一样）
	if l, saved, h := langOf(t, f); l != "zh" || saved || h != "zh" {
		t.Fatalf("默认：%q saved=%v header=%q", l, saved, h)
	}
	// 返工：设置里没有 ui_lang（这份设置文件在这次启动前就有）就是升级的老用户，中文。
	// 原来这里断言「没选过就跟随系统语言：en」，改成「系统语言是 en 也仍是 zh」：系统语言只在全新安装创建文件时起作用
	f.srv.cfg.SystemLang = i18n.EN
	if l, saved, h := langOf(t, f); l != "zh" || saved || h != "zh" {
		t.Fatalf("老用户（没有 ui_lang）即使系统是英文也保持中文：%q saved=%v header=%q", l, saved, h)
	}
	// 系统语言不认识：同样不起作用
	f.srv.cfg.SystemLang = "fr"
	if l, _, _ := langOf(t, f); l != "zh" {
		t.Fatalf("不认识的系统语言：%q", l)
	}
	// 每个响应都带头：成功的、出错的、各种角色的
	for _, c := range []struct{ as, method, path string }{
		{"owner", "GET", "/v1/health"}, {"unknown", "GET", "/v1/sessions"}, {"owner", "GET", "/v1/no-such-route"}, {"sub", "GET", "/v1/decisions"},
	} {
		req, _ := http.NewRequest(c.method, f.http.URL+c.path, nil)
		req.Header.Set("X-Test-As", c.as)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if h := resp.Header.Get(i18n.LangHeader); h != "zh" {
			t.Errorf("%s %s（%d）的响应头 %q，应为 zh", c.method, c.path, resp.StatusCode, h)
		}
	}
}

func TestN060S3PutSettingsSavesLangAndItSticks(t *testing.T) {
	f := newFixture(t)
	f.srv.cfg.SystemLang = i18n.EN // 系统是英文，owner 选中文
	if code, b := putS3Settings(t, f, func(m map[string]any) { m["ui_lang"] = "zh" }); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if l, saved, h := langOf(t, f); l != "zh" || !saved || h != "zh" {
		t.Fatalf("选中文之后：%q saved=%v header=%q", l, saved, h)
	}
	// 落盘了：重新读文件就是中文
	disk, err := deckstore.LoadSettings(f.dirs.Config)
	if err != nil || disk.UILang != "zh" {
		t.Fatalf("设置文件里的语言：%q %v", disk.UILang, err)
	}
	// 不带 ui_lang 的保存（旧界面、deck project add）不动已存的语言
	if code, b := putS3Settings(t, f, nil); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if l, saved, _ := langOf(t, f); l != "zh" || !saved {
		t.Fatalf("不带 ui_lang 的保存把语言动了：%q saved=%v", l, saved)
	}
	// 改成英文
	if code, b := putS3Settings(t, f, func(m map[string]any) { m["ui_lang"] = "en" }); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if l, _, h := langOf(t, f); l != "en" || h != "en" {
		t.Fatalf("改成英文之后：%q %q", l, h)
	}
	// 保存的响应带着新语言，设置界面据此立刻换
	code, _ := putS3Settings(t, f, func(m map[string]any) { m["ui_lang"] = "en" })
	if code != 200 {
		t.Fatal(code)
	}
	// 清掉已存的选择（带 ""）：和没选过的老用户一样是中文（返工：原来是「回到系统语言」，断言从 en 改成 zh；系统是 en 也一样）
	if code, b := putS3Settings(t, f, func(m map[string]any) { m["ui_lang"] = "" }); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if l, saved, _ := langOf(t, f); l != "zh" || saved {
		t.Fatalf("清掉选择：%q saved=%v（系统是 en，仍应是中文）", l, saved)
	}
}

func TestN060S3PutSettingsRejectsUnknownLangAndStaleGeneration(t *testing.T) {
	f := newFixture(t)
	if code, b := putS3Settings(t, f, func(m map[string]any) { m["ui_lang"] = "fr" }); code != 400 || !strings.Contains(b, "fr") {
		t.Fatalf("不认识的语言应 400：%d %s", code, b)
	}
	if _, saved, _ := langOf(t, f); saved {
		t.Fatal("被拒的保存不该改语言")
	}
	// n057 代数保护：带旧代数的保存（别处改过）409，语言不变
	stale := f.store.Settings().Generation
	if code, b := putS3Settings(t, f, func(m map[string]any) { m["ui_lang"] = "en" }); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	code, b := putS3Settings(t, f, func(m map[string]any) { m["generation"] = stale; m["ui_lang"] = "zh" })
	if code != http.StatusConflict {
		t.Fatalf("旧代数应 409：%d %s", code, b)
	}
	if l, _, _ := langOf(t, f); l != "en" {
		t.Fatalf("409 的保存不该改语言：%q", l)
	}
	// 非 owner 不能改语言
	set := f.store.Settings()
	body, _ := json.Marshal(map[string]any{"agents": set.Agents, "projects": set.Projects, "generation": set.Generation, "ui_lang": "zh"})
	if code, _ := f.do("main", "PUT", "/v1/settings", string(body)); code != http.StatusForbidden {
		t.Fatalf("主 agent 不能改设置：%d", code)
	}
}

// 重启 deckd（重新 Open 同一份设置）语言保持；deckd 进程自己的语言不变（给模型、落盘、日志的文字仍是中文）。
func TestN060S3LangSurvivesDeckdRestartAndDeckdStaysChinese(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	i18n.SetLang(i18n.ZH) // 进程语言设成英文跑测试时（N060_TEST_LANG=en）也从中文起，才看得出选语言有没有动它
	f := newFixture(t)
	if code, b := putS3Settings(t, f, func(m map[string]any) { m["ui_lang"] = "en" }); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	st2, err := deckstore.Open(f.dirs)
	if err != nil {
		t.Fatal(err)
	}
	srv2 := NewServer(Config{Dirs: f.dirs}, st2, nil, nil, nil, nil)
	t.Cleanup(srv2.Close)
	if got := srv2.uiLang(); got != "en" {
		t.Fatalf("重启后：%q", got)
	}
	// 选了英文，deckd 进程没有 SetLang：模型读的、落盘的错误文字仍是中文原文
	if i18n.Lang() != i18n.ZH {
		t.Fatalf("deckd 进程的语言不该被界面语言改动：%s", i18n.Lang())
	}
	code, body := f.do("main", "POST", "/v1/decisions", `{"kind":"nope","summary":"x"}`)
	if code != 400 {
		t.Fatalf("应该被拒：%d %s", code, body)
	}
	var e struct {
		Error string `json:"error"`
	}
	json.Unmarshal([]byte(body), &e)
	if e.Error == "" || !hasHanString(e.Error) {
		t.Fatalf("英文界面语言下，响应里的 error 原文仍是中文：%q", e.Error)
	}
}

func hasHanString(s string) bool {
	for _, r := range s {
		if r >= 0x4e00 && r <= 0x9fff {
			return true
		}
	}
	return false
}
