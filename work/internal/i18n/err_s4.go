package i18n

// 段 4 新增的错误码：CLI 发现（会话 PATH）、Codex 模型候选。中文是新写的原文，英文与之逐条对应。
// 命名：err.<包>.<场景>；占位符的个数和种类中英一致。
var errS4ZH = map[Key]string{
	// 会话 PATH（internal/sessionenv）
	"err.sessionenv.noshell":   "找不到会话的 PATH：没有可用的登录 shell（$SHELL=%q）",
	"err.sessionenv.timeout":   "找不到会话的 PATH：%s 在 %s 内没有给出结果（~/.zshrc 之类的启动文件可能卡住了）",
	"err.sessionenv.failed":    "找不到会话的 PATH：运行 %s 失败：%v",
	"err.sessionenv.nooutput":  "找不到会话的 PATH：%s 的输出里没有可用的 PATH",
	"err.sessionenv.toolong":   "找不到会话的 PATH：%s 给出的 PATH 太长，不采用",
	"err.sessionenv.recursion": "没有取会话的 PATH：环境里已有 %s（正在解析中，避免递归）",
	// 按会话 PATH 找 CLI、列模型（internal/agentdeck）
	"err.agentdeck.toolinfo.3":          "在会话的 PATH 里找不到 %s",
	"err.agentdeck.listmodels.toolarge": "%s 列模型的输出超过 8 MiB，不采用",
	"err.agentdeck.listmodels.timeout":  "%s 列模型在 %s 内没有结果",
	// Codex 模型候选（internal/deckd/tools.go）
	"err.deckd.codexmodels.badjson": "codex debug models 的输出不是预期的 JSON：%v",
	"err.deckd.codexmodels.empty":   "codex debug models 没有列出任何可选的模型",
	// 设置里的提示：用了哪份 PATH
	"err.deckd.servergettools.nopath": "没取到会话的 PATH，已改用 deckd 自己的 PATH 找 CLI：%w",
}

var errS4EN = map[Key]string{
	"err.sessionenv.noshell":   "Couldn't get the session PATH: no usable login shell ($SHELL=%q)",
	"err.sessionenv.timeout":   "Couldn't get the session PATH: %s gave no result within %s (a startup file such as ~/.zshrc may be hanging)",
	"err.sessionenv.failed":    "Couldn't get the session PATH: running %s failed: %v",
	"err.sessionenv.nooutput":  "Couldn't get the session PATH: the output of %s has no usable PATH",
	"err.sessionenv.toolong":   "Couldn't get the session PATH: the PATH from %s is too long, not used",
	"err.sessionenv.recursion": "Not reading the session PATH: %s is already set in the environment (probe in progress, avoiding recursion)",

	"err.agentdeck.toolinfo.3":          "cannot find %s on the session PATH",
	"err.agentdeck.listmodels.toolarge": "the output of %s listing models is over 8 MiB, not used",
	"err.agentdeck.listmodels.timeout":  "%s listing models gave no result within %s",

	"err.deckd.codexmodels.badjson": "the output of codex debug models is not the expected JSON: %v",
	"err.deckd.codexmodels.empty":   "codex debug models listed no selectable models",

	"err.deckd.servergettools.nopath": "Couldn't get the session PATH, so deckd's own PATH is used to find the CLIs: %w",
}

func init() {
	mergeErr(zh, errS4ZH, "zh")
	mergeErr(en, errS4EN, "en")
}
