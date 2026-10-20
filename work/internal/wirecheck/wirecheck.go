// Package wirecheck 是 n060 段 2 返工 3 的运行时保险：只在测试里用。
//
// 背景：「英文界面残留本地中文」在第 2 段出现过 3 次，静态扫描只能抓到它认识的形状。
// 这里换一个角度，不看源码，看真正发出去的东西：deckd 写给客户端的每一条响应和通知，
// 经 i18n.WireHook 交给本包检查；每一条带码错误的构造，经 i18n.ArgHook 交给本包检查。
// 生产里这两个钩子都是 nil，行为不变。
//
// 规则（「英文规则」，不看进程当前语言，因为 deckd 写出去的东西本来就和界面语言无关）：
//
//   - 兜底中文：一个对象带着码（code、note_code、summary_code、title_code），它的 error、text、note、summary、title 是给旧客户端和模型留的中文原文，
//     英文界面用码和参数翻译，兜底不显示。这一类不查。
//   - 参数 args：字符串是外部原文（stderr、用户写的标题、上游说的话），里面有中文没关系；
//     但如果它含有本项目码表里某句中文模板的原文，就说明本地中文被压成了字符串当参数，命中。
//     嵌套码 {code,args,error} 递归看它的 args，不看 error；列表 {sep,items} 看 items。
//   - 其余的文本字段：不许含中文（Han 字符和中文标点）。确实无法避免的，在 allow.go 里按
//     「方法 路径 · 字段路径 · 产生函数」登记，写理由。不许整包豁免。
//
// 命中不会当场中断（钩子在服务端的 goroutine 里），而是记下来：字段路径、命中的文字、当时正在跑的测试。
// TestMain 里用 Run 包住 m.Run，结束时有命中就以非零退出，并把全部命中打印出来。
package wirecheck

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// Violation 是一次命中。
type Violation struct {
	Test string // 当时正在跑的测试（从调用栈里找）
	Kind string // 哪条响应或通知，或哪个码
	Path string // 字段路径，如 notifications[].args[0]
	Text string // 命中的文字（截断）
	Why  string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s · %s · %s：%q（%s）", v.Test, v.Kind, v.Path, v.Text, v.Why)
}

type state struct {
	mu       sync.Mutex
	hits     []Violation
	docs     int            // 检查过的响应（含 SSE 事件）条数
	notes    int            // 检查过的落盘通知条数
	errs     int            // 检查过的带码错误条数
	tests    map[string]int // 检查次数按测试名
	used     map[string]int // 例外被用了几次
	captures []*capture
}

var st = &state{tests: map[string]int{}, used: map[string]int{}}

type capture struct{ hits []Violation }

// Capture 在 fn 执行期间接管命中：命中不进全局清单，而是返回给调用者。
// 反例测试用它证明钩子有牙：故意造出本地中文，Capture 必须拿到命中。
func Capture(fn func()) []Violation {
	c := &capture{}
	st.mu.Lock()
	st.captures = append(st.captures, c)
	st.mu.Unlock()
	defer func() {
		st.mu.Lock()
		for i, x := range st.captures {
			if x == c {
				st.captures = append(st.captures[:i], st.captures[i+1:]...)
				break
			}
		}
		st.mu.Unlock()
	}()
	fn()
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]Violation(nil), c.hits...)
}

// Stats 是本次运行已经检查过的数量。
type Stats struct {
	Docs, Notes, Errs int
	Tests             int
	Violations        int
	AllowUsed         map[string]int
}

// Snapshot 返回当前统计。
func Snapshot() Stats {
	st.mu.Lock()
	defer st.mu.Unlock()
	used := map[string]int{}
	for k, v := range st.used {
		used[k] = v
	}
	return Stats{Docs: st.docs, Notes: st.notes, Errs: st.errs, Tests: len(st.tests), Violations: len(st.hits), AllowUsed: used}
}

// Hits 返回全局清单里的命中。
func Hits() []Violation {
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]Violation(nil), st.hits...)
}

func record(kind, path, text, why string) {
	test := cheapTest()
	if test == "" {
		test = currentTests() // 命中很少，这时才值得 dump 全部 goroutine 找测试名
	}
	v := Violation{Test: test, Kind: kind, Path: path, Text: clip(text, 60), Why: why}
	st.mu.Lock()
	defer st.mu.Unlock()
	if n := len(st.captures); n > 0 {
		st.captures[n-1].hits = append(st.captures[n-1].hits, v)
		return
	}
	st.hits = append(st.hits, v)
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ---------------- 中文判断 ----------------

// HasHan 与第 1 段检查器同一口径：汉字、中文标点（U+3000–303F）、全角形式（U+FF00–FFEF）。
func HasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF) {
			return true
		}
	}
	return false
}

var verbRe = regexp.MustCompile(`%[-+# 0]*[0-9]*(\.[0-9]+)?[a-zA-Z]`)

var (
	fragOnce sync.Once
	frags    []string
)

// templateFragments 是码表里每句中文模板在占位符之间的原文片段。
// 只收含 4 个以上汉字的片段：太短的（「的」「个」）在外部文字里随处可见，不能当本地中文的证据。
func templateFragments() []string {
	fragOnce.Do(func() {
		seen := map[string]bool{}
		for _, k := range i18n.Keys() {
			zh, ok := i18n.TextZH(k)
			if !ok {
				continue
			}
			for _, f := range verbRe.Split(strings.ReplaceAll(zh, "%%", "%"), -1) {
				f = strings.TrimSpace(f)
				if hanCount(f) >= 4 && !seen[f] {
					seen[f] = true
					frags = append(frags, f)
				}
			}
		}
		sort.Slice(frags, func(i, j int) bool { return len(frags[i]) > len(frags[j]) })
	})
	return frags
}

func hanCount(s string) int {
	n := 0
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			n++
		}
	}
	return n
}

// StrictArgs 为真（默认）：参数里的字符串只要含中文、又不是登记过的外部原文，就算命中。
// 外部原文有两种登记法：客户端请求体里写的文字自动算；测试夹具里「上游说的话」用 External 登记。
// 设 N060_WIRECHECK_STRICT=0 退回宽松口径（只认长得像码表模板的），只用来对比，不作为门禁。
var StrictArgs = os.Getenv("N060_WIRECHECK_STRICT") != "0"

func argLocal(s string) (string, bool) {
	if StrictArgs && HasHan(s) {
		return "(严格模式)", true
	}
	return LocalLooking(s)
}

// LocalLooking 说明一个字符串是不是「本地写的中文」：含有码表里某句中文模板的原文片段。
// 外部原文（stderr、用户写的标题）不会恰好长得像本项目的句子。
func LocalLooking(s string) (string, bool) {
	if !HasHan(s) {
		return "", false
	}
	for _, f := range templateFragments() {
		if strings.Contains(s, f) {
			return f, true
		}
	}
	return "", false
}

// ---------------- 外部原文 ----------------

var (
	extMu sync.Mutex
	// ext：外部原文 → 登记者（测试名；包初始化时登记的夹具是空串，对全包有效）→ 登记位置。
	ext = map[string]map[string]string{}
)

// External 把一些文字登记为外部原文：测试夹具里「上游说的话」「用户起的名字」这类，不是本项目写的句子。
// 只在测试里用，登记要写在造出这段文字的地方，一眼看得出它是夹具。
//
// 范围（返工 4 收窄）：
//   - 生命周期：在测试里登记的只对这个测试有效（按测试名；Track 的清理也会删掉它）；包初始化（init）时登记的夹具清单对全包有效。
//   - 匹配：响应里的字符串恰好等于其中一条（去掉首尾空白）才放行；参数里含有其中一条（至少 3 个汉字）时，
//     把所有登记的片段从参数里去掉，剩下的部分还有中文（本地写的前缀、后缀）就照样命中，不再整串放行。
//   - 来源：每条记着登记它的测试名和代码位置，命中报告里能看到。
func External(texts ...string) {
	owner := cheapTest()
	_, file, line, _ := runtime.Caller(1)
	at := fmt.Sprintf("%s:%d", filepath.Base(file), line)
	extMu.Lock()
	defer extMu.Unlock()
	for _, s := range texts {
		if s = strings.TrimSpace(s); s != "" {
			if ext[s] == nil {
				ext[s] = map[string]string{}
			}
			ext[s][owner] = at
		}
	}
}

// forgetExternal 删掉某个测试登记的外部原文（Track 的清理调用）。
func forgetExternal(owner string) {
	if owner == "" {
		return
	}
	extMu.Lock()
	defer extMu.Unlock()
	for text, owners := range ext {
		delete(owners, owner)
		if len(owners) == 0 {
			delete(ext, text)
		}
	}
}

// requestBody 收集请求体里的字符串：客户端自己写进请求的文字，是外部原文。
func requestBody(doc []byte) {
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(doc)))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return
	}
	var collect func(any)
	collect = func(x any) {
		switch t := x.(type) {
		case string:
			if HasHan(t) {
				// deckd 在入口把伪造的打码标记换成 U+FFFD 再回显，两种写法都算外部原文（返工 4）
				External(t, i18n.Neutralize(t))
			}
		case []any:
			for _, e := range t {
				collect(e)
			}
		case map[string]any:
			for _, e := range t {
				collect(e)
			}
		}
	}
	collect(v)
}

// isExternal 说明 s 是不是登记过的外部原文（只看对当前测试有效的登记）。
// contains 为假：整句相等才算。为真：s 里含有登记的片段（至少 3 个汉字），并且把这些片段去掉之后不再有中文。
func isExternal(s string, contains bool) bool {
	now := cheapTest()
	extMu.Lock()
	defer extMu.Unlock()
	valid := func(owners map[string]string) bool {
		_, mine := owners[now]
		_, global := owners[""]
		return mine || global
	}
	if owners, ok := ext[strings.TrimSpace(s)]; ok && valid(owners) {
		return true
	}
	if !contains {
		return false
	}
	var frags []string
	for e, owners := range ext {
		if hanCount(e) >= 3 && valid(owners) && strings.Contains(s, e) {
			frags = append(frags, e)
		}
	}
	if len(frags) == 0 {
		return false
	}
	sort.Slice(frags, func(i, j int) bool { return len(frags[i]) > len(frags[j]) })
	rest := s
	for _, f := range frags {
		rest = strings.ReplaceAll(rest, f, "")
	}
	return !HasHan(rest)
}

// ---------------- 响应和通知 ----------------

// fallbackKeys：对象里有右边的码，左边的字段就是兜底中文，不查。
var fallbackKeys = map[string]string{
	"error":   "code",
	"text":    "code",
	"note":    "note_code",
	"summary": "summary_code",
	"title":   "title_code",
	// problems 是一组中文原文，problems_coded 是和它一一对应的码（数组），有码就不查
	"problems": "problems_coded",
}

// argKeys 是装参数列表的字段名。
var argKeys = map[string]bool{"args": true, "note_args": true, "summary_args": true, "title_args": true}

// Doc 检查 deckd 写给客户端的一段字节。kind 是「方法 路径 状态」。
// 可以是一段 JSON，也可以是 SSE 事件（id: / event: / data: 行）。
func Doc(kind string, doc []byte) {
	for _, js := range jsonParts(doc) {
		dec := json.NewDecoder(bytes.NewReader(js))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			continue
		}
		note(false)
		(&checker{kind: kind}).walk(v, "$")
	}
}

// NotificationLine 检查 deckstore 落盘的一行通知（一段 JSON）。
func NotificationLine(line []byte) {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return
	}
	note(true)
	(&checker{kind: "notification"}).walk(v, "$")
}

func note(isNote bool) {
	tests := cheapTest()
	st.mu.Lock()
	defer st.mu.Unlock()
	if isNote {
		st.notes++
	} else {
		st.docs++
	}
	if tests != "" {
		st.tests[tests]++
	}
}

// jsonParts 把一段字节拆成 JSON 文档：普通响应就是它自己，SSE 取每个 data: 行。
func jsonParts(doc []byte) [][]byte {
	d := bytes.TrimSpace(doc)
	if len(d) == 0 {
		return nil
	}
	if d[0] == '{' || d[0] == '[' {
		return [][]byte{d}
	}
	var out [][]byte
	for _, line := range bytes.Split(d, []byte("\n")) {
		if rest, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			if rest = bytes.TrimSpace(rest); len(rest) > 0 && (rest[0] == '{' || rest[0] == '[') {
				out = append(out, rest)
			}
		}
	}
	return out
}

type checker struct{ kind string }

var indexRe = regexp.MustCompile(`\[\d+\]`)

// normPath 把 [3] 写成 []，方便登记例外。
func normPath(p string) string { return indexRe.ReplaceAllString(p, "[]") }

func (c *checker) walk(v any, path string) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child := x[k]
			if codeKey, ok := fallbackKeys[k]; ok {
				if cs, _ := x[codeKey].(string); cs != "" {
					continue // 兜底中文，英文界面用码翻译
				}
				if arr, _ := x[codeKey].([]any); len(arr) > 0 {
					continue
				}
			}
			if argKeys[k] {
				if arr, ok := child.([]any); ok {
					c.args(arr, path+"."+k)
					continue
				}
			}
			c.walk(child, path+"."+k)
		}
	case []any:
		for i, e := range x {
			c.walk(e, fmt.Sprintf("%s[%d]", path, i))
		}
	case string:
		if !HasHan(x) {
			return
		}
		if isExternal(x, false) || c.allowed(path) {
			return
		}
		record(c.kind, normPath(path), x, "文本字段含中文，没有码可翻译，也没登记为外部原文")
	}
}

// args 检查参数列表：字符串是外部原文，但不能长得像本地中文；嵌套码和列表递归。
func (c *checker) args(arr []any, path string) {
	for i, e := range arr {
		p := fmt.Sprintf("%s[%d]", path, i)
		switch x := e.(type) {
		case string:
			if frag, ok := argLocal(x); ok {
				if isExternal(x, true) || c.allowed(p) {
					continue
				}
				record(c.kind, normPath(p), x, "参数里是本地中文（含码表模板片段 "+clip(frag, 16)+"），应该改成嵌套码")
			}
		case map[string]any:
			if _, isNest := x["code"]; isNest {
				if a, ok := x["args"].([]any); ok {
					c.args(a, p+".args")
				}
				continue
			}
			if items, ok := x["items"].([]any); ok {
				c.args(items, p+".items")
				continue
			}
			c.walk(x, p)
		}
	}
}

// Args 检查一条带码错误的参数（i18n.ArgHook）。
// 检查的是它将来上线路的样子（i18n.WireArgs）：带码的错误变成嵌套码、errors.Join 变成列表、
// 没有码的错误和字符串当外部原文，规则和响应里的 args 一样。
func Args(key i18n.Key, args []any) {
	name := callerTest() // 构造错误很多，不能每次都 dump 全部 goroutine；只看当前 goroutine 的调用栈，在测试 goroutine 里造的才记名
	st.mu.Lock()
	st.errs++
	if name != "" {
		st.tests[name]++
	}
	st.mu.Unlock()
	data, err := json.Marshal(i18n.WireArgs(args))
	if err != nil {
		return
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var arr []any
	if dec.Decode(&arr) != nil {
		return
	}
	(&checker{kind: "i18n.E " + string(key)}).args(arr, "args")
}

// ---------------- 当前测试 ----------------

// cur 是 Track 登记的当前测试名（测试顺序执行，同一时刻只有一个）。
var cur atomic.Value

// Track 登记「现在是这个测试在用服务端」：测试夹具的构造函数里调一次。
// 响应和通知是在服务端的 goroutine 里检查的，调用栈上没有测试函数；登记之后统计和命中报告就能直接按测试名归属，
// 不用每条响应都 dump 全部 goroutine（那很慢，会改变测试的时序，把本来碰运气过的清理竞争变成必现）。
// 子测试记父测试的名字。
func Track(t *testing.T) {
	name := t.Name()
	if i := strings.IndexByte(name, '/'); i > 0 {
		name = name[:i]
	}
	cur.Store(name)
	t.Cleanup(func() {
		forgetExternal(name)
		if cur.Load() == name {
			cur.Store("")
		}
	})
}

// cheapTest 给统计用：登记过的测试名，没有就看当前 goroutine 的调用栈（测试 goroutine 里造的），都没有返回空。不 dump 全部 goroutine。
func cheapTest() string {
	if n, _ := cur.Load().(string); n != "" {
		return n
	}
	return callerTest()
}

var callerTestRe = regexp.MustCompile(`\.(Test\w+)(?:\.|$)`)

// callerTest 看当前 goroutine 的调用栈里有没有测试函数。便宜（不 dump 全部 goroutine），在后台 goroutine 里造的错误返回空。
func callerTest() string {
	pcs := make([]uintptr, 48)
	n := runtime.Callers(3, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		if m := callerTestRe.FindStringSubmatch(f.Function); m != nil {
			return m[1]
		}
		if !more {
			return ""
		}
	}
}

var testFrameRe = regexp.MustCompile(`(?m)^(?:\S+/)?[\w.\-]+\.(Test\w+)(?:\.\w+)*\(`)

// currentTests 从全部 goroutine 的调用栈里找正在跑的测试：栈底是 testing.tRunner，它上面一帧是测试函数。
// 测试顺序执行，通常只有一个；有多个时用逗号连起来。
func currentTests() string {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, len(buf)*2)
	}
	var names []string
	for _, block := range strings.Split(string(buf), "\n\n") {
		i := strings.Index(block, "testing.tRunner(")
		if i < 0 {
			continue
		}
		lines := strings.Split(block[:i], "\n")
		// tRunner 上面是「文件:行」，再上面才是测试函数那一行
		for j := len(lines) - 1; j >= 0; j-- {
			if m := testFrameRe.FindStringSubmatch(lines[j]); m != nil {
				names = append(names, m[1])
				break
			}
			if strings.HasPrefix(lines[j], "\t") || strings.TrimSpace(lines[j]) == "" {
				continue
			}
			break
		}
	}
	if len(names) == 0 {
		return "(无测试)"
	}
	sort.Strings(names)
	return strings.Join(uniq(names), ",")
}

func uniq(s []string) []string {
	var out []string
	for i, x := range s {
		if i == 0 || x != s[i-1] {
			out = append(out, x)
		}
	}
	return out
}
