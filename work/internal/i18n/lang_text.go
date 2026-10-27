package i18n

// 第 3 段新增的文字：设置里的「界面语言」一项、旧记录标签、设置文件里语言字段不合法时的报错。
// 语言名两套界面里各写各的：中文界面「中文 / English」，英文界面「Chinese / English」，
// 英文界面因此不出现汉字（n060 的「英文界面没有汉字」验收）；产品名以外没有例外。
var langTextZH = map[Key]string{
	"settings.lang.label":               "界面语言",
	"settings.lang.zh":                  "中文",
	"settings.lang.en":                  "English",
	"help.lang":                         "空格或 ⏎ 在中文与 English 之间切换，保存后立刻生效，不用重启",
	"legacy.label":                      "旧记录",
	"err.deckstore.settingsvalidate.17": "界面语言 %s 不认识：只能是 zh 或 en（留空表示没选过）",
}

var langTextEN = map[Key]string{
	"settings.lang.label":               "Language",
	"settings.lang.zh":                  "Chinese",
	"settings.lang.en":                  "English",
	"help.lang":                         "Space or Enter switches between Chinese and English. It applies as soon as it is saved, no restart",
	"legacy.label":                      "Earlier record",
	"err.deckstore.settingsvalidate.17": "UI language %s is not known: use zh or en (leave it empty for no choice)",
}

func init() {
	mergeErr(zh, langTextZH, "zh")
	mergeErr(en, langTextEN, "en")
}
