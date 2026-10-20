package i18n

import "strings"

// 界面语言的选择与传播（n060 段 3）。
//
// 选择存在 deck 的设置里（deckstore.Settings.UILang，agents.yaml 的可选字段 ui_lang）。deckd 是唯一的真相：
// 它在每个响应上带 LangHeader，界面进程（侧栏、标签条、面板、设置、中间窗格、空闲页）经 deckclient.FollowLang 的客户端
// 收到就 SetLang，所以改了语言之后，各界面进程在下一次向 deckd 取数时就换过去（侧栏、标签条 1 秒，面板 2 秒）。
// deckd 自己不换语言：给模型、落盘、日志的文字一直是中文。

// LangHeader 是 deckd 在每个响应上带的响应头：此刻的界面语言（zh 或 en）。
const LangHeader = "X-Deck-Lang"

// ValidLang 说明 code 是不是认识的界面语言。空串不算：空表示「没有保存过选择」。
func ValidLang(code string) bool { return code == ZH || code == EN }

// SystemLang 按环境变量判断系统语言：LC_ALL、LC_MESSAGES、LANG 依次取第一个非空值，以 zh 开头（不分大小写）用中文，
// 其余用英文；这三个都是空时再看 LANGUAGE（GNU 的语言优先级列表，取第一项）；全空用英文。
// getenv 由调用方给（生产里是 os.Getenv，测试里是注入的表）。
//
// 取值例子：zh_CN.UTF-8 → zh；en_US.UTF-8 → en；C、POSIX、C.UTF-8 → en（不是中文就是英文）；空 → en。
// 第一个非空值就是答案，不往后找：LC_ALL=C 就是英文，即使 LANG=zh_CN.UTF-8（这是 POSIX 的规则）。
func SystemLang(getenv func(string) string) string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return langOfLocale(v)
		}
	}
	if v := strings.TrimSpace(getenv("LANGUAGE")); v != "" {
		first, _, _ := strings.Cut(v, ":")
		return langOfLocale(strings.TrimSpace(first))
	}
	return EN
}

func langOfLocale(v string) string {
	if strings.HasPrefix(strings.ToLower(v), "zh") {
		return ZH
	}
	return EN
}
