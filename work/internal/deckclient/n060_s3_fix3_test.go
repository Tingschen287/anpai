package deckclient

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// n060 段 3 返工 3，R2-1（路径 1）：最外层不带码的错误，Show 显示用的副本要先中和；错误本身与 err.Error() 原文不改。

func TestN060S3Fix3ShowNeutralizesOuterUncodedError(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	missing := filepath.Join(t.TempDir(), "external-"+i18n.RedactMark)
	_, raw := os.ReadDir(missing) // 真实的 *os.PathError，最外层没有码
	if raw == nil {
		t.Fatal("expected filesystem error")
	}
	rawText := raw.Error()
	for _, lang := range []string{i18n.EN, i18n.ZH} {
		i18n.SetLang(lang)
		for name, err := range map[string]error{
			"PathError": raw,
			"plain":     errors.New("x " + i18n.RedactMark),
			"wrapped":   fmt.Errorf("读不了：%w", raw),
		} {
			shown := Show(err)
			if strings.Contains(shown, "[redacted]") || strings.Contains(shown, "[已打码]") || i18n.HasMark(shown) {
				t.Errorf("%s/%s：外部文字被当成打码：%q", lang, name, shown)
			}
		}
	}
	if raw.Error() != rawText || !strings.Contains(rawText, i18n.RedactMark) {
		t.Error("错误本身与 err.Error() 原文不能改")
	}
}

// 带码错误外面再套一层外部文字：真标记（来自带码部分的字符串参数）保留，外壳里伪造的不变成第二个记号。
func TestN060S3Fix3ShowKeepsGenuineMarkInsideCodedError(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	genuine := i18n.E("err.deckclient.status", 502, "real "+i18n.RedactMark)
	wrapped := fmt.Errorf("external %s: %w", i18n.RedactMark, genuine)
	for lang, want := range map[string]string{i18n.EN: "[redacted]", i18n.ZH: "[已打码]"} {
		i18n.SetLang(lang)
		for name, err := range map[string]error{"coded": genuine, "wrapped": wrapped} {
			got := Show(err)
			if strings.Count(got, want) != 1 || i18n.HasMark(got) {
				t.Errorf("%s/%s：真标记应保留且只有一个记号：%q", lang, name, got)
			}
		}
	}
}
