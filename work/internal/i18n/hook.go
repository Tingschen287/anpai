package i18n

// 测试钩子。两个都默认是 nil，生产行为不变；只有测试的 TestMain 会设置。
// 具体的检查在 internal/wirecheck，这里只留挂钩的位置，免得生产包反过来依赖测试支持代码。
var (
	// WireHook 看每一段 deckd 写给客户端的响应或通知。kind 是「方法 路径 状态」，doc 是写出去的字节。
	// deckstore 追加通知时也会调它，kind 是「notification」，doc 是落盘的那一行。
	WireHook func(kind string, doc []byte)
	// ArgHook 看每一条带码错误的构造：码和参数。没有线路的包（agentdeck、projectctl 的测试）靠它检查参数。
	ArgHook func(key Key, args []any)
)
