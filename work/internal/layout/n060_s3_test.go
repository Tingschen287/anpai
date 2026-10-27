package layout

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 段 3：中间窗格停住等按键期间，owner 改了界面语言——PauseLive 拿到的是函数，每次调用按此刻的语言重新拼原因，
// 调用方（deck center）据此整屏重画。中文原文与以前逐字一样。
func TestN060S3CenterPauseLiveReasonFollowsTheLanguage(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	i18n.SetLang(i18n.ZH)
	var clock time.Time
	var zhReason, enReason, zhAgain string
	var legacyPaused bool
	l := &CenterLoop{
		Now: func() time.Time { return clock },
		Attach: func() (int, func() error, error) {
			return 7, func() error { return errors.New("tmux 退出了") }, nil
		},
		Announce: func(context.Context, string, int) {},
		Pause:    func(string) bool { legacyPaused = true; return false },
		PauseLive: func(reason func() string) bool {
			zhReason = reason()
			i18n.SetLang(i18n.EN)
			enReason = reason()
			i18n.SetLang(i18n.ZH)
			zhAgain = reason()
			return false
		},
		MaxQuick: 2, QuickExit: 3 * time.Second,
	}
	l.Run()
	if legacyPaused {
		t.Fatal("有 PauseLive 就不该调 Pause")
	}
	if zhReason == "" || zhReason == enReason || zhAgain != zhReason {
		t.Fatalf("原因应随语言重拼，切回后与切换前一致：\n%q\n%q\n%q", zhReason, enReason, zhAgain)
	}
	if !strings.Contains(zhReason, "tmux 退出了") || !strings.Contains(enReason, "tmux 退出了") {
		t.Fatalf("外部原文原样保留：%q %q", zhReason, enReason)
	}
	for _, r := range strings.ReplaceAll(enReason, "tmux 退出了", "") {
		if (r >= 0x4e00 && r <= 0x9fff) || (r >= 0x3000 && r <= 0x303f) || (r >= 0xff00 && r <= 0xffef) {
			t.Fatalf("英文原因里除外部原文外还有汉字 %q：%q", r, enReason)
		}
	}
}

// 没有 PauseLive 时照旧调 Pause，原因是此刻拼好的字符串（既有行为不变）。
func TestN060S3CenterPauseStillWorksWithoutPauseLive(t *testing.T) {
	var got string
	l := &CenterLoop{
		Now:      func() time.Time { return time.Unix(0, 0) },
		Attach:   func() (int, func() error, error) { return 0, nil, errors.New("连不上") },
		Announce: func(context.Context, string, int) {},
		Pause:    func(r string) bool { got = r; return false },
		MaxQuick: 1,
	}
	l.Run()
	if got == "" {
		t.Fatal("应调用 Pause")
	}
}
