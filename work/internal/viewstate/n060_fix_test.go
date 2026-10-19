package viewstate

import (
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

func TestN060SentinelErrorMatchesI18n(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	for _, e := range []*codeErr{
		ErrStaleClient, ErrRestoreUnconfirmed, ErrInputNotClosed, ErrNotOpen, ErrNotFocused,
		ErrNotSub, ErrTakeoverStale, ErrTakeoverBusy, ErrOwnerTyping, ErrNotTaken, ErrClosed,
	} {
		if e.Error() != e.I18n().Error() {
			t.Errorf("%s 的原文和码不一致：%q / %q", e.key, e.Error(), e.I18n().Error())
		}
	}
	hide := i18n.E("err.viewstate.why.hide")
	if hide.Error() != "隐藏" {
		t.Fatalf("隐藏原因的中文应仍是一个字：%q", hide.Error())
	}
	left := i18n.E("err.viewstate.leftway", hide)
	if left.Error() != "离开方式：隐藏" {
		t.Fatalf("离开方式应仍是原文：%q", left.Error())
	}
	part := &ErrPartial{Done: "切走", Cause: ErrNotOpen, done: i18n.E("err.viewstate.why.switch")}
	if part.Error() != part.I18n().Error() {
		t.Fatalf("部分完成不一致：%q / %q", part.Error(), part.I18n().Error())
	}
	hb := &ErrHandback{Session: "s1", Cause: ErrNotOpen}
	if hb.Error() != hb.I18n().Error() {
		t.Fatalf("交还失败不一致：%q / %q", hb.Error(), hb.I18n().Error())
	}
}

func TestN060CodedOuterDoesNotEmbedChineseInner(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	outer := i18n.E("err.deckd.tmuxcenterfind.3", ErrStaleClient, 7)
	if !strings.Contains(outer.Error(), ErrStaleClient.Error()) {
		t.Fatalf("中文外层应仍包含内层原文：%q", outer.Error())
	}
	code, args, ok := i18n.Wire(outer)
	if !ok {
		t.Fatal("外层应有码")
	}
	i18n.SetLang(i18n.EN)
	en := deckclient.Text(code, args, outer.Error())
	if strings.Contains(en, "通知来自已经不在中间的旧 client") || !strings.Contains(en, "notification came from an old client") {
		t.Fatalf("英文外层不应夹中文内层：%q", en)
	}
}
