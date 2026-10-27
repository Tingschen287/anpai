package deckui

import "github.com/Tingschen287/terminal-deck/work/internal/i18n"

// T 取当前语言的界面文字（n060：全界面共用 internal/i18n，n059 的设置键名留在原处）。
// 缺当前语言的译文时回退中文；中文也没有时返回键名。
// 包装留着：设置页大量直接写 T("…")，无类型常量仍能传。
// 键在变成 i18n.Key 的地方检查，不在这个函数的入口检查。
func T(key i18n.Key, args ...any) string { return i18n.T(key, args...) }
