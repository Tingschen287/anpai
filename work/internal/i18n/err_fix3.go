package i18n

// 返工 3 新增的错误码：projectctl 适配层里原本直接写中文的错误。
// 中文和改码之前逐字相同，Error() 不变；英文界面按码翻译，只有 stderr 这类外部原文进参数。
var errFix3ZH = map[Key]string{
	"err.projectctl.refused":                "拒绝执行非只读子命令 %q：Work Deck 对 Graph 控制器只读，唯一的写入口是派工前的 Start",
	"err.projectctl.defaultscript.1":        "WORKDECK_PROJECTCTL 指向的文件不可用：%w",
	"err.projectctl.defaultscript.2":        "无法确定用户主目录：%w",
	"err.projectctl.defaultscript.3":        "找不到 %s，请用 WORKDECK_PROJECTCTL 指定：%w",
	"err.projectctl.run.1":                  "projectctl %s 执行失败：%w（stderr: %s）",
	"err.projectctl.run.2":                  "projectctl %s 的输出不是合法 JSON：%w",
	"err.projectctl.run.3":                  "projectctl %s 返回失败：%s",
	"err.projectctl.start.missing":          "开工缺少 %s，不调 projectctl start",
	"err.projectctl.start.node":             "节点",
	"err.projectctl.start.noverify":         "开工前核对不了账本（%v），不开工：开工出岔子时没法对账",
	"err.projectctl.start.exitbad":          "projectctl start 回执说成功，但进程退出失败：%w（stderr: %s）",
	"err.projectctl.start.badreceipt":       "解析 projectctl start 回执失败：%w",
	"err.projectctl.start.mismatch":         "projectctl start 回执对不上：节点 %q attempt %d",
	"err.projectctl.reconcile.noverify":     "%w；开工后也核对不了账本（%v）：开工结果不明，可能已经开工",
	"err.projectctl.reconcile.notstarted":   "%w（账本事件数开工前后都是 %d：确定没开工）",
	"err.projectctl.reconcile.unknown":      "%w；账本事件数从 %d 变成 %d，但状态投影里对不上这次开工：开工结果不明，可能已经开工",
	"err.projectctl.validate.parse":         "解析 validate 输出失败：%w",
	"err.projectctl.next.parse":             "解析 next 输出失败：%w",
	"err.projectctl.verifychain.parse":      "解析 verify-chain 输出失败：%w",
	"err.projectctl.briefstate.parse":       "解析 brief 输出失败：%w",
	"err.projectctl.briefstate.nostateline": "brief 输出里找不到节点 %s 的状态行",
	"err.projectctl.graph.read":             "读不到 %s：%w",
	"err.projectctl.graph.parse":            "解析 %s 失败：%w",
	"err.projectctl.graph.empty":            "%s 里没有任何节点",
	"err.projectctl.status.read":            "读不到 %s：%w",
	"err.projectctl.status.scan":            "读 %s 时出错：%w",
	"err.projectctl.snapshot.chainfail":     "账本体检失败：%w",
	"err.projectctl.snapshot.nodefail":      "节点 %s 状态取不到：%v",
}

var errFix3EN = map[Key]string{
	"err.projectctl.refused":                "refusing to run non-read-only subcommand %q: Work Deck is read-only toward the Graph controller; the only write entry is Start, before dispatch",
	"err.projectctl.defaultscript.1":        "the file WORKDECK_PROJECTCTL points to is not usable: %w",
	"err.projectctl.defaultscript.2":        "cannot determine the user home directory: %w",
	"err.projectctl.defaultscript.3":        "cannot find %s; set WORKDECK_PROJECTCTL: %w",
	"err.projectctl.run.1":                  "projectctl %s failed to run: %w (stderr: %s)",
	"err.projectctl.run.2":                  "projectctl %s printed output that is not valid JSON: %w",
	"err.projectctl.run.3":                  "projectctl %s reported failure: %s",
	"err.projectctl.start.missing":          "start is missing %s; not calling projectctl start",
	"err.projectctl.start.node":             "the node",
	"err.projectctl.start.noverify":         "cannot verify the ledger before starting (%v); not starting, because a failed start could not be reconciled",
	"err.projectctl.start.exitbad":          "projectctl start's receipt says success but the process exited with failure: %w (stderr: %s)",
	"err.projectctl.start.badreceipt":       "cannot parse the projectctl start receipt: %w",
	"err.projectctl.start.mismatch":         "projectctl start's receipt does not match: node %q attempt %d",
	"err.projectctl.reconcile.noverify":     "%w; the ledger also could not be verified after the start (%v): the result is unknown and the node may have started",
	"err.projectctl.reconcile.notstarted":   "%w (the ledger had %d events before and after the start: it did not start)",
	"err.projectctl.reconcile.unknown":      "%w; the ledger event count went from %d to %d, but the status projection does not match this start: the result is unknown and the node may have started",
	"err.projectctl.validate.parse":         "cannot parse the validate output: %w",
	"err.projectctl.next.parse":             "cannot parse the next output: %w",
	"err.projectctl.verifychain.parse":      "cannot parse the verify-chain output: %w",
	"err.projectctl.briefstate.parse":       "cannot parse the brief output: %w",
	"err.projectctl.briefstate.nostateline": "the brief output has no state line for node %s",
	"err.projectctl.graph.read":             "cannot read %s: %w",
	"err.projectctl.graph.parse":            "cannot parse %s: %w",
	"err.projectctl.graph.empty":            "%s has no nodes",
	"err.projectctl.status.read":            "cannot read %s: %w",
	"err.projectctl.status.scan":            "error while reading %s: %w",
	"err.projectctl.snapshot.chainfail":     "ledger check failed: %w",
	"err.projectctl.snapshot.nodefail":      "cannot get the state of node %s: %v",
}

func init() {
	mergeErr(zh, errFix3ZH, "zh")
	mergeErr(en, errFix3EN, "en")
}
