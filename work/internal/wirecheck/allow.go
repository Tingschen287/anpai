package wirecheck

// Exceptions 是运行时保险的例外清单。
//
// 登记键是「方法 路径 · 字段路径 · 产生函数」，每条写理由。不许整包豁免、不许只写路径不写函数。
// 方法写 * 表示任何方法，路径里的 * 表示一段。通知的 Kind 是 notification；构造错误时的参数例外，Kind 写 「i18n.E <码>」。
// 登记了、本包测试里却没有再用到，Run 以失败收场（例外过期），免得清单只增不减。
//
// 能登记的只有两类：
//  1. 外部原文：用户、模型、上游或测试夹具给的文字原样放在字段里，不是本项目写的句子。
//     客户端自己写进请求体的文字（标题、任务、汇报、选项）不用登记，钩子自动当外部原文；
//     夹具里「上游说的话」用 wirecheck.External 在造它的地方登记。
//  2. 只给模型或记账、不给界面显示的中文：模型读的汇报、会话备注、诊断字段。界面不读这些字段，英文界面不会显示它们。
//
// 本项目写的、会出现在英文界面上的句子，必须改成码，不能登记在这里。
var Exceptions = []Exception{
	// ---- 只给模型读 ----
	{Pkg: "deckd", Kind: "GET /v1/reports/wait", Path: "$.reports[].text", Func: "Server.reportText",
		Why: "主 agent 自己等汇报时读的转告正文，给模型读的中文，合同不改；界面不读这个接口"},
	{Pkg: "deckd", Kind: "GET /v1/reports/wait", Path: "$.ack", Func: "Server.getReportsWait",
		Why: "回给模型的确认说明（deck agent wait 打印），界面不读"},
	{Pkg: "deckd", Kind: "POST /v1/agents", Path: "$.next", Func: "Server.runSpawn",
		Why: "主 agent 调 deck agent spawn 时回的下一步提示（202 等 owner 批准），给模型读；owner 看到的是同一条决定的码和摘要"},

	// ---- 诊断字段，界面不读 ----
	{Pkg: "deckd", Kind: "GET /v1/health", Path: "$.broken", Func: "Server.health",
		Why: "存储停止原因，给 curl 和启动脚本看的诊断字段；deckui、panels、cmd/deck 都不读它"},
	{Pkg: "deckd", Kind: "GET /v1/health", Path: "$.activity_error", Func: "Server.health",
		Why: "会话状态读不到的原因（上游 list 的原文），给开发者看的诊断字段；侧栏圆点只用 activity，不读它"},

	// ---- 额度面板不读的字段 ----
	{Pkg: "deckd", Kind: "GET /v1/quota", Path: "$[].reason", Func: "dashquota.Client.Display",
		Why: "额度来源的文字说明；额度面板（panels.QuotaLines、deckui.RunQuota）只画名字、状态、窗口和重置时间，不读 reason"},
	{Pkg: "deckd", Kind: "GET /v1/quota", Path: "$[].source", Func: "dashquota.Client.Display",
		Why: "额度来源的名字（如状态栏缓存）；面板不读 source"},

	// ---- 会话备注：只存中文兜底（返工 1 起的口径），界面不读 ----
	{Pkg: "deckd", Kind: "*", Path: "$.session.note", Func: "Store.LaunchFailed / SetSessionStatus",
		Why: "会话备注是记账用的中文，侧栏、标签条、面板都不读它；给 owner 看的启动说明走通知的码，见 note_code"},
	{Pkg: "deckd", Kind: "*", Path: "$.session.awaiting_owner", Func: "Store.SetAwaitingOwner",
		Why: "等 owner 处理的原因，记账用的中文；界面只看它是否为空（Session.StartPending），不显示内容"},

	// ---- 决定的结果：记账用的中文 ----
	{Pkg: "deckd", Kind: "POST /v1/decisions/*/answer", Path: "$.result", Func: "Store.FinishExecute",
		Why: "决定的执行结果，记账用的中文；决策面板不读 result，只读摘要、选项和推荐"},

	// ---- 构造错误时的参数 ----
	{Pkg: "deckd", Kind: "i18n.E err.deckclient.status", Path: "args[]", Func: "deckclient.Error.Error",
		Why: "客户端把 deckd 回的中文兜底（error 字段）装进 Error() 的中文原文；英文界面走 deckclient.Show，按 code 和 args 翻译，不显示这段兜底"},

	// ---- deckstore 自己的测试 ----
	{Pkg: "deckstore", Kind: "notification", Path: "$.text", Func: "Store.Notify",
		Why: "无码通知的自由文字：生产里没有调用者（只有 NotifyCoded 调它传空文字），只有存储自己的测试用中文夹具喂它"},
}
