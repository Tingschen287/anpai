package viewstate

import (
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 返工 3：交还时画面末行是外部文字，清洗后带中性标记。
//   - 写进写入权记账的 LastLines 是中文：标记换成中文记号，和 63f6247 逐字一样；
//   - 通知参数里留着标记，英文界面显示 [redacted]；
//   - 通知里提到会话标题时，本项目拼的标题（主 agent）是嵌套码，英文通知里没有中文。
func TestN060HandbackLedgerIsChineseAndNoticeKeepsTheMark(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	w := newWorld(t)
	w.targets["subA"] = Target{Session: "subA", Tmux: "agentdeck_a", Role: "sub", Title: "主 agent · x", TitleCode: "err.deckd.serverpostmain.4", TitleArgs: []any{"x"}}
	var notes []*i18n.Error
	w.m = New(&scriptedCapture{fakeCenter: w.c, line: "画面 sk-FAKETOKEN99 结尾"}, w.l, func(s string) (Target, bool) {
		x, ok := w.targets[s]
		return x, ok
	}, func(n *i18n.Error) error {
		notes = append(notes, n)
		return nil
	}, func() error { return nil })
	w.m.SetClock(func() time.Time { return w.now }, func(d time.Duration) { w.now = w.now.Add(d) })
	for _, step := range []func() error{
		func() error { return w.m.Open("subA") },
		func() error { return w.m.Takeover("subA") },
		func() error { return w.m.Open("subB") }, // 切走 subA：自动交还
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	ev, _ := w.l.Events()
	hb := ev[len(ev)-1].Handback
	if hb == nil || len(hb.LastLines) != 1 || hb.LastLines[0] != "画面 [已打码] 结尾" {
		t.Fatalf("记账里的画面末行应是中文记号：%+v", hb)
	}
	for _, n := range hb.Notes {
		if strings.ContainsAny(n, i18n.RedactMark) {
			t.Fatalf("记账备注不应带标记字符：%q", n)
		}
	}
	var left *i18n.Error
	var took *i18n.Error
	for _, n := range notes {
		switch n.Key {
		case "err.viewstate.leftline":
			left = n
		case "err.viewstate.tookover":
			took = n
		}
	}
	if left == nil || took == nil {
		t.Fatalf("应有「离开」和「接管」两条通知：%v", notes)
	}
	// 中文原文：标记是中文记号，标题是原来的中文
	if got := left.Error(); !strings.Contains(got, "画面 [已打码] 结尾") || !strings.Contains(got, "主 agent · x") {
		t.Fatalf("中文通知变了：%q", got)
	}
	// 英文：画面末行里的标记换成 [redacted]，标题是码翻译出来的
	for _, n := range []*i18n.Error{left, took} {
		code, args, _ := i18n.Wire(n)
		i18n.SetLang(i18n.EN)
		en := deckclient.Text(code, args, n.Error())
		i18n.SetLang(i18n.ZH)
		for _, zh := range []string{"主 agent", "已打码"} {
			if strings.Contains(en, zh) {
				t.Fatalf("英文通知不该有 %q：%q", zh, en)
			}
		}
		if n == left && (!strings.Contains(en, "[redacted]") || !strings.Contains(en, "main agent: x")) {
			t.Fatalf("英文「离开」通知：%q", en)
		}
		if n == took && !strings.Contains(en, "main agent: x") {
			t.Fatalf("英文「接管」通知：%q", en)
		}
	}
}
