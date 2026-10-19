package deckd

import (
	"context"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 写入权检查的本地原因走真实 Handler 往返：中文 error 字段逐字不变，英文渲染里不含中文。
func TestN060LeaseReasonThroughHandler(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })

	cases := []struct {
		name   string
		setup  func(w *agentWorld)
		zhHas  string
		enHas  string
		status int
	}{
		{
			name:   "没有写入权记录",
			setup:  func(w *agentWorld) {},
			zhHas:  "没有写入权记录：不是 Work Deck 派出、或记录丢失，持有者未知，不自动投递",
			enHas:  "has no write-access record",
			status: 409,
		},
		{
			name:   "owner 接管中",
			setup:  func(w *agentWorld) { w.leases.OwnerTakeover(w.sub.AgentDeckID, "deck") },
			zhHas:  "正由 owner 接管",
			enHas:  "taken over by the owner",
			status: 409,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			i18n.SetLang(i18n.ZH)
			w := newAgentWorld(t)
			tc.setup(w)
			code, b := w.do("owner", "POST", "/v1/agents/"+w.sub.Key+"/send", `{"text":"再做一件"}`)
			if code != tc.status {
				t.Fatalf("状态码 %d：%s", code, b)
			}
			m := decode(t, b)
			zh, _ := m["error"].(string)
			if !strings.Contains(zh, tc.zhHas) {
				t.Fatalf("中文 error 字段应保留原文：%q", zh)
			}
			c, _ := m["code"].(string)
			args, _ := m["args"].([]any)
			if c == "" {
				t.Fatalf("响应应带码：%s", b)
			}
			// 中文显示：渲染结果与 error 字段逐字相同。
			if got := deckclient.Text(c, args, zh); got != zh {
				t.Fatalf("中文显示应与 error 字段逐字一样：%q / %q", got, zh)
			}
			i18n.SetLang(i18n.EN)
			en := deckclient.Text(c, args, zh)
			if hasHan(en) {
				t.Fatalf("英文里不应有中文：%q", en)
			}
			if !strings.Contains(en, tc.enHas) {
				t.Fatalf("英文应含 %q：%q", tc.enHas, en)
			}
		})
	}
}

// 发话未确认：note 字段仍是中文原文，同时带 note_code / note_args，英文走码。
func TestN060DeliveryUnconfirmedNoteIsCoded(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	w := newAgentWorld(t)
	w.leases.Grant(w.sub.AgentDeckID, "测试")
	w.up.sendErr = &agentdeck.ErrDeliveryUnconfirmed{SessionID: w.sub.AgentDeckID, Detail: "delivery typed, no submit proof"}
	code, b := w.do("owner", "POST", "/v1/agents/"+w.sub.Key+"/send", `{"text":"再做一件"}`)
	if code != 202 {
		t.Fatalf("送达未确认应是 202：%d %s", code, b)
	}
	m := decode(t, b)
	note, _ := m["note"].(string)
	nc, _ := m["note_code"].(string)
	na, _ := m["note_args"].([]any)
	if note == "" || nc == "" {
		t.Fatalf("应有 note 与 note_code：%s", b)
	}
	if got := deckclient.Text(nc, na, note); got != note {
		t.Fatalf("中文显示应与 note 逐字一样：%q / %q", got, note)
	}
	i18n.SetLang(i18n.EN)
	en := deckclient.Text(nc, na, note)
	if hasHan(en) || !strings.Contains(en, "delivery typed, no submit proof") {
		t.Fatalf("英文应只留上游原文：%q", en)
	}
}

// 停止原因：带码的本地原因超长被截断后，中文仍是截断后的原文，英文走码，不显示截断后的中文。
func TestN060StopReasonKeepsCodeWhenTruncated(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	local := i18n.E("err.deckd.serverrunspawn.47", strings.Repeat("A", 400))
	arg := nestOrText(local, 60)
	err, ok := arg.(error)
	if !ok || i18n.AsCoded(err) == nil {
		t.Fatalf("截断后也应留下码：%#v", arg)
	}
	if n := len([]rune(err.Error())); n > 61 || !strings.HasSuffix(err.Error(), "…") {
		t.Fatalf("中文 Error() 应是截断后的原文：%d %q", n, err.Error())
	}
	n := i18n.E("err.deckd.serverstoplaunched.2", "标题", "子 agent", arg)
	c, args, ok := i18n.Wire(n)
	if !ok {
		t.Fatal("应有码")
	}
	i18n.SetLang(i18n.EN)
	en := deckclient.Text(c, args, n.Error())
	if strings.Contains(en, "…") && hasHan(strings.NewReplacer("标题", "", "子 agent", "").Replace(en)) {
		t.Fatalf("英文里不应有截断后的中文：%q", en)
	}
	// 外部原文（字符串）仍是字符串，标记换成语言无关的记号。
	i18n.SetLang(i18n.ZH)
	if s, ok := nestOrText("upstream sk-FAKETOKEN99 failed", 200).(string); !ok || strings.Contains(s, "sk-FAKE") || !strings.Contains(s, i18n.RedactMark) {
		t.Fatalf("外部原文应是带标记的字符串：%#v", s)
	}
}

// 启动结果：有码的错误留下码，没有码的上游原文清洗后仍是字符串。
func TestN060LaunchOutcomeKeepsCode(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	local := i18n.E("err.deckd.serverrunspawn.47", "x")
	_, _, why := launchOutcome(agentdeck.LaunchResult{}, local)
	if e, ok := why.(error); !ok || i18n.AsCoded(e) == nil {
		t.Fatalf("有码的启动错误应留下码：%#v", why)
	}
	_, uncertain, why := launchOutcome(agentdeck.LaunchResult{}, context.DeadlineExceeded)
	if _, ok := why.(string); !ok {
		t.Fatalf("上游原文应是字符串：%#v", why)
	}
	if !uncertain {
		t.Fatal("超时是结果不明")
	}
}

// 新建主 agent 时没确认提交的启动提示：来自 deckstore.Arg 的码，英文里没有中文。
func TestN060ArgOfUnconfirmedBriefIsCoded(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	derr := &agentdeck.ErrDeliveryUnconfirmed{SessionID: "ad-1", Detail: "typed"}
	e := i18n.E("err.deckd.serverrunspawn.48", deckstore.Arg(derr, 160))
	c, args, ok := i18n.Wire(e)
	if !ok {
		t.Fatal("应有码")
	}
	i18n.SetLang(i18n.EN)
	if en := deckclient.Text(c, args, e.Error()); hasHan(en) {
		t.Fatalf("英文里不应有中文：%q", en)
	}
}
