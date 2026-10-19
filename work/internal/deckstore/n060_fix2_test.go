package deckstore

import (
	"errors"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF) {
			return true
		}
	}
	return false
}

// 返工 3：Clean 打码只写中性标记，不跟任何语言走；中文显示（PresentZH / Present）和 d36dd20 逐字一样。
func TestN060CleanZHMarkIsUnchanged(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	in := "前 sk-FAKETOKEN99 后 password=hunter22 末"
	want := "前 [已打码] 后 [已打码] 末"
	got := Clean(in, 0)
	if strings.Contains(got, "[已打码]") || strings.Contains(got, "sk-FAKE") || strings.Count(got, i18n.RedactMark) != 2 {
		t.Fatalf("Clean 应只写中性标记：%q", got)
	}
	if z := i18n.PresentZH(got); z != want {
		t.Fatalf("中文记号变了：%q", z)
	}
	if z := i18n.Present(got); z != want {
		t.Fatalf("中文界面显示变了：%q", z)
	}
	// 标记不跟界面语言走：英文界面下 Clean 的结果同样，只有显示出口换成 [redacted]。
	i18n.SetLang(i18n.EN)
	if again := Clean(in, 0); again != got {
		t.Fatalf("英文界面下 Clean 不应变：%q", again)
	}
	if en := i18n.Present(got); en != "前 [redacted] 后 [redacted] 末" {
		t.Fatalf("英文显示：%q", en)
	}
	if z := i18n.PresentZH(got); z != want {
		t.Fatalf("PresentZH 不看界面语言：%q", z)
	}
	if RedactLabel() != "[redacted]" {
		t.Fatalf("英文记号：%q", RedactLabel())
	}
	i18n.SetLang(i18n.ZH)
	if RedactLabel() != "[已打码]" {
		t.Fatalf("中文记号：%q", RedactLabel())
	}
}

// 参数版和正文一样存中性标记，渲染时中英各换各的；截断位置和以前直接写中文记号时一样。
func TestN060ArgTextStoresNeutralMark(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	in := "令牌 sk-FAKETOKEN99 到此"
	got := ArgText(in, 0)
	if strings.Contains(got, "[已打码]") || strings.Contains(got, "sk-FAKE") || !strings.Contains(got, i18n.RedactMark) {
		t.Fatalf("参数应存标记：%q", got)
	}
	if i18n.Present(got) != "令牌 [已打码] 到此" {
		t.Fatalf("中文显示：%q", i18n.Present(got))
	}
	i18n.SetLang(i18n.EN)
	if en := i18n.Present(got); hasHan(strings.NewReplacer("令牌", "", "到此", "").Replace(en)) || !strings.Contains(en, "[redacted]") {
		t.Fatalf("英文显示：%q", en)
	}
	// 外部原文里字面的「[已打码]」不是标记，ArgText 不改写它（Codex 复审 3 必须改 1 的反例）。
	lit := "upstream literal [已打码] end"
	if ArgText(lit, 0) != lit {
		t.Fatalf("字面的 [已打码] 被改写：%q", ArgText(lit, 0))
	}
	// 先跑过 Clean 的字符串再进参数，标记原样保留，不被二次清洗抹掉。
	if again := ArgText(Clean(in, 0), 0); again != got {
		t.Fatalf("已清洗的字符串应原样保留标记：%q", again)
	}
	// 截断位置与 d36dd20 一样：旧做法是先写「[已打码]」再按字符截断。中文显示逐字相同，包括只剩「[已」的残缺记号。
	i18n.SetLang(i18n.ZH)
	old := "令牌 [已打码] 到此"
	for _, max := range []int{3, 5, 6, 7, 9, 12} {
		a, c := ArgText(in, max), Clip(old, max)
		if utf8.RuneCountInString(a) != utf8.RuneCountInString(c) || i18n.Present(a) != c {
			t.Fatalf("max=%d 截断不一致：%q / %q", max, i18n.Present(a), c)
		}
	}
}

// Arg：无码错误是外部原文，清洗后存标记；有码错误留码，Error() 仍是清洗后的中文。
func TestN060ArgKeepsCodeAndMarksExternal(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })

	ext := Arg(errors.New("upstream said sk-FAKETOKEN99"), 200)
	s, ok := ext.(string)
	if !ok || strings.Contains(s, "sk-FAKE") || !strings.Contains(s, i18n.RedactMark) {
		t.Fatalf("外部原文应是带标记的字符串：%#v", ext)
	}

	local := i18n.E("err.deckd.serverrunspawn.47", "sk-FAKETOKEN99")
	got := Arg(local, 200)
	coded, ok := got.(error)
	if !ok || i18n.AsCoded(coded) == nil {
		t.Fatalf("有码错误应留下码：%#v", got)
	}
	zh := coded.Error()
	if strings.Contains(zh, "sk-FAKE") || !strings.Contains(zh, "[已打码]") || strings.Contains(zh, i18n.RedactMark) {
		t.Fatalf("中文 Error() 应是清洗后的中文：%q", zh)
	}

	// 截断：外壳 Error() 是截断后的中文，码仍在，英文不显示截断后的中文。
	long := i18n.E("err.deckd.serverrunspawn.47", strings.Repeat("A", 50))
	cut := Arg(long, 10)
	if utf8.RuneCountInString(cut.(error).Error()) > 11 && !strings.HasSuffix(cut.(error).Error(), "…") {
		t.Fatalf("中文 Error() 应被截断：%q", cut.(error).Error())
	}
	n := NotifyArgsRoundTrip(t, "err.deckd.serverrunspawn.48", cut)
	if hasHan(n) {
		t.Fatalf("英文不应含中文：%q", n)
	}
}

// NotifyArgsRoundTrip 把一条带码通知落盘、重读，返回英文渲染。
func NotifyArgsRoundTrip(t *testing.T, key string, args ...any) string {
	t.Helper()
	d := testDirs(t)
	s, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.NotifyCoded("p", "report", i18n.E(i18n.Key(key), args...), "deckd"); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	ns := s2.Notifications(0, "")
	if len(ns) != 1 {
		t.Fatalf("重读应有 1 条：%d", len(ns))
	}
	i18n.SetLang(i18n.EN)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	return deckclient.Text(ns[0].Code, []any(ns[0].Args), ns[0].Text)
}

// 通知里本地先 Clean、再放进参数的字符串（带中性标记），落盘后也不让英文留下中文记号。
func TestN060NotifyArgFromCleanHasNoChineseMark(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	pre := Clean("see sk-FAKETOKEN99 here", 160)
	if !strings.Contains(pre, i18n.RedactMark) {
		t.Fatalf("前提：Clean 带中性标记：%q", pre)
	}
	en := NotifyArgsRoundTrip(t, "err.deckd.serverrunspawn.47", pre)
	if hasHan(en) || !strings.Contains(en, "[redacted]") || strings.Contains(en, "sk-FAKE") {
		t.Fatalf("英文：%q", en)
	}
}
