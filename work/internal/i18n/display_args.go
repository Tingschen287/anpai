package i18n

import (
	"fmt"
	"io"
	"reflect"
)

// 外部文字变成显示参数的统一出口（n060 段 3 返工 2，M4）。
//
// 这一类问题出现过三次（第 2 段 B1、复审 4 的转义绕过、段 3 审查 1 的 YAML 解码失败错误）：每次都是外部文字里带着打码标记字符，
// 在某个出口被当成 deck 自己写的真标记，英文界面显示成「[redacted]」、中文显示成「[已打码]」，伪造了打码。
// 每个入口各补一处挡不住下一个入口，所以在文字变成显示用参数的那几个出口统一中和：
//
//   - Wire / WireArgs（wireArg）：线路上的参数；
//   - (*Error).Error()：给 err.Error()、日志、线路 error 字段用的中文原文；
//   - T、Errorf：同一进程里直接把参数填进格式串的显示。
//
// 它们共用 safeArg 和 ErrText 两个函数。规则只有一条：除了「字符串」和「自带码的错误」，
// 其余参数（不带码的 error、fmt.Sprint 兜底的任何值）都当外部原文，填进去之前中和。
//
// 为什么不会误伤真标记（真标记的正常往返）：真标记只由 deckstore.Clean 打码时写出，之后只存在于
//  1. 落盘的字符串字段（决定的摘要与选项、汇报正文、通知正文、会话标题与备注……）；
//  2. 带码错误的字符串参数（deckd 用 deckstore.Arg / ArgText / CleanArgs 收进去的，保留的就是 Clean 的结果），
//     以及中间窗格 tmux 退出说明那样「Clean 过的字符串作参数」；
//  3. 嵌套码对象 Nest.Error / Nest.Args 里的同样的字符串；
//  4. 最外层、不带码的错误的文字（测试里用它装一个 Clean 过的文字，deckclient.Show 把里面的标记翻成当前语言的记号）。
//
// 前三处是 Go 的 string（或 Nest、List 里的 string），safeArg 对字符串、Nest、List 原样放行，真标记随字符串参数的往返不变。
// 第四处是最外层的错误，不是「参数」，统一出口不碰它（deckclient.Show、deckd 的 writeErr 都原样用 err.Error()）。
// 被中和的只有「作为参数的不带码的 error」和「fmt.Sprint 兜底的值」：
//   - 作为参数的不带码的 error 里不会有真标记：带码错误的 Error() 已经把真标记换成了中文记号（revealRedactZH），
//     deckstore.Arg 把不带码的错误收成参数时先 Clean 过——收成字符串，外部原文里的标记字符变成 U+FFFD，再打码；
//     生产代码里没有把 Clean 过的文字装进不带码的 error 再当参数传的写法（grep Clean/Tidy 与 errors.New、fmt.Errorf 同行：无）。
//   - Stringer、结构体、具名字符串这类兜底值不是 Clean 的出口，没有真标记。
// 所以参数里的不带码 error 与兜底值出现标记字符，只可能是外部伪造，一律中和。

// ErrText 是错误的文字，已中和：标记字符换成 U+FFFD。给「错误作为参数」的出口用（wireArg 的不带码 error 一条）；nil 返回空串。
// 最外层、不带码的错误不用它（见上，它可以带真标记）。
func ErrText(err error) string {
	if err == nil {
		return ""
	}
	return Neutralize(err.Error())
}

// neutralErr 包一个不带码的 error：Error() 中和，Unwrap 仍然露出原错误（errors.Is / As 照常）。
type neutralErr struct{ err error }

func (n neutralErr) Error() string { return Neutralize(n.err.Error()) }
func (n neutralErr) Unwrap() error { return n.err }

// neutralFmt 包任意值：按调用方原来的动词和标志格式化，再把结果中和。
type neutralFmt struct{ v any }

func (n neutralFmt) Format(f fmt.State, verb rune) {
	io.WriteString(f, Neutralize(fmt.Sprintf(fmt.FormatString(f, verb), n.v)))
}

// safeArg 返回可以直接交给 fmt 的参数：字符串、数字、布尔、带码错误、Nest 原样；List 逐项处理；
// 不带码的 error 包成 neutralErr；其他任何值包成 neutralFmt。
func safeArg(a any) any {
	switch v := a.(type) {
	case nil, string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, Nest:
		return a
	case List:
		items := make([]any, len(v.Items))
		for i, it := range v.Items {
			items[i] = safeArg(it)
		}
		return List{Sep: v.Sep, Items: items}
	case neutralErr, neutralFmt:
		return a
	case error:
		if codedOf(v) != nil {
			return a // 带码错误自己的 Error() 已经处理过
		}
		return neutralErr{v}
	}
	switch reflect.ValueOf(a).Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return a // 具名的数字（time.Duration 等）：没有外部文字
	}
	return neutralFmt{a}
}

// safeArgs 对每个参数做 safeArg。没有任何参数需要改时返回原切片，不分配。
func safeArgs(args []any) []any {
	var out []any
	for i, a := range args {
		b := safeArg(a)
		if out == nil && !sameArg(a, b) {
			out = append(make([]any, 0, len(args)), args[:i]...)
		}
		if out != nil {
			out = append(out, b)
		}
	}
	if out == nil {
		return args
	}
	return out
}

// sameArg 说明 safeArg 有没有换掉参数（List 总是复制，按不同处理）。
func sameArg(a, b any) bool {
	switch a.(type) {
	case List:
		return false
	}
	switch b.(type) {
	case neutralErr, neutralFmt:
		return false
	}
	return true
}
