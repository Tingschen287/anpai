package main

import (
	"os"
	"path/filepath"
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

// n058（owner 试用：重开布局后空闲页还是旧名字）：二进制被换成新文件（写新文件再改名）时，空闲页认得出来、换成新的。
func TestIdleNoticesReplacedBinary(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "deck")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	started, _ := os.Stat(exe)
	// 跑着的进程一直占着自己的二进制，旧文件的 inode 不会被释放、不会被新文件复用；这里打开着它来模拟
	hold, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()
	if exeReplaced(exe, started) {
		t.Fatal("没换过不算换了")
	}
	os.Chtimes(exe, started.ModTime(), started.ModTime()) // 同一个文件改时间也不算
	if exeReplaced(exe, started) {
		t.Fatal("同一个文件不算换了")
	}
	if exeReplaced("", started) || exeReplaced(exe, nil) {
		t.Fatal("取不到路径或启动时的文件时不换")
	}
	// 没写完、不可执行的新文件不换
	os.WriteFile(exe+".new", []byte("half"), 0o644)
	os.Rename(exe+".new", exe)
	if exeReplaced(exe, started) {
		t.Fatal("不可执行的不换")
	}
	os.WriteFile(exe+".new", []byte("new"), 0o755)
	os.Rename(exe+".new", exe)
	if !exeReplaced(exe, started) {
		t.Fatal("换成新的可执行文件应认出来")
	}
	os.Remove(exe)
	if exeReplaced(exe, started) {
		t.Fatal("文件没了不换")
	}
}
