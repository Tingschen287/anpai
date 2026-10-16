package deckui

// 产品的公开名（n058，adr_032）：原名 Agent Deck 与 upstream agent-deck 撞名，owner 2026-10-15 选定「安排 Anpai」。
// 只改界面上 owner 看得到的名字；命令名 deck/deckd、配置与数据路径、tmux socket 与会话名都不变。
const (
	// BrandName 是完整的产品名。
	BrandName = "安排 Anpai"
	// BrandShort 是窄处用的短名（只有字标）。
	BrandShort = "Anpai"
)

// BrandFor 按可用的显示列数选产品名：放得下完整的「安排 Anpai」（10 列）就用完整的，
// 放不下就只用「Anpai」（5 列）；再窄由调用方的 Fit 截断。取舍规则只有这一条，侧栏标题与空闲页共用。
func BrandFor(cols int) string {
	if cols >= 10 {
		return BrandName
	}
	return BrandShort
}
