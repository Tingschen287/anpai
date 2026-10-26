package deckstore

import (
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 段 3 返工 3，R2-1（路径 2 的近邻）：路径检查失败的提示里，路径是外部文字，显示用的参数先中和。
func TestN060S3Fix3CheckPathErrorDoesNotCarryForgedMark(t *testing.T) {
	for _, p := range []string{"relative-" + i18n.RedactMark, "/abs/ctl\n-" + i18n.RedactMark} {
		err := CheckPath(p)
		if err == nil {
			t.Fatalf("%q 应当不合格", p)
		}
		_, args, ok := i18n.Wire(err)
		if !ok {
			t.Fatalf("应是带码错误：%v", err)
		}
		for _, a := range args {
			if s, isStr := a.(string); isStr && i18n.HasMark(s) {
				t.Errorf("%q：参数仍带标记字符 %q", p, s)
			}
		}
		if i18n.HasMark(err.Error()) {
			t.Errorf("err.Error() 不该有标记字符：%q", err.Error())
		}
	}
}
