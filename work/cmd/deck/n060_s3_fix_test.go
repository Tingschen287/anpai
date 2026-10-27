package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 段 3 返工：界面进程连上 deckd 之前先猜的语言，遵守首次语言规则：
// 设置里有 ui_lang 用它；agents.yaml 已存在但没有（升级的老用户）用中文，文件不动；文件不存在（全新安装）按环境变量。
func TestN060S3FixUIClientGuessFollowsTheFirstRunRule(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	setEnv := func(lang string) {
		for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANGUAGE"} {
			t.Setenv(k, "")
		}
		t.Setenv("LANG", lang)
	}
	old := "generation: 1\nagents: []\n"
	cases := []struct {
		name  string
		file  *string // nil = 没有 agents.yaml
		env   string
		start string // 先放成的语言：确认结果是这次判断出来的
		want  string
	}{
		{"老文件没有 ui_lang + 英文系统", &old, "en_US.UTF-8", i18n.EN, i18n.ZH},
		{"老文件没有 ui_lang + C.UTF-8", &old, "C.UTF-8", i18n.EN, i18n.ZH},
		{"老文件没有 ui_lang + 中文系统", &old, "zh_CN.UTF-8", i18n.EN, i18n.ZH},
		{"没有文件 + 英文系统", nil, "en_US.UTF-8", i18n.ZH, i18n.EN},
		{"没有文件 + C.UTF-8", nil, "C.UTF-8", i18n.ZH, i18n.EN},
		{"没有文件 + 中文系统", nil, "zh_CN.UTF-8", i18n.EN, i18n.ZH},
		{"ui_lang: en + 中文系统", ptr(old + "ui_lang: en\n"), "zh_CN.UTF-8", i18n.ZH, i18n.EN},
		{"ui_lang: zh + 英文系统", ptr(old + "ui_lang: zh\n"), "en_US.UTF-8", i18n.EN, i18n.ZH},
	}
	for _, c := range cases {
		dirs := shortDirs(t)
		if c.file != nil {
			if err := os.MkdirAll(dirs.Config, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dirs.Config, "agents.yaml"), []byte(*c.file), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		setEnv(c.env)
		i18n.SetLang(c.start)
		uiClient(dirs) // 没有 deckd：保持先猜的
		if i18n.Lang() != c.want {
			t.Errorf("%s：界面语言 %s，应为 %s", c.name, i18n.Lang(), c.want)
		}
		if c.file != nil {
			if b, _ := os.ReadFile(filepath.Join(dirs.Config, "agents.yaml")); string(b) != *c.file {
				t.Errorf("%s：先猜语言不该改文件：\n%s", c.name, b)
			}
		} else if _, err := os.Stat(dirs.Config); err == nil {
			t.Errorf("%s：先猜语言不该创建设置目录", c.name)
		}
	}
}

func ptr(s string) *string { return &s }
