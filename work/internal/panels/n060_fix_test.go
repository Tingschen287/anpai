package panels

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
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
	}
	i18n.SetLang(i18n.EN)
	long := deckstore.Clip(deckclient.Text(ns[2].Code, []any(ns[2].Args), ns[2].Text), 600)
	if n := utf8.RuneCountInString(long); n > 601 {
		t.Fatalf("英文整句超长：%d", n)
	}
	if strings.Contains(long, "\x1b") {
		t.Fatal("渲染整句仍有 ESC")
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
