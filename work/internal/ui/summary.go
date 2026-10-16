package ui

import "strings"

// summarize 把一段长理由压成一行能看的摘要。
//
// 账本里的退回理由动辄几百字，整段铺在屏幕上会把真正该先看到的东西挤走。
// 这里只留第一个句子，太长再截断，完整内容按 Enter 展开。
func summarize(s string, limit int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if s == "" {
		return ""
	}
	// 在第一个句末停住。中文句号、问号、叹号、分号都算一句结束。
	for i, r := range s {
		switch r {
		case '。', '；', '！', '？':
			s = s[:i+len(string(r))]
			goto trimmed
		}
	}
trimmed:
	// 前缀是模板话的（「已按…」「owner 决定…」）留着，它们恰恰是关键。
	return clip(strings.TrimSpace(s), limit)
}

// actionHint 把「要 owner 做什么」压成一句祈使话。
//
// 账本里这一栏经常写成一整段交代，但屏幕上人只需要知道「轮到我做什么」。
func actionHint(s string, limit int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if s == "" {
		return ""
	}
	// 「无需任何后续动作」这类要原样留着：它说明这条其实不用管。
	for _, idle := range []string{"无需任何后续动作", "无需后续动作", "不需要任何动作"} {
		if strings.HasPrefix(s, idle) {
			return idle
		}
	}
	for i, r := range s {
		switch r {
		case '。', '；', '！', '？':
			s = s[:i+len(string(r))]
			goto done
		}
	}
done:
	return clip(strings.TrimSpace(s), limit)
}

// needsNoAction 判断这条虽然挂着 blocked，其实不用人动手。
//
// 项目里有几个节点是「归档终结」，状态是 blocked 但没人需要为它做任何事。
// 把它们和真正等人拍板的混在一起，等于把待办清单注水。
func needsNoAction(requiredAction string) bool {
	s := strings.TrimSpace(requiredAction)
	if s == "" {
		return false
	}
	for _, idle := range []string{"无需任何后续动作", "无需后续动作", "不需要任何动作"} {
		if strings.HasPrefix(s, idle) {
			return true
		}
	}
	return false
}
