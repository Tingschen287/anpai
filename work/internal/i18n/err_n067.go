package i18n

// n067 新增：projectctl 解释器选择（internal/projectctl/python.go）的错误。
var errN067ZH = map[Key]string{
	"err.projectctl.interp.override.fail": "指定的 Python 解释器 %s 跑不起来：%v",
	"err.projectctl.interp.override.old":  "指定的 Python 解释器 %s 是 %s，projectctl 要 %s 及以上",
	"err.projectctl.interp.notfound.none": "找不到 Python %s 及以上的解释器，projectctl 跑不了：PATH 上没有 python3。请安装一个，或用环境变量 %s 指定解释器的路径",
	"err.projectctl.interp.notfound.seen": "找不到 Python %s 及以上的解释器，projectctl 跑不了：PATH 上探测到的都不行（%s）。请安装一个，或用环境变量 %s 指定解释器的路径",
}

var errN067EN = map[Key]string{
	"err.projectctl.interp.override.fail": "the Python interpreter you set, %s, cannot run: %v",
	"err.projectctl.interp.override.old":  "the Python interpreter you set, %s, is %s, but projectctl needs %s or newer",
	"err.projectctl.interp.notfound.none": "no Python %s or newer found, so projectctl cannot run: there is no python3 on PATH. Install one, or set the environment variable %s to the interpreter's path",
	"err.projectctl.interp.notfound.seen": "no Python %s or newer found, so projectctl cannot run: none of the interpreters found on PATH works (%s). Install one, or set the environment variable %s to the interpreter's path",
}

func init() {
	mergeErr(zh, errN067ZH, "zh")
	mergeErr(en, errN067EN, "en")
}
