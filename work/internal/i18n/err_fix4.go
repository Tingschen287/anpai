package i18n

// 返工 4 新增：deck center 自己画在中间窗格里的提示（cmd/deck/up.go 的 fence、Pause）和 tmux 退出说明的包裹。
// 中文和改码之前逐字相同；写进 center.log 的仍是中文（err.deck.fence.* 的 Error()），画在窗格里的按界面语言。
var errFix4ZH = map[Key]string{
	"err.deck.fence.unconfirmed": "deckd 的回答没有确认（fenced 不是 true）",
	"err.deck.fence.probefail":   "探测 deckd 单实例锁失败：%v（%v）",
	"err.deck.fence.lockheld":    "deckd 的单实例锁还被持有（可能正在关闭、手上还有请求）（%v）",
	"err.deck.fence.notup":       "deckd 还没起来（%s 不存在）（%v）",
	"err.deck.center.attachexit": "%w（%s）",
	"center.fence.waiting":       "等 deckd 确认没有进行中的操作再重连中间窗格…（%s）",
	"center.pause.hint":          "检查 agent-deck 的 tmux server（%s）后按回车重试，输入 q 回车退出。",
}

var errFix4EN = map[Key]string{
	"err.deck.fence.unconfirmed": "deckd's answer did not confirm (fenced is not true)",
	"err.deck.fence.probefail":   "could not probe deckd's single-instance lock: %v (%v)",
	"err.deck.fence.lockheld":    "deckd's single-instance lock is still held (it may be shutting down with requests in flight) (%v)",
	"err.deck.fence.notup":       "deckd is not up yet (%s does not exist) (%v)",
	"err.deck.center.attachexit": "%w (%s)",
	"center.fence.waiting":       "Waiting for deckd to confirm no operation is in progress before reconnecting the center pane… (%s)",
	"center.pause.hint":          "Check the agent-deck tmux server (%s), then press Enter to retry, or type q and press Enter to quit.",
}

func init() {
	mergeErr(zh, errFix4ZH, "zh")
	mergeErr(en, errFix4EN, "en")
}
