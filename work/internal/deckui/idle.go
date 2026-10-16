package deckui

import (
	"fmt"

	"github.com/Tingschen287/terminal-deck/work/internal/layout"
)

// IdleLines 是中间空闲页的文字（第一行是产品名）。中文窄时只写「Anpai」（n058）；英文一律「Anpai」（n060）。
func IdleLines(w int) []string {
	lines := []string{
		BrandFor(w),
		"",
		T("idle.hint"),
		"",
	}
	for _, k := range layout.Keys() {
		lines = append(lines, fmt.Sprintf("%-6s %s", k.Key, k.What))
	}
	return lines
}
