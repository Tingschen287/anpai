package deckui

import (
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/panels"
)

// 返工 4 第 4 条：63f6247 建的主会话（role=main，title="主 agent · p"，没有 title_code）在升级后，
// 经真实的 Store.Open 补码，再进真实的 Sidebar、Tabs、活动面板：英文没有中文，中文逐字不变，
// owner 自己改过的标题原样显示。

func hasHanRune(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// legacyStore 返回一个重读过的存储。titles 的每一项是一条旧形状的主会话（没有标题码）。
func legacyStore(t *testing.T, titles map[string]string) *deckstore.Store {
	t.Helper()
	root := t.TempDir()
	dirs := deckstore.Dirs{Config: root + "/cfg", State: root + "/state"}
	st, err := deckstore.Open(dirs)
	if err != nil {
		t.Fatal(err)
	}
	set := st.Settings()
	set.Projects = append(set.Projects, deckstore.ProjectDef{ID: "p", Name: "p", Path: "/tmp/p",
		Agents: []string{"claude"}, Models: map[deckstore.Tier]deckstore.ModelList{deckstore.Frontier: {"claude-opus-5-5"}},
		MainAgent: deckstore.MainAgent{Agent: "claude", Tier: deckstore.Frontier}, Envelope: deckstore.DefaultEnvelope("p")})
	if err := st.ReplaceSettings(set); err != nil {
		t.Fatal(err)
	}
	for key, title := range titles {
		r, err := st.Reserve(deckstore.Session{Project: "p", Role: "main", Agent: "claude", Tool: "claude", Model: "m", Tier: deckstore.Frontier,
			Title: title, CreatedBy: "owner", RequestID: "req-" + key, Dir: "/tmp/p",
			TaskSHA256: strings.Repeat("a", 64), ArgsSHA256: strings.Repeat("b", 64)}, "")
		if err != nil {
			t.Fatal(err)
		}
		st.LaunchSucceeded(r.Key, "ad-"+key, "tmux-"+key)
	}
	// 重读：这就是升级后 deckd 启动时做的事
	st2, err := deckstore.Open(dirs)
	if err != nil {
		t.Fatal(err)
	}
	return st2
}

func renderLegacy(t *testing.T, lang string, titles map[string]string) (side, tabs, act string) {
	t.Helper()
	i18n.SetLang(lang)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	st := legacyStore(t, titles)
	var infos []SessionInfo
	var open []string
	for _, x := range st.Sessions("") {
		infos = append(infos, SessionInfo{Session: x})
		open = append(open, x.AgentDeckID)
	}
	d := Data{Projects: []deckstore.ProjectDef{{ID: "p", Name: "p"}}, Sessions: infos}
	d.View.Open = open
	side = strings.Join((&Sidebar{Data: d, Mode: "none"}).Render(60, 30).Lines, "\n")
	tabs = strings.Join((&Tabs{Data: d}).Render(120).Lines, "\n")
	act = panels.RenderActivity(st.Sessions(""), time.Now())
	return side, tabs, act
}

// Codex 复审 3 的反例（TestAuditN060R4LegacyMainTitle）：旧形状进真实界面，英文不含中文。
func TestN060R4LegacyMainTitleIsCodedAfterOpen(t *testing.T) {
	side, tabs, act := renderLegacy(t, i18n.EN, map[string]string{"a": "主 agent · p"})
	for name, out := range map[string]string{"侧栏": side, "标签条": tabs, "活动面板": act} {
		if hasHanRune(out) {
			t.Errorf("英文%s里还有中文：%q", name, out)
		}
		if !strings.Contains(out, "Main agent") && !strings.Contains(out, "main agent") {
			t.Errorf("英文%s里没有译出来的主 agent 标题：%q", name, out)
		}
	}
}

func TestN060R4LegacyMainTitleChineseIsUnchanged(t *testing.T) {
	side, tabs, act := renderLegacy(t, i18n.ZH, map[string]string{"a": "主 agent · p"})
	for name, out := range map[string]string{"侧栏": side, "标签条": tabs, "活动面板": act} {
		if !strings.Contains(out, "主 agent · p") {
			t.Errorf("中文%s里的标题变了：%q", name, out)
		}
	}
}

// 只认系统生成的确切形状：owner 自己改过的标题、别的项目名拼出来的标题，原样显示。
func TestN060R4LegacyTitleOnlyExactShapeIsCoded(t *testing.T) {
	for _, title := range []string{"主 agent · 别的项目", "我的主会话", "主 agent · p 改过", "主 agent·p"} {
		side, tabs, act := renderLegacy(t, i18n.EN, map[string]string{"a": title})
		for name, out := range map[string]string{"侧栏": side, "标签条": tabs, "活动面板": act} {
			if !hasHanRune(out) {
				t.Errorf("标题 %q 在英文%s里被误译了：%q", title, name, out)
			}
		}
	}
}
