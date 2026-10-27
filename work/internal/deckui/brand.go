package deckui

import "github.com/Tingschen287/terminal-deck/work/internal/i18n"

// 产品的公开名（n058，adr_032）：原名 Agent Deck 与 upstream agent-deck 撞名，owner 2026-10-15 选定「安排 Anpai」。
// 只改界面上 owner 看得到的名字；命令名 deck/deckd、配置与数据路径、tmux socket 与会话名都不变。
// 中文界面放得下 10 列就写「安排 Anpai」，否则写「Anpai」。英文界面一律「Anpai」（n060）。

// BrandShort 是窄处用的短名，也是英文界面的唯一产品名。
const BrandShort = "Anpai"

// BrandName 是当前语言的完整产品名。中文是「安排 Anpai」，英文是「Anpai」。
func BrandName() string { return i18n.T("brand.name") }

// BrandFor 按可用的显示列数选产品名。中文：放得下完整名（10 列）就用完整的，否则用短名。
// 英文一律短名。再窄由调用方的 Fit 截断。侧栏标题与空闲页共用。
func BrandFor(cols int) string {
	if i18n.Lang() == i18n.EN {
		return BrandShort
	}
	if cols >= 10 {
		return BrandName()
	}
	return BrandShort
}
