package layout

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 返工 3：中间窗格「停止重试」的原因里带着 client 的 stderr 清洗后的文字（cmd/deck/up.go 先 Clean），
// 里面的中性打码标记在这个显示出口按当前语言翻成记号：中文「[已打码]」（和 63f6247 逐字一样），英文「[redacted]」。
func TestN060CenterPauseReasonTranslatesTheMark(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	for _, c := range []struct{ lang, want string }{{i18n.ZH, "[已打码]"}, {i18n.EN, "[redacted]"}} {
		i18n.SetLang(c.lang)
		var reason string
		now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
		l := &CenterLoop{
			Now: func() time.Time { return now },
			Attach: func() (int, func() error, error) {
				// 生产里的带码形状（cmd/deck/up.go：i18n.E("err.deck.center.attachexit", err, deckstore.Clean(最后一行, 160))），
				// 不是不带码的 errors.New 装一个真标记（段 3 复审 2：夹具改成生产形状，下面的中英文短语断言一字未动）
				return 1, func() error {
					return i18n.E("err.deck.center.attachexit", errors.New("exit status 1"), "tmux said "+i18n.RedactMark)
				}, nil
			},
			Announce: func(context.Context, string, int) {},
			Pause:    func(r string) bool { reason = r; return false },
		}
		l.Run()
		if strings.ContainsAny(reason, i18n.RedactMark) || !strings.Contains(reason, "tmux said "+c.want) {
			t.Fatalf("%s：停止原因应显示 %q：%q", c.lang, c.want, reason)
		}
	}
}
