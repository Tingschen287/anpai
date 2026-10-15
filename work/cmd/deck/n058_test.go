package main

import (
	"strings"
	"testing"
)

// n058（adr_032）：空闲页的产品名是「安排 Anpai」，窄时只写「Anpai」，不再有「Agent Deck」。
func TestIdleLinesUseAnpaiName(t *testing.T) {
	wide := idleLines(80)
	if wide[0] != "安排 Anpai" || strings.Contains(strings.Join(wide, "\n"), "Agent Deck") {
		t.Fatalf("空闲页标题应是「安排 Anpai」：%q", wide[0])
	}
	if narrow := idleLines(8); narrow[0] != "Anpai" {
		t.Fatalf("窄空闲页标题应是「Anpai」：%q", narrow[0])
	}
}
