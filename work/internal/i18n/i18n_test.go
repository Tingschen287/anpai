package i18n

import (
	"regexp"
	"strings"
	"testing"
	"unicode"
)

func cjk(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF) {
			return true
		}
	}
	return false
}

var verbRe = regexp.MustCompile(`%[-+# 0]*[0-9]*(\.[0-9]+)?[a-zA-Z]`)

func verbs(s string) []string {
	return verbRe.FindAllString(strings.ReplaceAll(s, "%%", ""), -1)
}

func TestDefaultIsChinese(t *testing.T) {
	SetLang(ZH)
	if Lang() != ZH || T("title") != "设置" || T("main.uses", "x") != "起来时用 x" {
		t.Fatalf("默认中文：%s %q", Lang(), T("title"))
	}
	SetLang(EN)
	t.Cleanup(func() { SetLang(ZH) })
	if Lang() != EN || T("title") == "设置" || cjk(T("title")) || T("brand.name") != "Anpai" {
		t.Fatalf("英文：%s title %q brand %q", Lang(), T("title"), T("brand.name"))
	}
	SetLang("nope")
	if Lang() != ZH || T("title") != "设置" {
		t.Fatal("不认识的语言应回到中文")
	}
}

func TestMissingTranslationFallsBackToChinese(t *testing.T) {
	const key = "test.only.zh"
	zh[key] = "只有中文"
	t.Cleanup(func() {
		delete(zh, key)
		SetLang(ZH)
	})
	SetLang(EN)
	if got := T(key); got != "只有中文" {
		t.Fatalf("缺英文应回退中文，得到 %q", got)
	}
	if got := T("没有这个键"); got != "没有这个键" {
		t.Fatalf("两种都没有应返回键名，得到 %q", got)
	}
}

// 中英键集合一致，占位符的个数和种类一致，英文里没有中文（含中文标点）。
func TestTablesMatch(t *testing.T) {
	if len(zh) != len(en) {
		t.Fatalf("zh %d 条，en %d 条", len(zh), len(en))
	}
	for k, zv := range zh {
		ev, ok := en[k]
		if !ok {
			t.Errorf("英文没有 %q", k)
			continue
		}
		if vz, ve := verbs(zv), verbs(ev); strings.Join(vz, ",") != strings.Join(ve, ",") {
			t.Errorf("%s 占位符不一致：中文 %v，英文 %v", k, vz, ve)
		}
		if cjk(ev) {
			t.Errorf("英文 %s 含中文：%q", k, ev)
		}
		if zv == "" || ev == "" {
			t.Errorf("%s 有空译文", k)
		}
	}
	for k := range en {
		if _, ok := zh[k]; !ok {
			t.Errorf("中文没有 %q", k)
		}
	}
}
