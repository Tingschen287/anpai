package i18n

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// 段 3 必改 3：旧数据里没有码的固定系统句子，逐字匹配后补码。

func TestN060LegacyCodeRecognizesEveryFixedSentence(t *testing.T) {
	recognized, ambiguous := 0, 0
	for key, format := range zh {
		if !strings.HasPrefix(string(key), "err.") {
			continue
		}
		lit, verbs, ok := splitFormat(format)
		if !ok || len(verbs) != 0 {
			continue
		}
		code, args, hit := LegacyCode(lit[0])
		if !hit {
			ambiguous++ // 同一句话对上多个英文不同的码：不认
			continue
		}
		recognized++
		if len(args) != 0 {
			t.Errorf("%s：没有参数的句子认出了参数 %v", key, args)
		}
		if zh[Key(code)] != format || en[Key(code)] != en[key] {
			t.Errorf("%s：认成了 %s，中文或英文对不上", key, code)
		}
	}
	if recognized < 100 {
		t.Fatalf("认出的固定句子太少（%d），匹配可能坏了", recognized)
	}
	t.Logf("固定句子认出 %d 条，因同句多码译文不同而不认 %d 条", recognized, ambiguous)
}

// 有参数的模板：参数是标识或整数时能确定反解；填回去的中文与原句逐字相同。
func TestN060LegacyCodeRecoversIdentifierArguments(t *testing.T) {
	recognized := 0
	for key, format := range zh {
		if !strings.HasPrefix(string(key), "err.") {
			continue
		}
		lit, verbs, ok := splitFormat(format)
		if !ok || len(verbs) == 0 || countHan(strings.Join(lit, "")) < 2 {
			continue
		}
		args := make([]any, len(verbs))
		for i, v := range verbs {
			if v == 'd' {
				args[i] = int64(40 + i)
			} else {
				args[i] = fmt.Sprintf("id-%d", i)
			}
		}
		text := fmt.Sprintf(format, args...)
		code, got, hit := LegacyCode(text)
		if !hit {
			continue // 同句多码、或与别的模板撞车
		}
		recognized++
		if fmt.Sprintf(zh[Key(code)], got...) != text {
			t.Errorf("%s：反解出的 %s %v 填回去与原句不同：%q", key, code, got, text)
		}
		if en[Key(code)] != en[key] {
			t.Errorf("%s：认成了 %s，英文不同", key, code)
		}
	}
	if recognized < 100 {
		t.Fatalf("有参数的模板认出的太少（%d）", recognized)
	}
}

func TestN060LegacyCodeOnlyAcceptsExactMatches(t *testing.T) {
	// 任选一个固定句子和一个带标识参数的模板
	var fixed, tmpl string
	// 按键排序取样：以前直接遍历 map，每次选中的模板不同；选中 checkcliargs.1（「参数 %s 的取值…」）时，
	// 「参数里有空格」那一例填出来的句子恰好是两个参数的 checkcliargs.2 的真句子，被正确认出，断言就偶尔失败（约十几分之一）。
	keys := make([]Key, 0, len(zh))
	for key := range zh {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, key := range keys {
		format := zh[key]
		if !strings.HasPrefix(string(key), "err.") {
			continue
		}
		lit, verbs, ok := splitFormat(format)
		if !ok {
			continue
		}
		if len(verbs) == 0 && fixed == "" && len([]rune(lit[0])) > 8 {
			if _, hit := func() (string, bool) { c, _, h := LegacyCode(lit[0]); return c, h }(); hit {
				fixed = lit[0]
			}
		}
		if len(verbs) == 1 && verbs[0] == 's' && tmpl == "" && countHan(strings.Join(lit, "")) >= 4 && !strings.HasSuffix(format, "%s") && !strings.HasPrefix(format, "%s") {
			// 填「two words」后的句子不能恰好是另一个模板的真句子（两个参数的模板），否则「参数里有空格」那一例的前提不成立
			if _, _, hit := LegacyCode(fmt.Sprintf(format, "abc-1")); hit {
				if _, _, again := LegacyCode(fmt.Sprintf(format, "two words")); !again {
					tmpl = format
				}
			}
		}
	}
	if fixed == "" || tmpl == "" {
		t.Fatalf("找不到样例 fixed=%q tmpl=%q", fixed, tmpl)
	}
	for name, text := range map[string]string{
		"前面多一个字":    "x" + fixed,
		"后面多一个字":    fixed + "x",
		"夹在外部原文里":   "上游说：" + fixed + "（完）",
		"去掉最后一个字":   string([]rune(fixed)[:len([]rune(fixed))-1]),
		"参数里有空格":    fmt.Sprintf(tmpl, "two words"),
		"参数里有汉字":    fmt.Sprintf(tmpl, "标题"),
		"参数为空":      fmt.Sprintf(tmpl, ""),
		"参数里带模板的文字": fmt.Sprintf(tmpl, "a」b"),
		"空串":        "",
	} {
		if code, _, hit := LegacyCode(text); hit {
			t.Errorf("%s：不该认，却认成了 %s：%q", name, code, text)
		}
	}
}

// 同一句话对上多个码、而且英文不一样：说不清是哪个，不认。
func TestN060LegacyCodeRefusesAmbiguousSentences(t *testing.T) {
	byZH := map[string][]Key{}
	for key, format := range zh {
		if !strings.HasPrefix(string(key), "err.") {
			continue
		}
		if lit, verbs, ok := splitFormat(format); ok && len(verbs) == 0 {
			byZH[lit[0]] = append(byZH[lit[0]], key)
		}
	}
	checked := 0
	for text, keys := range byZH {
		if len(keys) < 2 {
			continue
		}
		same := true
		for _, k := range keys[1:] {
			same = same && en[k] == en[keys[0]]
		}
		_, _, hit := LegacyCode(text)
		if hit == !same {
			continue
		}
		checked++
		t.Errorf("%q 对上 %v：英文相同=%v，却 hit=%v", text, keys, same, hit)
	}
	_ = checked
}
