// Package i18n 是安排 Anpai 界面共用的文字表（n060）。
//
// 两套语言：zh（默认）与 en。SetLang 只改本进程，不写盘。
// 设置里的「界面语言」、持久保存、第一次跟系统语言是第 3 段，这里不做。
//
// T 取当前语言的文字，有参数时按 fmt 填。当前语言没有这条时回退中文，不把键名显示出来。
// 中文也没有时返回键名，方便测试发现漏网。
// 带 %w 的错误用 Errorf，不要用 T：T 走 fmt.Sprintf，包不住下层错误。
//
// Key 是键的类型。无类型字符串常量可以直接当 Key 用。
// 包外要造出一个新的 Key，只允许检查器逐个放过的显式转换。
// +、+=、切片，以及转成其它含有 Key 的类型，都会被检查器拒绝。
//
// 加一条文字：zh.go 与 en.go 用同一个键，占位符的个数和种类（%s、%d、%q、%w…）要一致。
// 键名小写、用点分开，例如 side.status.running。
package i18n

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
)

// Key 是文字表里的键。底层是 string，所以无类型字符串常量能直接传进来。
type Key string

const (
	// ZH 是中文。默认语言。
	ZH = "zh"
	// EN 是英文。界面上的产品名一律写 Anpai，不写「安排」。
	EN = "en"
)

var lang atomic.Value

// langSeq 是语言代次：本进程的界面语言每真正变一次加一（SetLang 设成同一个语言不算）。
// 向 deckd 发请求的客户端发出前记下它，回来时代次变了，说明发出之后本进程已经换过语言（例如设置里刚存上新语言），
// 这个响应带的语言头是发出那一刻的旧状态，不能再拿来覆盖（段 3 返工 2，M2；见 deckclient.Client.Do）。
var (
	langMu  sync.Mutex
	langSeq atomic.Uint64
)

// 缺键记录默认关。depth 用原子读，生产路径上 T 只多一次加载。
// 真正追加时再拿 missMu。测试用 RecordMisses 打开。
var (
	missDepth atomic.Int32
	missMu    sync.Mutex
	missKeys  []Key
)

func init() { lang.Store(ZH) }

// RecordMisses 开始记下 T、Format、Errorf 收到、并且中文表里也没有的键。
// stop 结束这一层记录，返回这段时间里的缺键（按出现顺序，同一个键可以出现多次）。
// stop 只生效一次。默认关闭；关掉时缺译文仍回退中文，中文也没有仍返回键名。
func RecordMisses() (stop func() []Key) {
	missMu.Lock()
	missDepth.Add(1)
	start := len(missKeys)
	missMu.Unlock()
	var once sync.Once
	return func() []Key {
		var out []Key
		once.Do(func() {
			missMu.Lock()
			defer missMu.Unlock()
			if start > len(missKeys) {
				start = len(missKeys)
			}
			out = append([]Key{}, missKeys[start:]...)
			if missDepth.Add(-1) <= 0 {
				missDepth.Store(0)
				missKeys = nil
			}
		})
		return out
	}
}

func noteMiss(key Key) {
	if missDepth.Load() == 0 {
		return
	}
	if _, ok := zh[key]; ok {
		return
	}
	missMu.Lock()
	if missDepth.Load() > 0 {
		missKeys = append(missKeys, key)
	}
	missMu.Unlock()
}

// SetLang 切换本进程的界面语言。只认 zh 与 en，其他值都回到中文。
func SetLang(code string) {
	next := ZH
	if code == EN {
		next = EN
	}
	langMu.Lock()
	defer langMu.Unlock()
	if lang.Load().(string) == next {
		return
	}
	lang.Store(next)
	langSeq.Add(1)
}

// SetLangIf 是条件更新：只有语言代次仍等于 seq（调用方发出请求时记下的）、且 code 是合法的另一种语言时才切换，返回是否切换了。
// 「检查代次没变」和「设置新语言」在同一把锁里做完，不会在两步之间被另一个 goroutine 的 SetLang 插进来（段 3 返工 3，复审 2 的建议）。
func SetLangIf(code string, seq uint64) bool {
	if !ValidLang(code) {
		return false
	}
	langMu.Lock()
	defer langMu.Unlock()
	if langSeq.Load() != seq || lang.Load().(string) == code {
		return false
	}
	lang.Store(code)
	langSeq.Add(1)
	return true
}

// LangSeq 返回语言代次（见 langSeq）。
func LangSeq() uint64 { return langSeq.Load() }

// Lang 返回当前语言代码（zh 或 en）。
func Lang() string { return lang.Load().(string) }

func lookup(key Key) (string, bool) {
	if Lang() == EN {
		if s, ok := en[key]; ok {
			return s, true
		}
	}
	s, ok := zh[key]
	if !ok {
		noteMiss(key)
	}
	return s, ok
}

// Format 返回当前语言的格式串，不填参数。缺译文时回退中文；中文也没有时返回键名。
func Format(key Key) string {
	if s, ok := lookup(key); ok {
		return s
	}
	return string(key)
}

// T 取一条界面文字。有参数时按 fmt 填。缺译文时回退中文；中文也没有时返回键名，且不把参数填进去。
func T(key Key, args ...any) string {
	s, ok := lookup(key)
	if !ok {
		return string(key)
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, safeArgs(args)...) // 不带码的 error 和兜底值的文字先中和（display_args.go）
	}
	return s
}

// Errorf 用文字表里的格式串构造错误。格式串可以带 %w，这样能包住下层错误。
func Errorf(key Key, args ...any) error {
	return fmt.Errorf(Format(key), safeArgs(args)...)
}

// TextZH 返回中文原文。测试用来核对键在不在。
// 名字不叫 ZH：ZH 是语言代码常量。
func TextZH(key Key) (string, bool) { s, ok := zh[key]; return s, ok }

// MustTextZH 返回中文原文。键不在表里就 panic。
// 给错误的 Error() 和要逐字保持中文的生产路径用：不跟当前语言走。
func MustTextZH(key Key) string {
	s, ok := zh[key]
	if !ok {
		panic("i18n: 缺中文 " + string(key))
	}
	return s
}

// TextEN 返回英文。测试用来核对键在不在。
func TextEN(key Key) (string, bool) { s, ok := en[key]; return s, ok }

// Keys 返回全部键，按字典序。返回 []Key，不返回 []string：
// 键的公开类型就是 Key，调用方拿去对 TextZH 不用再转一次。
// 中英两套的键必须一样。
func Keys() []Key {
	ks := make([]Key, 0, len(zh))
	for k := range zh {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i] < ks[j] })
	return ks
}
