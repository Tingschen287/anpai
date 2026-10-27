package layout

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 返工 4：tmux 退出说明是带码的错误（cmd/deck 的 center 包的），停止原因按界面语言显示：
// 中文和原来逐字一样，英文没有本地中文（全角括号）。
func TestN060CenterStopReasonCarriesTheCodedAttachExit(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	for _, c := range []struct{ lang, want string }{
		{i18n.ZH, "。最后一次：exit status 1（tmux said [已打码]）"},
		{i18n.EN, ". Last error: exit status 1 (tmux said [redacted])"},
	} {
		i18n.SetLang(c.lang)
		var reason string
		now := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
		l := &CenterLoop{
			Now: func() time.Time { return now },
			Attach: func() (int, func() error, error) {
				return 1, func() error {
					return i18n.E("err.deck.center.attachexit", errors.New("exit status 1"), "tmux said "+i18n.RedactMark)
				}, nil
			},
			Announce: func(context.Context, string, int) {},
			Pause:    func(r string) bool { reason = r; return false },
		}
		l.Run()
		if !strings.HasSuffix(reason, c.want) {
			t.Errorf("%s：停止原因应以 %q 结尾：%q", c.lang, c.want, reason)
		}
		if c.lang == i18n.EN {
			for _, r := range reason {
				if unicode.Is(unicode.Han, r) || r == '（' || r == '）' {
					t.Errorf("英文停止原因里有本地中文：%q", reason)
					break
				}
			}
		}
	}
}
