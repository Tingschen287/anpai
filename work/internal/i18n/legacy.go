package i18n

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// 旧数据里没有码的系统文字（n060 段 3，第 2 段复审 4 的必改 3）。
//
// 码是第 2 段才加到通知、决定摘要上的；更早写进磁盘的记录只有中文原文，英文界面读到会原样显示中文。
// 读旧数据时（deckstore.Open）用 LegacyCode 把「码表里的固定句子」认出来，补上 code/args，原文不改：
//   - 只认逐字匹配：整句必须恰好等于某个码的中文模板填入参数后的样子，不做模糊替换、不做子串替换；
//   - 模板里没有参数的句子，整句相等就认；
//   - 模板里有参数的，只在参数能确定反解时才认：模板只含 %s、%d，每个参数前后有固定文字隔开，
//     整句只能按一种方式匹配，且每个 %s 取到的是一个不含空白的标识（编号、工具名、数字这类），%d 取到的是整数。
//     参数是标题、报错、摘要这类可能混有外部原文的文字时，取到的片段不是标识，这里不认；
//   - 同一句话对上多个码、而且这些码的英文译文不一样时，说不清是哪个，不认。
//
// 不认的旧记录由界面按兼容显示策略处理（panels：英文下整条显示原文，前面加「旧记录」标签）。
// 限制：旧中文「[已打码]」与外部原文里字面写的一样，无法区分，这一条不处理。

const (
	// LegacyLabelKey 是「旧记录」标签的文字键。英文界面显示没有码、含中文的旧记录时加在前面；中文界面不加。
	LegacyLabelKey = Key("legacy.label")
)

type legacyTemplate struct {
	key   Key
	lit   []string // 固定文字，len(verbs)+1 段
	verbs []byte   // 's' 或 'd'
	re    *regexp.Regexp
}

var (
	legacyOnce  sync.Once
	legacyExact map[string][]Key // 没有参数的模板：中文整句 → 码
	legacyTmpl  []legacyTemplate // 有参数的模板
)

// idToken 是 %s 参数在反解时允许的样子：编号、工具名、模型名、路径这类不含空白的标识（可以以 / ~ . 开头）。
const idToken = `[A-Za-z0-9/~._][A-Za-z0-9._/:@+~\-]{0,255}`

func buildLegacy() {
	legacyExact = map[string][]Key{}
	for key, format := range zh {
		if !strings.HasPrefix(string(key), "err.") {
			continue
		}
		lit, verbs, ok := splitFormat(format)
		if !ok {
			continue
		}
		if len(verbs) == 0 {
			legacyExact[lit[0]] = append(legacyExact[lit[0]], key)
			continue
		}
		// 固定文字太少的模板（例如只有一个 %s）会把任何标识都认成它，不收
		if han := countHan(strings.Join(lit, "")); han < 2 {
			continue
		}
		var re strings.Builder
		re.WriteString("^")
		for i, l := range lit {
			re.WriteString(regexp.QuoteMeta(l))
			if i < len(verbs) {
				if verbs[i] == 'd' {
					re.WriteString(`(-?[0-9]+)`)
				} else {
					re.WriteString("(" + idToken + ")")
				}
			}
		}
		re.WriteString("$")
		c, err := regexp.Compile(re.String())
		if err != nil {
			continue
		}
		legacyTmpl = append(legacyTmpl, legacyTemplate{key: key, lit: lit, verbs: verbs, re: c})
	}
	sort.Slice(legacyTmpl, func(i, j int) bool { return legacyTmpl[i].key < legacyTmpl[j].key })
}

func countHan(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x4e00 && r <= 0x9fff {
			n++
		}
	}
	return n
}

// splitFormat 把中文模板按 %s、%d 切成固定文字和参数；含别的参数种类（%v、%w、%q、%x、带宽度的…）的模板不收。
// %% 是一个百分号。
func splitFormat(f string) (lit []string, verbs []byte, ok bool) {
	var cur strings.Builder
	for i := 0; i < len(f); i++ {
		if f[i] != '%' {
			cur.WriteByte(f[i])
			continue
		}
		if i+1 >= len(f) {
			return nil, nil, false
		}
		switch f[i+1] {
		case '%':
			cur.WriteByte('%')
		case 's', 'd':
			lit = append(lit, cur.String())
			cur.Reset()
			verbs = append(verbs, f[i+1])
		default:
			return nil, nil, false
		}
		i++
	}
	lit = append(lit, cur.String())
	return lit, verbs, true
}

// LegacyCode 在旧数据的中文原文里认出码表里的固定句子，返回码和参数（%d 的参数是 int64，%s 的是 string）。
// 认不出（没有逐字匹配、参数不是标识、对上多个译文不同的码）返回 ok=false。text 不会被改。
func LegacyCode(text string) (code string, args []any, ok bool) {
	if text == "" {
		return "", nil, false
	}
	legacyOnce.Do(buildLegacy)
	type hit struct {
		key  Key
		args []any
	}
	var hits []hit
	for _, k := range legacyExact[text] {
		hits = append(hits, hit{key: k})
	}
	for _, t := range legacyTmpl {
		if !strings.HasPrefix(text, t.lit[0]) || !strings.HasSuffix(text, t.lit[len(t.lit)-1]) {
			continue
		}
		m := t.re.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		as := make([]any, len(t.verbs))
		good := true
		for i, v := range t.verbs {
			if v == 'd' {
				n, err := strconv.ParseInt(m[i+1], 10, 64)
				if err != nil {
					good = false
					break
				}
				as[i] = n
			} else {
				as[i] = m[i+1]
			}
		}
		if good {
			hits = append(hits, hit{key: t.key, args: as})
		}
	}
	if len(hits) == 0 {
		return "", nil, false
	}
	// 对上多个码：译文和参数个数都一样才算同一句话，取字典序最小的
	first := hits[0]
	for _, h := range hits[1:] {
		if en[h.key] != en[first.key] || len(h.args) != len(first.args) {
			return "", nil, false
		}
		if h.key < first.key {
			first = h
		}
	}
	if first.args == nil {
		first.args = []any{}
	}
	return string(first.key), first.args, true
}
