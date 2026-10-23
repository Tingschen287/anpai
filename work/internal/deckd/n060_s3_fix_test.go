package deckd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 段 3 返工：首次语言——老用户中文，全新安装才跟系统（deckstore.LoadSettingsFor 的规则，这里验 deckd 的出口）。

func fixDirs(t *testing.T) deckstore.Dirs {
	t.Helper()
	root := t.TempDir()
	d := deckstore.Dirs{Config: filepath.Join(root, "cfg"), State: filepath.Join(root, "state")}
	for _, p := range []string{d.Config, d.State} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func fixServer(t *testing.T, d deckstore.Dirs, sys string) *Server {
	t.Helper()
	st, err := deckstore.OpenWithLang(d, sys)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(Config{Dirs: d, SystemLang: sys}, st, nil, nil, nil, nil)
	t.Cleanup(srv.Close)
	return srv
}

func TestN060S3FixFirstRunRuleAtDeckd(t *testing.T) {
	// 没有文件 + 英文系统：英文，ui_lang: en 已写入；第二次启动（系统变成中文）仍是英文
	d := fixDirs(t)
	if got := fixServer(t, d, i18n.EN).uiLang(); got != i18n.EN {
		t.Fatalf("新装、英文系统：%q", got)
	}
	b, _ := os.ReadFile(filepath.Join(d.Config, "agents.yaml"))
	if !strings.Contains(string(b), "ui_lang: en") {
		t.Fatalf("没写入 ui_lang: en：\n%s", b)
	}
	if got := fixServer(t, d, i18n.ZH).uiLang(); got != i18n.EN {
		t.Fatalf("第二次启动应仍是英文：%q", got)
	}
	// 没有文件 + 中文系统：中文并写入
	d2 := fixDirs(t)
	if got := fixServer(t, d2, i18n.ZH).uiLang(); got != i18n.ZH {
		t.Fatalf("新装、中文系统：%q", got)
	}
	if b, _ := os.ReadFile(filepath.Join(d2.Config, "agents.yaml")); !strings.Contains(string(b), "ui_lang: zh") {
		t.Fatalf("没写入 ui_lang: zh：\n%s", b)
	}
	// 老文件没有 ui_lang + 英文系统：中文，文件字节不变
	d3 := fixDirs(t)
	old := "generation: 3\nagents:\n  - id: claude\n    name: Claude Code\n    tool: claude\n    binary: claude\n    models:\n      - id: claude-opus-5-5\n        tier: frontier\n"
	os.WriteFile(filepath.Join(d3.Config, "agents.yaml"), []byte(old), 0o600)
	os.WriteFile(filepath.Join(d3.Config, "projects.yaml"), []byte("generation: 3\nprojects: []\n"), 0o600)
	if got := fixServer(t, d3, i18n.EN).uiLang(); got != i18n.ZH {
		t.Fatalf("老用户、英文系统：%q，应为中文", got)
	}
	if b, _ := os.ReadFile(filepath.Join(d3.Config, "agents.yaml")); string(b) != old {
		t.Fatalf("老文件被改了：\n%s", b)
	}
	// 有 ui_lang：以它为准
	d4 := fixDirs(t)
	os.WriteFile(filepath.Join(d4.Config, "agents.yaml"), []byte(old+"ui_lang: en\n"), 0o600)
	os.WriteFile(filepath.Join(d4.Config, "projects.yaml"), []byte("generation: 3\nprojects: []\n"), 0o600)
	if got := fixServer(t, d4, i18n.ZH).uiLang(); got != i18n.EN {
		t.Fatalf("有 ui_lang: en、系统中文：%q", got)
	}
}

// deck daemon 把 deck up 传来的系统语言交给 Store（OpenWithLang），否则新装时写不进 ui_lang。
func TestN060S3FixDaemonPassesTheSystemLangToTheStore(t *testing.T) {
	b, err := os.ReadFile("daemon.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "deckstore.OpenWithLang(cfg.Dirs, cfg.SystemLang)") {
		t.Fatal("deckd.Run 必须用 deckstore.OpenWithLang(cfg.Dirs, cfg.SystemLang) 打开状态")
	}
	if strings.Contains(string(b), "deckstore.Open(cfg.Dirs)") {
		t.Fatal("deckd.Run 不能再用不带系统语言的 Open")
	}
}
