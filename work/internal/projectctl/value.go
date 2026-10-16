package projectctl

import "fmt"

// Value 包着一个可能取不到的值。
//
// 这个类型存在的唯一理由：Work Deck 宁可显示「未知」，也不显示一个看起来合理的数字。
// 取不到的时候必须说出原因，界面直接把原因摆给人看，不静默留空，也不填默认值。
type Value[T any] struct {
	val    T
	known  bool
	reason string
}

// Known 构造一个取到了的值。
func Known[T any](v T) Value[T] {
	return Value[T]{val: v, known: true}
}

// Unknown 构造一个没取到的值，reason 说明为什么没取到。
func Unknown[T any](reason string) Value[T] {
	return Value[T]{reason: reason}
}

// Get 返回值本身以及它是否可用。调用方必须检查第二个返回值。
func (v Value[T]) Get() (T, bool) { return v.val, v.known }

// IsKnown 表示这个值能不能用。
func (v Value[T]) IsKnown() bool { return v.known }

// Reason 说明为什么取不到。值可用时返回空串。
func (v Value[T]) Reason() string { return v.reason }

// String 给界面用：取到了就格式化值，没取到就是「未知」。
func (v Value[T]) String() string {
	if !v.known {
		return "未知"
	}
	return fmt.Sprintf("%v", v.val)
}
