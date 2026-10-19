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
	if code == EN {
		lang.Store(EN)
		return
	}
	lang.Store(ZH)
}

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
		return fmt.Sprintf(s, args...)
	}
	return s
}

// Errorf 用文字表里的格式串构造错误。格式串可以带 %w，这样能包住下层错误。
func Errorf(key Key, args ...any) error {
	return fmt.Errorf(Format(key), args...)
}

// TextZH 返回中文原文。测试用来核对键在不在。
// 名字不叫 ZH：ZH 是语言代码常量。
func TextZH(key Key) (string, bool) { s, ok := zh[key]; return s, ok }

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
