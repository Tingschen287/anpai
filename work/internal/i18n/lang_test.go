package i18n

import "testing"

// 段 3：第一次运行跟随系统语言。LC_ALL → LC_MESSAGES → LANG 取第一个非空值（POSIX 的规则），
// 都空时再看 LANGUAGE；以 zh 开头用中文，否则英文。

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestN060S3SystemLangFromEnvironment(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"zh_CN.UTF-8", map[string]string{"LANG": "zh_CN.UTF-8"}, ZH},
		{"en_US.UTF-8", map[string]string{"LANG": "en_US.UTF-8"}, EN},
		{"C", map[string]string{"LANG": "C"}, EN},
		{"POSIX", map[string]string{"LANG": "POSIX"}, EN},
		{"C.UTF-8", map[string]string{"LANG": "C.UTF-8"}, EN},
		{"全空", map[string]string{}, EN},
		{"只有空白", map[string]string{"LANG": "  ", "LC_ALL": "\t"}, EN},
		{"zh_TW", map[string]string{"LANG": "zh_TW.UTF-8"}, ZH},
		{"zh 大写", map[string]string{"LANG": "ZH_CN"}, ZH},
		{"zh 无地区", map[string]string{"LANG": "zh"}, ZH},
		{"LC_ALL 优先于 LANG", map[string]string{"LC_ALL": "en_US.UTF-8", "LANG": "zh_CN.UTF-8"}, EN},
		{"LC_ALL 优先于 LANG（反过来）", map[string]string{"LC_ALL": "zh_CN.UTF-8", "LANG": "en_US.UTF-8"}, ZH},
		{"LC_ALL=C 就是英文，不往后找", map[string]string{"LC_ALL": "C", "LANG": "zh_CN.UTF-8"}, EN},
		{"LC_MESSAGES 优先于 LANG", map[string]string{"LC_MESSAGES": "zh_CN.UTF-8", "LANG": "en_US.UTF-8"}, ZH},
		{"LC_ALL 空时看 LC_MESSAGES", map[string]string{"LC_ALL": "", "LC_MESSAGES": "en_GB", "LANG": "zh_CN"}, EN},
		{"LC_ALL、LC_MESSAGES 都空时看 LANG", map[string]string{"LC_ALL": "", "LC_MESSAGES": "", "LANG": "zh_CN"}, ZH},
		{"三个都空才看 LANGUAGE", map[string]string{"LANGUAGE": "zh_CN:en_US"}, ZH},
		{"LANGUAGE 取第一项", map[string]string{"LANGUAGE": "en_US:zh_CN"}, EN},
		{"有 LANG 就不看 LANGUAGE", map[string]string{"LANG": "en_US.UTF-8", "LANGUAGE": "zh_CN"}, EN},
		{"别的语言", map[string]string{"LANG": "ja_JP.UTF-8"}, EN},
	}
	for _, c := range cases {
		if got := SystemLang(envOf(c.env)); got != c.want {
			t.Errorf("%s：得到 %s，应为 %s", c.name, got, c.want)
		}
	}
}

func TestN060S3ValidLang(t *testing.T) {
	for code, want := range map[string]bool{ZH: true, EN: true, "": false, "fr": false, "ZH": false, "zh_CN": false} {
		if ValidLang(code) != want {
			t.Errorf("ValidLang(%q) 应为 %v", code, want)
		}
	}
}

// 新增的文字：中英两套都有，英文没有汉字。
func TestN060S3NewTextsExistInBothLanguages(t *testing.T) {
	for _, k := range []Key{"settings.lang.label", "settings.lang.zh", "settings.lang.en", "help.lang", "legacy.label", "err.deckstore.settingsvalidate.17", "page.general", "keys.general"} {
		z, zok := TextZH(k)
		e, eok := TextEN(k)
		if !zok || !eok || z == "" || e == "" {
			t.Errorf("%s 缺译文", k)
		}
		for _, r := range e {
			if (r >= 0x4e00 && r <= 0x9fff) || (r >= 0x3000 && r <= 0x303f) || (r >= 0xff00 && r <= 0xffef) {
				t.Errorf("英文 %s 里有汉字：%q", k, e)
				break
			}
		}
	}
}
