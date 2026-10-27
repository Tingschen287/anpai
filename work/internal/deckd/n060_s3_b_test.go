package deckd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 段 3 必改 2（第 2 段复审 4）：无效选项经报错回显。
// 回答决定的 option 是界面回传的选项原文，存下来的选项可能带真标记，所以只拿来和存下来的选项比较、不在入口中和；
// 比较失败时它进了错误参数——伪造的标记就成了界面上的假打码。比较失败后、进错误参数之前要先中和。

// Codex 复审 4 的反例，名字沿用。
func TestAuditN060R5InvalidAnswerForgedMarker(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	f := newFixture(t)
	code, body := f.do("main", "POST", "/v1/decisions", `{"kind":"push","summary":"external"}`)
	if code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	var d deckstore.Decision
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"option": "external " + i18n.RedactMark + " tail", "version": d.Version})
	code, body = f.doHdr("owner", "POST", "/v1/decisions/"+d.ID+"/answer", string(b), map[string]string{i18n.MarkHeader: i18n.MarkNeutral})
	if code != 400 {
		t.Fatalf("%d %s", code, body)
	}
	var e apiError
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatal(err)
	}
	i18n.SetLang(i18n.EN)
	shown := deckclient.Text(e.Code, e.Args, e.Error)
	if strings.Contains(shown, "[redacted]") || strings.Contains(body, "[已打码]") {
		t.Errorf("伪造的标记经无效选项的报错变成了假打码：%s -> %q", body, shown)
	}
	if i18n.HasMark(body) {
		t.Errorf("响应里还有标记：%s", body)
	}
}

// 合法的带真标记的选项仍要能原样比对成功（界面把存下来的选项原样回传）。
func TestAuditN060R5GenuineOptionRoundTrip(t *testing.T) {
	f := newFixture(t)
	code, body := f.doHdr("main", "POST", "/v1/decisions", `{"kind":"disagree","summary":"external","options":["token=FAKEsecret99"]}`, map[string]string{i18n.MarkHeader: i18n.MarkNeutral})
	if code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	var d deckstore.Decision
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatal(err)
	}
	opt := d.Options[0]
	if !i18n.HasMark(opt) {
		t.Fatalf("选项里没有真标记：%q", opt)
	}
	b, _ := json.Marshal(map[string]any{"option": opt, "version": d.Version})
	code, body = f.doHdr("owner", "POST", "/v1/decisions/"+d.ID+"/answer", string(b), map[string]string{i18n.MarkHeader: i18n.MarkNeutral})
	if code != 200 {
		t.Fatalf("真标记的选项应能原样回答：%d %s", code, body)
	}
}

// 各种写法的无效选项：字面标记、JSON 转义写法、被截断的残缺标记、带了中文记号的、只含部分标记的；
// 不带和带 X-Deck-Marks 头的客户端，错误响应里都不能有标记，也不能出现任何语言的打码记号。
func TestN060S3InvalidAnswerEveryShape(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	f := newFixture(t)
	code, body := f.do("main", "POST", "/v1/decisions", `{"kind":"push","summary":"external"}`)
	if code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	var d deckstore.Decision
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatal(err)
	}
	m := i18n.RedactMark
	options := map[string]string{
		"literal":   `"external ` + m + ` tail"`,
		"escaped":   `"external \ue000\ue001\ue002\ue003\ue004 tail"`,
		"partial":   `"a\ue000\ue001b"`,
		"single":    `"\ue004"`,
		"mixed":     `"token=FAKEsecret99 ` + m + `"`,
		"plainmiss": `"nothing special"`,
	}
	for name, opt := range options {
		for _, hdr := range []map[string]string{nil, {i18n.MarkHeader: i18n.MarkNeutral}} {
			doc := `{"option":` + opt + `,"version":` + itoa(d.Version) + `}`
			code, resp := f.doHdr("owner", "POST", "/v1/decisions/"+d.ID+"/answer", doc, hdr)
			if code != 400 {
				t.Fatalf("%s：%d %s", name, code, resp)
			}
			if i18n.HasMark(resp) || strings.Contains(resp, "[已打码]") || strings.Contains(resp, "[redacted]") {
				t.Errorf("%s（头 %v）：无效选项的错误响应里有标记或打码记号：%s", name, hdr, resp)
			}
		}
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// 其他把原样请求值放进错误参数或通知的出口：除了回答决定，没有别的请求用 readBodyRaw 取原样值。
// 开子 agent 的原样值只用来核对指纹；指纹对不上的错误里不带任何请求值。
func TestN060S3SpawnRawValuesNeverReachErrors(t *testing.T) {
	w := newAgentWorld(t)
	m := i18n.RedactMark
	good := spawnJSON("balanced", "title"+m, "task"+m, "req-bad-fp", "/tmp/alpha", "", "claude", "claude-sonnet-5-5")
	// 指纹对不上：把任务改掉，其余不动
	var sb spawnBody
	if err := json.Unmarshal([]byte(good), &sb); err != nil {
		t.Fatal(err)
	}
	sb.Task = "other" + m
	bad, _ := json.Marshal(sb)
	code, resp := w.do("main", "POST", "/v1/agents", string(bad))
	if code != 400 {
		t.Fatalf("指纹对不上应拒绝：%d %s", code, resp)
	}
	if i18n.HasMark(resp) || strings.Contains(resp, "[已打码]") {
		t.Errorf("指纹错误的响应带了请求里的标记：%s", resp)
	}
}

// JSON 转义写法的伪造标记走请求体：和字面标记一样，整个状态目录里不许出现标记字节。
// 同一组请求把字面标记换成 \ue000…\ue004 的转义写法（指纹仍按字面原文算，所以请求照样被接受）。
func TestN060S3EscapedRequestBodiesNeverReachTheStateDir(t *testing.T) {
	w := newAgentWorld(t)
	spawnedSub(t, w)
	m := i18n.RedactMark
	esc := func(s string) string { return strings.ReplaceAll(s, m, `\ue000\ue001\ue002\ue003\ue004`) }
	rep := func(done bool, text string) string {
		return `{"done":` + map[bool]string{true: "true", false: "false"}[done] + `,"text":"` + text + `"}`
	}
	if code, b := w.do("sub", "POST", "/v1/reports", rep(false, `r \ue000\ue001\ue002\ue003\ue004`)); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if code, b := w.do("sub", "POST", "/v1/reports", rep(true, `d \ue000\ue001\ue002\ue003\ue004`)); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if code, b := w.do("main", "POST", "/v1/agents", esc(spawnJSON("balanced", "title"+m, "task"+m, "req-forged-esc", "/tmp/alpha", "", "claude", "claude-sonnet-5-5"))); code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	w.leases.Grant(w.sub.AgentDeckID, "review")
	if code, b := w.do("owner", "POST", "/v1/agents/"+w.sub.Key+"/send", `{"text":"next \ue000\ue001\ue002\ue003\ue004"}`); code != 202 && code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if code, b := w.do("main", "POST", "/v1/decisions", `{"kind":"push","summary":"推送 \ue000\ue001\ue002\ue003\ue004","options":["o\ue000"]}`); code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	w.srv.WaitBackground()
	var files int
	filepath.Walk(w.dirs.State, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		files++
		data, _ := os.ReadFile(p)
		if strings.ContainsAny(string(data), m) {
			t.Errorf("%s 里还有伪造的标记", p)
		}
		return nil
	})
	if files == 0 {
		t.Fatal("状态目录是空的，测试没有测到东西")
	}
}
