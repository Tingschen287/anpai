package logo

import (
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Codex 复审 3 的 R3-1：决策文件只读前 128 字节，「合法前缀 + 超限后的额外内容」会被当成有效决策。
// 现在多探一个字节：长度超过 128 就是坏文件（decBad），读者按字母徽章，不回退到旧文件，也不看 DECK_GRAPHICS。

// 合法前缀（「sixel -」补空格到 n 字节）后接 tail，整个文件是 n+len(tail) 字节。
func overlongDecision(n int, tail string) string {
	return "sixel -" + strings.Repeat(" ", n-len("sixel -")) + tail
}

func TestOverlongDecisionFileIsBadEvenWithValidPrefix(t *testing.T) {
	t.Cleanup(func() { SetBackground(nil) })
	t.Setenv("DECK_GRAPHICS", "")
	cases := map[string]string{
		"prefix128+none-":           overlongDecision(128, "none -\n"), // 135 字节：Codex 反例
		"prefix129+none-":           overlongDecision(129, "none -\n"), // 136 字节
		"prefix4096+none-":          overlongDecision(4096, "none -\n"),
		"prefix128+junk":            overlongDecision(128, "garbage"),
		"prefix128+1byte":           overlongDecision(128, "x"),  // 恰好 129 字节：越界一个字节
		"prefix128+newline":         overlongDecision(128, "\n"), // 129 字节，多的只是换行：仍超限
		"sixel-color-prefix+extras": "sixel 1e1e1e" + strings.Repeat(" ", 118) + " none -\n",
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			d := t.TempDir()
			if err := os.WriteFile(DecisionFile(d), []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			SetBackground(&color.RGBA{1, 2, 3, 255}) // 看 Apply 是否把它清掉
			m, b, ok := ReadDecision(d)
			if ok {
				t.Fatalf("%d 字节的文件被接受：ReadDecision=(%q,%v,%v)", len(data), m, b, ok)
			}
			if got := Apply(d); got != "none" {
				t.Fatalf("%d 字节的文件 Apply=%q，应为 none", len(data), got)
			}
			if Background() != nil {
				t.Fatalf("%d 字节的文件 Apply 后背景应为 nil，得 %v", len(data), Background())
			}
		})
	}
}

// 坏的新文件不退到旧两文件格式，也不理会 DECK_GRAPHICS=sixel。
func TestOverlongDecisionDoesNotFallBackToLegacyOrEnv(t *testing.T) {
	t.Cleanup(func() { SetBackground(nil) })
	t.Setenv("DECK_GRAPHICS", "sixel")
	d := t.TempDir()
	if err := os.WriteFile(ModeFile(d), []byte("sixel\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(BackgroundFile(d), []byte("1e1e1e\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := overlongDecision(128, "none -\n")
	if err := os.WriteFile(DecisionFile(d), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := ReadDecision(d); ok {
		t.Fatal("超限的新文件被接受")
	}
	if got := Apply(d); got != "none" {
		t.Fatalf("Apply=%q：超限的新文件不应退到旧文件或 DECK_GRAPHICS，应为 none", got)
	}
	if Background() != nil {
		t.Fatalf("背景应为 nil（不取旧 graphics-bg 的 1e1e1e），得 %v", Background())
	}
	// 对照：同样的旧两文件，没有新文件时确实会被采用（证明上面的 none 来自坏文件而不是旧文件本来就读不到）
	os.Remove(DecisionFile(d))
	if got := Apply(d); got != "sixel" || Background() == nil {
		t.Fatalf("对照：无新文件时应采用旧格式 sixel+背景，得 %q %v", got, Background())
	}
}

// 边界：恰好 128 字节（及更短）的合法文件，空白补齐，仍被接受。
func TestDecisionFileAcceptedAtExactly128Bytes(t *testing.T) {
	t.Cleanup(func() { SetBackground(nil) })
	t.Setenv("DECK_GRAPHICS", "")
	for _, tc := range []struct {
		name, data string
		mode       string
		bg         *color.RGBA
	}{
		{"sixel-transparent-128", "sixel -" + strings.Repeat(" ", 127-len("sixel -")) + "\n", "sixel", nil},
		{"sixel-color-128", "sixel 1e1e1e" + strings.Repeat(" ", 127-len("sixel 1e1e1e")) + "\n", "sixel", &color.RGBA{0x1e, 0x1e, 0x1e, 255}},
		{"none-127", "none -" + strings.Repeat(" ", 127-len("none -")), "none", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if n := len(tc.data); n > 128 {
				t.Fatalf("测试数据 %d 字节，超过 128", n)
			}
			d := t.TempDir()
			if err := os.WriteFile(filepath.Join(d, "graphics-decision"), []byte(tc.data), 0o600); err != nil {
				t.Fatal(err)
			}
			m, b, ok := ReadDecision(d)
			if !ok || m != tc.mode || (b == nil) != (tc.bg == nil) || (b != nil && *b != *tc.bg) {
				t.Fatalf("%d 字节：ReadDecision=(%q,%v,%v)", len(tc.data), m, b, ok)
			}
		})
	}
	// 精确 128 字节
	data := "sixel -" + strings.Repeat(" ", 128-len("sixel -"))
	if len(data) != 128 {
		t.Fatalf("构造错误：%d", len(data))
	}
	d := t.TempDir()
	os.WriteFile(DecisionFile(d), []byte(data), 0o600)
	if m, b, ok := ReadDecision(d); !ok || m != "sixel" || b != nil {
		t.Fatalf("恰好 128 字节应接受：(%q,%v,%v)", m, b, ok)
	}
	if got := Apply(d); got != "sixel" {
		t.Fatalf("Apply=%q", got)
	}
}
