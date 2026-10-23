package deckd

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 段 3 返工 2。

// M2：PUT /v1/settings 保存了 ui_lang 之后，这次响应自己的 X-Deck-Lang 就是新语言（以前在请求进来时取，是旧语言）。
func TestN060S3Fix2PutSettingsResponseHasNewLanguage(t *testing.T) {
	f := newFixture(t)
	set := f.store.Settings()
	body, _ := json.Marshal(map[string]any{"agents": set.Agents, "projects": set.Projects, "generation": set.Generation, "ui_lang": "en"})
	req, _ := http.NewRequest("PUT", f.http.URL+"/v1/settings", strings.NewReader(string(body)))
	req.Header.Set("X-Test-As", "owner")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	if got := resp.Header.Get(i18n.LangHeader); got != "en" || f.store.UILang() != "en" {
		t.Fatalf("保存后的响应应带新语言：头=%q 已存=%q", got, f.store.UILang())
	}
}

// 语言头在响应头真正写出去的前一刻才取：处理器中途改了语言，写体、只写状态码、什么都没写，三种都带新语言。
func TestN060S3Fix2LangHeaderIsStampedWhenTheResponseIsWritten(t *testing.T) {
	cur := "zh"
	for name, h := range map[string]http.HandlerFunc{
		"write":       func(w http.ResponseWriter, r *http.Request) { cur = "en"; w.Write([]byte("x")) },
		"writeheader": func(w http.ResponseWriter, r *http.Request) { cur = "en"; w.WriteHeader(204) },
		"empty":       func(w http.ResponseWriter, r *http.Request) { cur = "en" },
		"flush": func(w http.ResponseWriter, r *http.Request) {
			cur = "en"
			w.(http.Flusher).Flush()
		},
	} {
		cur = "zh"
		rec := httptest.NewRecorder()
		markFilter{next: h, lang: func() string { return cur }}.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
		if got := rec.Header().Get(i18n.LangHeader); got != "en" {
			t.Errorf("%s：语言头应是写出去那一刻的 en：%q", name, got)
		}
	}
}

// M4：错误响应的出口。不带码的错误、带码错误里的外部错误参数，文字里的标记字符不能进响应，也不能变成假打码。
func TestN060S3Fix2ErrorResponsesNeverForgeRedaction(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	forged := "decode: " + i18n.RedactMark + " tail"
	for _, err := range []error{
		i18n.E("err.deckclient.status", 502, errors.New(forged)),
		i18n.E("err.deckclient.status", 502, struct{ S string }{forged}),
	} {
		rec := httptest.NewRecorder()
		writeErr(rec, http.StatusBadRequest, err)
		body := rec.Body.String()
		if i18n.HasMark(body) || strings.Contains(body, "[已打码]") {
			t.Errorf("writeErr 把伪造的标记放进了响应：%s", body)
		}
		var e apiError
		if jerr := json.Unmarshal([]byte(body), &e); jerr != nil {
			t.Fatal(jerr)
		}
		i18n.SetLang(i18n.EN)
		if shown := deckclient.Text(e.Code, e.Args, e.Error); strings.Contains(shown, "[redacted]") || i18n.HasMark(shown) {
			t.Errorf("界面显示伪造了打码：%q", shown)
		}
		i18n.SetLang(i18n.ZH)
		m := errFields(err, nil)
		raw, _ := json.Marshal(m)
		if i18n.HasMark(string(raw)) {
			t.Errorf("errFields 把伪造的标记放进了响应：%s", raw)
		}
	}
}

// M5：主 agent 经 POST /v1/decisions 提交的 cost 决定，摘要是它写的外部原文——即使恰好和系统句子一字不差，
// 初次返回、重启重读之后都不补码、不当系统句子。
func TestN060S3Fix2ExternalCostSummaryStaysExternalOverHTTP(t *testing.T) {
	f := newFixture(t)
	for _, text := range []string{"会话不在打开集合里", "外部原文需要一些额度"} {
		b, _ := json.Marshal(map[string]any{"kind": "cost", "summary": text})
		code, body := f.do("main", "POST", "/v1/decisions", string(b))
		if code != 201 {
			t.Fatal(code, body)
		}
		var d deckstore.Decision
		if err := json.Unmarshal([]byte(body), &d); err != nil {
			t.Fatal(err)
		}
		if d.SummaryCode != "" {
			t.Fatalf("初次返回就不该有码、不该算系统摘要：%+v", d)
		}
		re, err := deckstore.Open(f.dirs)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := re.Decision(d.ID)
		if got.SummaryCode != "" || got.Summary != text {
			t.Errorf("重启重读之后被当成系统摘要补了码：%+v", got)
		}
	}
}
