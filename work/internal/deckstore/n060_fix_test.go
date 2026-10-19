package deckstore

import (
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

func TestN060NotifyArgsAreCleanedOnDisk(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })

	token := "sk-FAKETOKEN99"
	ansi := "\x1b[31mRED\x1b[0m"
	long := strings.Repeat("A", 700)
	d := testDirs(t)
	s, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		arg  string
		hide string
		keep string
	}{
		{name: "token", arg: token, hide: token},
		{name: "ansi", arg: ansi, hide: "\x1b", keep: "RED"},
		{name: "long", arg: long, hide: long},
	}
	for _, tc := range cases {
		if _, err := s.NotifyCoded("p", "report", i18n.E("err.deckd.serverrunspawn.47", tc.arg), "deckd"); err != nil {
			t.Fatal(err)
		}
	}
	inner := i18n.E("err.deckd.serverrunspawn.47", token)
	if _, err := s.NotifyCoded("p", "report", i18n.E("err.deckd.tmuxcenterfind.3", inner, 7), "deckd"); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(s.path("notifications.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	disk := string(raw)
	if strings.Contains(disk, token) || strings.Contains(disk, "\x1b") || strings.Contains(disk, long) {
		t.Fatal("落盘的通知仍有原文、控制字符或超长参数")
	}

	s2, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	ns := s2.Notifications(0, "")
	if len(ns) != 4 {
		t.Fatalf("重读应有 4 条，实际 %d", len(ns))
	}
	for i, tc := range cases {
		n := ns[i]
		checkNote(t, tc.name, n, tc.hide, tc.keep)
	}
	nest := ns[3]
	if nest.Code != "err.deckd.tmuxcenterfind.3" {
		t.Fatalf("嵌套外层码丢了：%s", nest.Code)
	}
	i18n.SetLang(i18n.EN)
	en := deckclient.Text(nest.Code, []any(nest.Args), nest.Text)
	if strings.Contains(en, token) || !strings.Contains(en, "cannot send the brief") || !strings.Contains(en, "no longer in the center") {
		t.Fatalf("嵌套码被洗坏了：%q", en)
	}
	if !strings.Contains(en, "[已打码]") {
		t.Fatalf("英文应保留已打进参数的中文打码记号：%q", en)
	}
}

func checkNote(t *testing.T, name string, n Notification, hide, keep string) {
	t.Helper()
	if strings.Contains(n.Text, hide) {
		t.Errorf("%s 落盘正文仍含 %q", name, hide)
	}
	for _, a := range n.Args {
		s, ok := a.(string)
		if !ok {
			continue
		}
		if strings.Contains(s, hide) {
			t.Errorf("%s 落盘参数仍含 %q：%q", name, hide, s)
		}
		if utf8.RuneCountInString(s) > 601 {
			t.Errorf("%s 参数超长：%d", name, utf8.RuneCountInString(s))
		}
	}
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		text := deckclient.Text(n.Code, []any(n.Args), n.Text)
		text = Clip(text, 600)
		if strings.Contains(text, hide) {
			t.Errorf("%s %s 渲染仍含 %q：%q", name, lang, hide, text)
		}
		if keep != "" && !strings.Contains(text, keep) {
			t.Errorf("%s %s 渲染应留下 %q：%q", name, lang, keep, text)
		}
		if nrunes := utf8.RuneCountInString(text); nrunes > 601 {
			t.Errorf("%s %s 整句超长：%d", name, lang, nrunes)
		}
	}
}

func TestN060ListSepFollowsLang(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	e := &ErrAllExhausted{Project: "deck", Tier: Balanced, Skipped: []string{"alpha", "beta"}}
	zh := e.Error()
	if !strings.Contains(zh, "alpha、beta") {
		t.Fatalf("中文应用顿号：%q", zh)
	}
	code, args, ok := i18n.Wire(e)
	if !ok {
		t.Fatal("应有码")
	}
	i18n.SetLang(i18n.EN)
	en := deckclient.Text(code, args, zh)
	if !strings.Contains(en, "alpha, beta") || strings.Contains(en, "、") || strings.Contains(en, "额度") {
		t.Fatalf("英文应用逗号，且不再夹中文：%q", en)
	}
}

func TestN060StoredIntsRenderWithPercentD(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	d := testDirs(t)
	s, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	msg := i18n.E("err.deckd.serverrunspawn.25", 2, 3)
	if _, err := s.NotifyCoded("", "spawn", msg, "deckd"); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	ns := s2.Notifications(0, "")
	if len(ns) != 1 || len(ns[0].Args) != 2 {
		t.Fatalf("重读通知不对：%+v", ns)
	}
	i18n.SetLang(i18n.ZH)
	zh := deckclient.Text(ns[0].Code, []any(ns[0].Args), ns[0].Text)
	i18n.SetLang(i18n.EN)
	en := deckclient.Text(ns[0].Code, []any(ns[0].Args), ns[0].Text)
	if zh != msg.Error() {
		t.Fatalf("中文应与原文一致：%q", zh)
	}
	const want = "the new main agent took over 2 sub-agents and 3 reports that the old main agent left waiting to be sent"
	if en != want || strings.Contains(en, "%!d") || strings.Contains(en, "float64") {
		t.Fatalf("英文 %%d 不对：%q", en)
	}
}
