package deckd

import (
	"os"
	"path/filepath"
	"testing"
)

// n049 评审 3 B1-A：fence 排在「布局没接上」「存储已停」之前回答：deck center 等的是「没有进行中的操作」，
// 这两种情况不能用 503 打发（否则 deck center 一直等）。只有 owner 能调。
func TestFenceAnswersBeforeViewAndStoreChecks(t *testing.T) {
	f := newFixture(t) // 没接布局：view 为 nil
	if code, body := f.do("owner", "POST", "/v1/view", `{"op":"fence"}`); code != 200 {
		t.Fatalf("布局没接上时 fence 也应回答：%d %s", code, body)
	}
	if code, _ := f.do("main", "POST", "/v1/view", `{"op":"fence"}`); code == 200 {
		t.Fatal("主 agent 不能调 fence")
	}
	if os.Geteuid() == 0 {
		t.Skip("root 不受目录权限限制")
	}
	state := filepath.Join(filepath.Dir(f.srv.cfg.Dirs.Config), "state")
	os.Chmod(state, 0o500)
	defer os.Chmod(state, 0o700)
	f.do("main", "POST", "/v1/decisions", `{"kind":"push","summary":"x"}`) // 写失败，存储停止
	if code, body := f.do("owner", "POST", "/v1/view", `{"op":"fence"}`); code != 200 {
		t.Fatalf("存储停了 fence 也应回答：%d %s", code, body)
	}
	if code, _ := f.do("owner", "POST", "/v1/view", `{"op":"open","session":"x"}`); code != 503 {
		t.Fatalf("其他视图操作照旧 503：%d", code)
	}
}
