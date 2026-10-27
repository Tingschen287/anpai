package writerlock

import (
	"strings"
	"testing"
	"unicode"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 写入者锁被占着：中文 Error() 和以前逐字一样，英文里没有中文（持有者是嵌套码）。
func TestN060ErrHeldIsCoded(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	a, err := Acquire("deck", "deckd")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()
	_, err = Acquire("deck", "work")
	if err == nil {
		t.Fatal("第二个写入者应被拒")
	}
	zh := err.Error()
	if !strings.Contains(zh, "的写入者是 deckd（pid ") || !strings.Contains(zh, "：同一个 profile 只许一个写入者，本进程只读、不派工（锁 ") {
		t.Fatalf("中文原文变了：%q", zh)
	}
	code, args, ok := i18n.Wire(err)
	if !ok {
		t.Fatal("应有码")
	}
	if got := deckclient.Text(code, args, zh); got != zh {
		t.Fatalf("中文显示应逐字相同：%q", got)
	}
	i18n.SetLang(i18n.EN)
	en := deckclient.Text(code, args, zh)
	for _, r := range en {
		if unicode.Is(unicode.Han, r) || r == '（' || r == '）' {
			t.Fatalf("英文里不应有中文：%q", en)
		}
	}
	if !strings.Contains(en, "only one writer is allowed per profile") {
		t.Fatalf("英文：%q", en)
	}

	if _, err := Acquire("bad profile", "deckd"); err == nil || !strings.Contains(err.Error(), "不能用作锁文件名") {
		t.Fatalf("坏 profile 名的中文原文变了：%v", err)
	}
}
