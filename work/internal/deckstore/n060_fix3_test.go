package deckstore

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/wirecheck"
)

// 返工 3 任务 1：伪造中性标记的输入被中和。输入里本来就有的标记字符先被去掉，外部文字造不出「已打码」。
func TestN060CleanNeutralizesForgedMarks(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	forged := "前 " + i18n.RedactMark + " 中   后"
	got := Clean(forged, 0)
	if strings.ContainsAny(got, i18n.RedactMark) {
		t.Fatalf("伪造的标记字符应被去掉：%q", got)
	}
	if strings.Contains(i18n.Present(got), "[已打码]") || strings.Contains(i18n.Present(got), "[redacted]") {
		t.Fatalf("伪造的标记不能显示成打码记号：%q", i18n.Present(got))
	}
	// 真的密钥照常打码，而且只有这一处是标记
	mixed := Clean(i18n.RedactMark+" sk-FAKETOKEN99 "+i18n.RedactMark, 0)
	if strings.Count(mixed, i18n.RedactMark) != 1 || strings.Contains(mixed, "sk-FAKE") {
		t.Fatalf("只有真打码的一处是标记：%q", mixed)
	}
	// 参数版也一样：Arg 对无码错误第一次清洗，去掉伪造的标记
	arg, _ := Arg(errors.New("x "+i18n.RedactMark+" y"), 0).(string)
	if strings.ContainsAny(arg, i18n.RedactMark) {
		t.Fatalf("Arg 的外部原文里伪造的标记应被去掉：%q", arg)
	}
	// Present 对不成形的标记字符也不漏私用区字符
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		if s := i18n.Present("abc"); strings.ContainsAny(s, i18n.RedactMark) {
			t.Fatalf("%s：Present 不应漏出标记字符：%q", lang, s)
		}
	}
}

// Tidy 是「再清洗一遍」：不去掉真的标记，所以对 Clean 的结果幂等；Clean 对 Clean 的结果不幂等（会去掉标记），只用在原文上。
func TestN060TidyKeepsGenuineMarks(t *testing.T) {
	in := "a sk-FAKETOKEN99 b password=hunter22 c"
	once := Clean(in, 0)
	if Tidy(once, 0) != once || ArgText(once, 0) != once {
		t.Fatalf("Tidy 和 ArgText 对已清洗的文字应原样：%q / %q", Tidy(once, 0), once)
	}
	if Clean(once, 0) == once {
		t.Fatal("Clean 要去掉输入里的标记，不该对它自己的结果幂等")
	}
	// 显示出口：Display 保留标记并翻译，不抹掉
	i18n.SetLang(i18n.EN)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	if got := Display(once, 0); got != "a [redacted] b [redacted] c" {
		t.Fatalf("英文 Display：%q", got)
	}
	i18n.SetLang(i18n.ZH)
	if got := Display(once, 0); got != "a [已打码] b [已打码] c" {
		t.Fatalf("中文 Display 应和 63f6247 逐字一样：%q", got)
	}
	// 对原文用 Display 也行：没有标记时现打码，显示出口再翻译
	if got := Display(in, 0); got != "a [已打码] b [已打码] c" {
		t.Fatalf("原文直接 Display：%q", got)
	}
}

// 外部原文里字面写着的「[已打码]」不是标记：参数、通知、中英文界面都原样保留。
func TestN060ExternalLiteralMarkIsKeptInEveryPath(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	lit := "upstream literal [已打码] end"
	wirecheck.External(lit) // 夹具里的上游原文，恰好含有字面的中文记号
	if Clean(lit, 0) != lit || ArgText(lit, 0) != lit || Tidy(lit, 0) != lit {
		t.Fatal("字面的 [已打码] 被清洗改写了")
	}
	if got, _ := Arg(errors.New(lit), 0).(string); got != lit {
		t.Fatalf("Arg 改写了外部原文：%q", got)
	}
	d := testDirs(t)
	s, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	// 外部原文里有字面记号，同时有真的密钥
	if _, err := s.NotifyCoded("p", "report", i18n.E("err.deckd.serverrunspawn.47", lit+" sk-FAKETOKEN99"), "deckd"); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	n := s2.Notifications(0, "")[0]
	for _, lang := range []string{i18n.ZH, i18n.EN} {
		i18n.SetLang(lang)
		got := deckclient.Text(n.Code, []any(n.Args), n.Text)
		real := "[已打码]"
		if lang == i18n.EN {
			real = "[redacted]"
		}
		if !strings.Contains(got, "literal [已打码] end") || !strings.Contains(got, real) || strings.Contains(got, "sk-FAKE") {
			t.Fatalf("%s：字面记号要原样、真密钥要打码：%q", lang, got)
		}
	}
}

func openWithSession(t *testing.T) (*Store, string) {
	t.Helper()
	s := withProject(t, testDirs(t), DefaultEnvelope("deck"))
	r, err := s.Reserve(sub(Balanced), "")
	if err != nil {
		t.Fatal(err)
	}
	return s, r.Key
}

// 会话备注、决定结果是记账用的中文：打码标记在落盘前换成中文记号，和 63f6247 逐字一样，不带私用区字符。
func TestN060LedgerTextsAreChinese(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	s, key := openWithSession(t)
	if err := s.LaunchFailed(key, true, Clean("失败 sk-FAKETOKEN99 了", 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAwaitingOwner(key, Clean("等 sk-FAKETOKEN99", 0)); err != nil {
		t.Fatal(err)
	}
	x, _ := s.Session(key)
	if x.Note != "失败 [已打码] 了" || x.AwaitingOwner != "等 [已打码]" {
		t.Fatalf("备注应是中文记号：%q %q", x.Note, x.AwaitingOwner)
	}
	if err := s.SetSessionStatus(key, StatusFailed, "又 password=hunter22"); err != nil {
		t.Fatal(err)
	}
	if x, _ = s.Session(key); x.Note != "又 [已打码]" || strings.ContainsAny(x.Note, i18n.RedactMark) {
		t.Fatalf("状态备注：%q", x.Note)
	}
}

// 决定的摘要、选项、推荐：存下来带中性标记（界面按语言翻译），重读后仍在，假 token 不在盘上。
func TestN060DecisionTextsKeepNeutralMark(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	d := testDirs(t)
	s := withProject(t, d, DefaultEnvelope("deck"))
	dec, err := s.CreateDecision(NewDecision{Project: "deck", Kind: KindCost, Summary: "ship sk-FAKETOKEN99", Options: []string{"go sk-FAKETOKEN99"}, Recommend: "go sk-FAKETOKEN99", CreatedBy: "ses-1"})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s2.Decision(dec.ID)
	if !ok {
		t.Fatal("重读后应在")
	}
	all := got.Summary + "|" + strings.Join(got.Options, "|") + "|" + got.Recommend
	if strings.Contains(all, "sk-FAKE") || strings.Count(all, i18n.RedactMark) != 3 || strings.Contains(all, "[已打码]") {
		t.Fatalf("存下来的决定文字应带中性标记：%q", all)
	}
}

// 任务 5：Arg 和 Wire 对没有码的 %w 包装解包一致：外层只是包装，里面有码，两边都取内层的码。
func TestN060ArgUnwrapsLikeWire(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	inner := i18n.E("err.agentdeck.clientlaunch.4")
	wrapped := fmt.Errorf("%w", inner)
	got := Arg(wrapped, 200)
	e, ok := got.(error)
	if !ok || i18n.AsCoded(e) == nil || i18n.AsCoded(e).Key != inner.Key {
		t.Fatalf("Arg 应取到包装里的码：%#v", got)
	}
	code, _, ok := i18n.Wire(wrapped)
	if !ok || code != string(inner.Key) {
		t.Fatalf("Wire 也应取到同一个码：%q", code)
	}
	if arg := i18n.WireArgs([]any{wrapped}); len(arg) != 1 {
		t.Fatal("WireArgs")
	} else if n, ok := arg[0].(i18n.Nest); !ok || n.Code != string(inner.Key) {
		t.Fatalf("作为参数时也应嵌套内层的码：%#v", arg[0])
	}
	// 英文：外层无码包装不再把内层压成中文字符串
	i18n.SetLang(i18n.EN)
	n := i18n.E("err.deckd.serverrunspawn.brief", got)
	c, args, _ := i18n.Wire(n)
	en := deckclient.Text(c, args, n.Error())
	if hasHan(en) {
		t.Fatalf("英文不应有中文：%q", en)
	}
}

// 假 token、ANSI、超长参数的安全清洗不回退（返工 3 把打码改成中性标记之后再确认一遍）。
func TestN060SafetyCleaningDidNotRegress(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	in := "\x1b[31mRED\x1b[0m sk-FAKETOKEN99 ghp_" + strings.Repeat("a", 24) + " Bearer abcdefgh12345678 " + strings.Repeat("长", 900)
	for name, got := range map[string]string{"Clean": Clean(in, 600), "Tidy": Tidy(in, 600), "ArgText": ArgText(in, 600), "Display": Display(in, 600)} {
		if strings.Contains(got, "\x1b") || strings.Contains(got, "sk-FAKE") || strings.Contains(got, "ghp_a") || strings.Contains(got, "abcdefgh1") {
			t.Fatalf("%s 没清干净：%q", name, got)
		}
		if n := len([]rune(got)); n > 601 || !strings.HasSuffix(got, "…") {
			t.Fatalf("%s 没限长：%d", name, n)
		}
	}
	args := CleanArgs([]any{in, i18n.Nest{Code: "x", Args: []any{in}}, i18n.ListOf("sep.list", in)}, 100)
	if fmt.Sprint(args...) == "" || strings.Contains(fmt.Sprint(args...), "sk-FAKE") || strings.Contains(fmt.Sprint(args...), "\x1b") {
		t.Fatalf("参数没清干净：%v", args)
	}
}
