package panels

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 段 3 返工 2。

// shortSockServer 起一个只带语言头的假 deckd（短路径，macOS 的 unix socket 路径上限 104 字节）。
// 语言头取自 lang；GET 回 []，其余回 {}；hits 记收到的请求数。
func shortSockServer(t *testing.T, lang *atomic.Value, hits *atomic.Int32) *deckclient.Client {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "n6")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "p.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		w.Header().Set(i18n.LangHeader, lang.Load().(string))
		if r.Method == "GET" {
			w.Write([]byte(`[]`))
			return
		}
		w.Write([]byte(`{}`))
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return deckclient.NewUI(sock)
}

// M1：决策面板回答之后的提示（已回答 / 回答失败）存的是怎么画，不是画好的字；
// 提示还在屏幕上时语言变了（下一次真实取数收到英文头），下一帧就是英文。
func TestN060S3Fix2DecisionAnswerNoteFollowsLanguageSwitch(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	var lang atomic.Value
	lang.Store("en")
	c := shortSockServer(t, &lang, nil)
	m := &model{name: "decisions", decs: []deckstore.Decision{{ID: "dec-1", State: deckstore.StateOpen, Options: []string{"approve"}, Version: 1,
		ExpiresAt: time.Now().Add(time.Hour)}}}
	m.c = c // 回答的请求不跟语言（语言头只让下面带 FollowLang 的取数采用）
	m.Update(m.answer(0)())
	if v := m.View(); !strings.Contains(v, "已回答") {
		t.Fatalf("前提：中文下应显示已回答：%q", v)
	}
	m.c = c.FollowLang()
	m.Update(m.fetch()()) // 下一次真实请求：响应头是英文
	if i18n.Lang() != i18n.EN {
		t.Fatalf("前提：取数之后进程应是英文：%s", i18n.Lang())
	}
	v := m.View()
	if hasHan(v) {
		t.Errorf("提示留着中文，没有跟语言：%q", v)
	}
	if !strings.Contains(v, "Answered") {
		t.Errorf("英文下应显示已回答的英文提示：%q", v)
	}
	lang.Store("zh")
	m.Update(m.fetch()())
	if v := m.View(); !strings.Contains(v, "已回答") {
		t.Errorf("切回中文后应是中文提示：%q", v)
	}
}

// M1（失败那条）：回答失败的提示带着错误，每帧按当前语言翻译。
func TestN060S3Fix2DecisionAnswerErrorNoteFollowsLanguageSwitch(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	dir, err := os.MkdirTemp("/tmp", "n6")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "p.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(i18n.LangHeader, "en")
		if r.Method == "POST" {
			w.WriteHeader(409)
			w.Write([]byte(`{"error":"会话不在打开集合里","code":"err.viewstate.notopen"}`))
			return
		}
		w.Write([]byte(`[]`))
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	c := deckclient.NewUI(sock)
	m := &model{name: "decisions", c: c, decs: []deckstore.Decision{{ID: "dec-1", State: deckstore.StateOpen, Options: []string{"approve"}, Version: 1,
		ExpiresAt: time.Now().Add(time.Hour)}}}
	m.Update(m.answer(0)())
	if v := m.View(); !strings.Contains(v, "会话不在打开集合里") {
		t.Fatalf("前提：中文下应显示回答失败的原因：%q", v)
	}
	m.c = c.FollowLang()
	m.Update(m.fetch()())
	if v := m.View(); hasHan(v) {
		t.Errorf("回答失败的提示留着中文：%q", v)
	}
}

// M3：没指定项目的进度面板，提示照旧，但每一轮仍向 deckd 对一次语言，提示按当前语言画。
func TestN060S3Fix2ProgressWithoutProjectFollowsLanguage(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	var lang atomic.Value
	lang.Store("zh")
	var hits atomic.Int32
	m := &model{name: "progress", c: shortSockServer(t, &lang, &hits).FollowLang()}
	m.Update(m.fetch()())
	if v := m.View(); !strings.Contains(v, "--project") || !hasHan(v) {
		t.Fatalf("中文下照旧显示要指定项目的提示：%q", v)
	}
	if hits.Load() != 1 {
		t.Fatalf("每一轮都要向 deckd 对一次语言：请求数 %d", hits.Load())
	}
	lang.Store("en") // 别处换了语言
	m.Update(m.fetch()())
	m.Update(m.fetch()())
	if hits.Load() != 3 {
		t.Fatalf("每一轮都要对：请求数 %d", hits.Load())
	}
	v := m.View()
	if i18n.Lang() != i18n.EN || hasHan(v) || !strings.Contains(v, "--project") {
		t.Errorf("没指定项目的进度面板没有跟上语言：lang=%s %q", i18n.Lang(), v)
	}
}

// M3（同类分支）：面板名不认识时也是本地直接返回，同样要对语言。
func TestN060S3Fix2UnknownPanelBranchAlsoSyncsLanguage(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	var lang atomic.Value
	lang.Store("en")
	var hits atomic.Int32
	m := &model{name: "nonesuch", c: shortSockServer(t, &lang, &hits).FollowLang()}
	m.Update(m.fetch()())
	if hits.Load() != 1 || i18n.Lang() != i18n.EN {
		t.Errorf("本地返回的分支也要对语言：请求数 %d，语言 %s", hits.Load(), i18n.Lang())
	}
}

// M5：主 agent 提交的 cost 决定（没有码，也没有系统范围）不是系统摘要：英文界面原样显示、不加「旧记录」标签；
// 真正的旧系统 cost（带 SpawnScope）混有外部原文时才加。
func TestN060S3Fix2ExternalCostSummaryGetsNoEarlierRecordLabel(t *testing.T) {
	i18n.SetLang(i18n.EN)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	now := time.Now()
	mk := func(id, text string, scope map[string]string) deckstore.Decision {
		return deckstore.Decision{ID: id, Project: "alpha", Kind: deckstore.KindCost, Summary: text, Scope: scope, Options: []string{"approve", "reject", "stop"},
			Version: 1, State: deckstore.StateOpen, CreatedBy: "main-session", CreatedAt: now, ExpiresAt: now.Add(time.Hour), UpdatedAt: now}
	}
	ext := mk("dec-1", "外部原文需要一些额度", nil)
	sys := mk("dec-2", "额度未知或已用尽时启动 frontier：请求开 Claude Code（frontier · m）「写测试」", map[string]string{"action": "spawn"})
	v := RenderDecisions([]deckstore.Decision{ext, sys}, 0, "")
	if !strings.Contains(v, ext.Summary) || strings.Contains(v, "[Earlier record] "+ext.Summary) {
		t.Errorf("主 agent 提交的 cost 摘要应原样显示、不加标签：%q", v)
	}
	if !strings.Contains(v, "[Earlier record] "+sys.Summary) {
		t.Errorf("真正的旧系统 cost 摘要混有外部原文，应加标签：%q", v)
	}
}
