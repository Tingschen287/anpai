package i18n

// n071 新增的文字：进度面板的数字缩写、预算标签与「未知」。
// 删掉的旧键：progress.unknown（状态未知 %d，标签与数字现在分开排版）、progress.budget.unknown（整行写死的格式）。
var n071ZH = map[Key]string{
	"progress.unknown.label":   "状态未知",
	"progress.val.unknown":     "未知",
	"progress.budget.worker":   "执行轮次",
	"progress.budget.frontier": "frontier 调用",
	"progress.budget.elapsed":  "耗时",
	"progress.budget.tokens":   "token",
	"num.wan":                  " 万",
	"num.yi":                   " 亿",
	"num.wanyi":                " 万亿",
	"num.k":                    "K",
	"num.m":                    "M",
	"num.b":                    "B",
	"num.t":                    "T",
}

var n071EN = map[Key]string{
	"progress.unknown.label":   "Unknown",
	"progress.val.unknown":     "unknown",
	"progress.budget.worker":   "Worker rounds",
	"progress.budget.frontier": "Frontier calls",
	"progress.budget.elapsed":  "Elapsed",
	"progress.budget.tokens":   "Tokens",
	"num.wan":                  "W",
	"num.yi":                   "Y",
	"num.wanyi":                "WY",
	"num.k":                    "K",
	"num.m":                    "M",
	"num.b":                    "B",
	"num.t":                    "T",
}

func init() {
	mergeErr(zh, n071ZH, "zh")
	mergeErr(en, n071EN, "en")
}
