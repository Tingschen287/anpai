package panels

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

func TestN060RenderNotificationsClipsAndHidesSecrets(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	root := t.TempDir()
	d := deckstore.Dirs{Config: filepath.Join(root, "cfg"), State: filepath.Join(root, "state")}
	s, err := deckstore.Open(d)
	if err != nil {
		t.Fatal(err)
	}
	token := "sk-FAKETOKEN99"
	if _, err := s.NotifyCoded("p", "report", i18n.E("err.deckd.serverrunspawn.47", token), "deckd"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NotifyCoded("p", "report", i18n.E("err.deckd.serverrunspawn.47", "\x1b[31mRED\x1b[0m"), "deckd"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NotifyCoded("p", "report", i18n.E("err.deckd.serverrunspawn.47", strings.Repeat("B", 700)), "deckd"); err != nil {
		t.Fatal(err)
	}
	s2, err := deckstore.Open(d)
	if err != nil {
		t.Fatal(err)
	}
	ns := s2.Notifications(0, "")
	if len(ns) != 3 {
		t.Fatalf("重读应有 3 条，实际 %d", len(ns))
	}
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		panel := RenderNotifications(ns, 10)
		if strings.Contains(panel, token) || strings.Contains(panel, strings.Repeat("B", 700)) {
			t.Fatalf("%s 面板仍有原文", lang)
		}
		if !strings.Contains(panel, "RED") {
			t.Fatalf("%s 面板应留下去掉控制符后的字母", lang)
		}
		mark := "[已打码]"
		if lang == i18n.EN {
			mark = "[redacted]"
		}
		if !strings.Contains(panel, mark) {
			t.Fatalf("%s 面板应显示 %s", lang, mark)
		}
	}
	// 直接量生产面板输出里那一行的长度：整句不能超过 600 字（加一个「…」）。
	// 不在测试里自己调 Clip，生产里的截断被删掉，这里会失败。
	prefix := len([]rune("15:04 p · "))
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		panel := RenderNotifications(ns, 10)
		long := ""
		for _, line := range strings.Split(ansi.Strip(panel), "\n") {
			if strings.Contains(line, "BBBBBBBB") {
				long = line
			}
		}
		if long == "" {
			t.Fatalf("%s 面板里找不到超长通知那一行", lang)
		}
		if n := utf8.RuneCountInString(long) - prefix; n > 601 || !strings.HasSuffix(long, "…") {
			t.Fatalf("%s 面板里的超长通知有 %d 字，应截到 600 字加「…」：%q", lang, n, long[len(long)-20:])
		}
	}
}

func TestN060DecisionPanelEnglishHasNoHan(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	root := t.TempDir()
	d := deckstore.Dirs{Config: filepath.Join(root, "cfg"), State: filepath.Join(root, "state")}
	s, err := deckstore.Open(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDecision(deckstore.NewDecision{
		Kind:       deckstore.KindCodexTrust,
		SummaryErr: i18n.E("err.deckd.serverrunspawn.2", "Codex", "/tmp/alpha"),
		CreatedBy:  "owner",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDecision(deckstore.NewDecision{
		Kind:       deckstore.KindCost,
		SummaryErr: i18n.E("err.deckd.serverrunspawn.7", "claude", "frontier", "claude-opus-5-5", "title"),
		CreatedBy:  "owner",
	}); err != nil {
		t.Fatal(err)
	}
	s2, err := deckstore.Open(d)
	if err != nil {
		t.Fatal(err)
	}
	ds := s2.Decisions("")
	if len(ds) != 2 || ds[0].SummaryCode == "" || ds[1].SummaryCode == "" {
		t.Fatalf("决定应带码：%+v", ds)
	}
	i18n.SetLang(i18n.EN)
	panel := RenderDecisions(ds, 0, "")
	if hasHan(panel) {
		t.Fatalf("英文决策面板还有中文：%q", panel)
	}
	if !strings.Contains(panel, "starting") && !strings.Contains(panel, "rewrite") {
		t.Fatalf("英文决策面板应有译文：%q", panel)
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
