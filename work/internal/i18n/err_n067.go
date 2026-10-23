package i18n

// n067 新增：projectctl 解释器选择（internal/projectctl/python.go）的错误，以及 macOS 读进程命令行（internal/agentdeck/procs_darwin.go）的诊断。
var errN067ZH = map[Key]string{
	"err.projectctl.interp.override.fail": "指定的 Python 解释器 %s 跑不起来：%v",
	"err.projectctl.interp.override.old":  "指定的 Python 解释器 %s 是 %s，projectctl 要 %s 及以上",
	"err.projectctl.interp.notfound.none": "找不到 Python %s 及以上的解释器，projectctl 跑不了：PATH 上没有 python3。请安装一个，或用环境变量 %s 指定解释器的路径",
	"err.projectctl.interp.notfound.seen": "找不到 Python %s 及以上的解释器，projectctl 跑不了：PATH 上探测到的都不行（%s）。请安装一个，或用环境变量 %s 指定解释器的路径",
	// procargs2（macOS 读进程命令行）：确认不了与自检没过的各个原因。%w 处是 agentdeck 包的哨兵（也是带码错误），
	// 系统 errno 文本、argv 原文作为参数原样带出。
	"err.agentdeck.procargs.unconfirmable": "命令行确认不了",
	"err.agentdeck.procargs.selfcheck":     "procargs2 运行时自检没过",
	"err.agentdeck.procargs.selffail":      "%w：%w：%w",
	"err.agentdeck.procargs.tooshort":      "%w：procargs2 数据太短",
	"err.agentdeck.procargs.argcrange":     "%w：procargs2 里的 argc %d 超出合理范围",
	"err.agentdeck.procargs.nopathend":     "%w：procargs2 里可执行文件的路径没有结尾",
	"err.agentdeck.procargs.shortargs":     "%w：procargs2 只读到 %d 个参数，应有 %d 个（缓冲区被截断，或 argc 偏大）",
	"err.agentdeck.procargs.readfail":      "%w：读 procargs2 失败：%w",
	"err.agentdeck.procargs.self.read":     "读不了本进程自己的 procargs2：%w",
	"err.agentdeck.procargs.self.parse":    "本进程自己的 procargs2 解析失败：%w",
	"err.agentdeck.procargs.self.mismatch": "本进程自己的 procargs2 解析为 %q，而启动时的 os.Args 是 %q",
}

var errN067EN = map[Key]string{
	"err.projectctl.interp.override.fail":  "the Python interpreter you set, %s, cannot run: %v",
	"err.projectctl.interp.override.old":   "the Python interpreter you set, %s, is %s, but projectctl needs %s or newer",
	"err.projectctl.interp.notfound.none":  "no Python %s or newer found, so projectctl cannot run: there is no python3 on PATH. Install one, or set the environment variable %s to the interpreter's path",
	"err.projectctl.interp.notfound.seen":  "no Python %s or newer found, so projectctl cannot run: none of the interpreters found on PATH works (%s). Install one, or set the environment variable %s to the interpreter's path",
	"err.agentdeck.procargs.unconfirmable": "command line cannot be confirmed",
	"err.agentdeck.procargs.selfcheck":     "the procargs2 runtime self-check failed",
	"err.agentdeck.procargs.selffail":      "%w: %w: %w",
	"err.agentdeck.procargs.tooshort":      "%w: the procargs2 data is too short",
	"err.agentdeck.procargs.argcrange":     "%w: argc %d in procargs2 is out of range",
	"err.agentdeck.procargs.nopathend":     "%w: the executable path in procargs2 has no terminator",
	"err.agentdeck.procargs.shortargs":     "%w: procargs2 holds only %d of %d arguments (the buffer was cut short, or argc is too large)",
	"err.agentdeck.procargs.readfail":      "%w: reading procargs2 failed: %w",
	"err.agentdeck.procargs.self.read":     "cannot read this process's own procargs2: %w",
	"err.agentdeck.procargs.self.parse":    "this process's own procargs2 could not be parsed: %w",
	"err.agentdeck.procargs.self.mismatch": "this process's own procargs2 parsed as %q, but os.Args at startup is %q",
}

func init() {
	mergeErr(zh, errN067ZH, "zh")
	mergeErr(en, errN067EN, "en")
}
