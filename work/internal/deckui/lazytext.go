package deckui

// lazyText 是一条还没定语言的提示（n060 段 3 返工 2，M1）。
//
// 设置页底部的提示、保存失败的原因、新建项目表单的报错、侧栏起主 agent 的结果这类文字，owner 看到之后可能还要停留几秒甚至一直留着。
// 如果在动作发生的那一刻就调 T 把译文存成字符串，这时 owner 在「通用」页切了语言，画面其余部分换了，这一条仍是旧语言。
// 所以存的是「怎么取文字」：每次画的时候才调用，调用里的 T 按当时的语言取；带的错误也留着原错误，Show 每次按当前语言翻。
// 调用里的 T 仍是普通的 T("键", 参数…)，静态检查（键必须是常量、参数扫描）照常覆盖。
type lazyText func() string

// later 把一个取文字的函数存成 lazyText。函数里用 T 取文字，参数在函数外先取出来（别引用以后会变的状态）。
func later(f func() string) lazyText { return f }

// lit 是不分语言的固定文字（外部原文、已经清洗好的，或测试里直接给的）。
func lit(s string) lazyText { return func() string { return s } }

// String 取此刻语言的文字。nil 是空。
func (l lazyText) String() string {
	if l == nil {
		return ""
	}
	return l()
}

// empty 说明现在没有文字（nil 或者取出来是空串）。
func (l lazyText) empty() bool { return l.String() == "" }

// errNote 是「这个错误」的提示：留着原错误，每次画时按当前语言翻译。
func errNote(err error) lazyText { return func() string { return errLong(err) } }
