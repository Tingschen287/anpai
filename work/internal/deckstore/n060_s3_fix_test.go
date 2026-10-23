package deckstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 段 3 返工（owner 2026-10-20 定）：首次语言——老用户中文、新装跟系统。
//   - 设置里有 ui_lang：用它；
//   - agents.yaml 在这次启动前就存在、没有 ui_lang（升级的老用户）：中文，文件一个字节都不改；
//   - agents.yaml 不存在（全新安装）：按系统语言，并在第一次创建文件时把它写进 ui_lang（否则第二次启动会被当成老用户）。
// 「这次启动前已存在」的判断点：LoadSettingsFor 里 readSettings（在它写任何东西之前）。

const (
	fixOldAgents   = "generation: 5\nagents:\n  - id: claude\n    name: Claude Code\n    tool: claude\n    binary: claude\n    models:\n      - id: claude-opus-5-5\n        tier: frontier\n"
	fixOldProjects = "generation: 5\nprojects:\n  - id: p1\n    name: P1\n    path: /tmp/p1\n    agents: [claude]\n    models:\n      frontier: [claude-opus-5-5]\n    main_agent: {agent: claude, tier: frontier}\n    envelope: {id: p1-default, version: 1, max_subagents: 3, max_frontier: 0, token_soft_cap: 2000000}\n"
)

func envOf(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func writeOld(t *testing.T, d Dirs, agents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(d.Config, "agents.yaml"), []byte(agents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.Config, "projects.yaml"), []byte(fixOldProjects), 0o600); err != nil {
		t.Fatal(err)
	}
}

// 老文件没有 ui_lang：不管系统语言是什么（英文、中文、没传），界面语言都是「没选过」（deckd 据此用中文），文件字节不变。
func TestN060S3FixOldFileWithoutUILangStaysUntouched(t *testing.T) {
	for _, sys := range []string{i18n.EN, i18n.ZH, "", "fr"} {
		d := s3Dirs(t)
		writeOld(t, d, fixOldAgents)
		st, err := OpenWithLang(d, sys)
		if err != nil {
			t.Fatal(err)
		}
		if st.UILang() != "" {
			t.Fatalf("系统语言 %q：老用户不该有 ui_lang：%q", sys, st.UILang())
		}
		if got := readFile(t, filepath.Join(d.Config, "agents.yaml")); got != fixOldAgents {
			t.Fatalf("系统语言 %q：agents.yaml 被改了：\n%s", sys, got)
		}
		if got := readFile(t, filepath.Join(d.Config, "projects.yaml")); got != fixOldProjects {
			t.Fatalf("系统语言 %q：projects.yaml 被改了：\n%s", sys, got)
		}
		// 界面进程连上 deckd 之前先猜的语言（ResolveUILang）：英文系统环境下也是中文，只读
		for _, env := range []map[string]string{{"LANG": "en_US.UTF-8"}, {"LC_ALL": "C"}, {"LANG": "C.UTF-8"}, {"LANG": "zh_CN.UTF-8"}, {}} {
			if got := ResolveUILang(d.Config, envOf(env)); got != i18n.ZH {
				t.Fatalf("老文件、环境 %v：先猜 %q，应为 zh", env, got)
			}
		}
		if got := readFile(t, filepath.Join(d.Config, "agents.yaml")); got != fixOldAgents {
			t.Fatal("ResolveUILang 改了文件")
		}
	}
}

// 全新安装：按系统语言，并在第一次创建 agents.yaml 时写进 ui_lang；第二次启动（不管那时的环境是什么）仍是同一个语言。
func TestN060S3FixNewInstallRecordsTheSystemLang(t *testing.T) {
	for _, c := range []struct{ first, second string }{
		{i18n.EN, i18n.ZH}, // C.UTF-8 之类算英文：第二次启动时系统换成中文也不变
		{i18n.ZH, i18n.EN},
		{i18n.EN, ""}, // 第二次没人传系统语言（手动起 deckd）
	} {
		d := s3Dirs(t)
		st, err := OpenWithLang(d, c.first)
		if err != nil {
			t.Fatal(err)
		}
		if st.UILang() != c.first {
			t.Fatalf("新装按系统语言 %s，得到 %q", c.first, st.UILang())
		}
		agents := readFile(t, filepath.Join(d.Config, "agents.yaml"))
		if !strings.Contains(agents, "ui_lang: "+c.first+"\n") {
			t.Fatalf("第一次创建就要写 ui_lang: %s：\n%s", c.first, agents)
		}
		if got := ResolveUILang(d.Config, envOf(map[string]string{"LANG": "xx"})); got != c.first {
			t.Fatalf("先猜的语言应读到写下的 %s：%q", c.first, got)
		}
		// 第二次启动：文件已存在，不再按系统语言，字节也不变
		st2, err := OpenWithLang(d, c.second)
		if err != nil {
			t.Fatal(err)
		}
		if st2.UILang() != c.first {
			t.Fatalf("第二次启动（系统 %q）语言变了：%q，应还是 %s", c.second, st2.UILang(), c.first)
		}
		if got := readFile(t, filepath.Join(d.Config, "agents.yaml")); got != agents {
			t.Fatalf("第二次启动改了 agents.yaml：\n%s", got)
		}
	}
}

// 新装时系统语言不认识或没传：不记语言（界面按中文，和以前一样），不报错。
func TestN060S3FixNewInstallWithoutSystemLangRecordsNothing(t *testing.T) {
	for _, sys := range []string{"", "fr", "ZH"} {
		d := s3Dirs(t)
		st, err := OpenWithLang(d, sys)
		if err != nil {
			t.Fatal(err)
		}
		if st.UILang() != "" || strings.Contains(readFile(t, filepath.Join(d.Config, "agents.yaml")), "ui_lang") {
			t.Fatalf("系统语言 %q 不该写 ui_lang", sys)
		}
	}
	// Open 就是不带系统语言的 OpenWithLang
	d := s3Dirs(t)
	if _, err := Open(d); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFile(t, filepath.Join(d.Config, "agents.yaml")), "ui_lang") {
		t.Fatal("Open 不带系统语言，不该写 ui_lang")
	}
}

// 设置里有 ui_lang：以它为准，不管系统语言；文件不改。
func TestN060S3FixSavedUILangWins(t *testing.T) {
	for _, c := range []struct{ saved, sys string }{{i18n.ZH, i18n.EN}, {i18n.EN, i18n.ZH}, {i18n.EN, i18n.EN}} {
		d := s3Dirs(t)
		agents := fixOldAgents + "ui_lang: " + c.saved + "\n"
		writeOld(t, d, agents)
		st, err := OpenWithLang(d, c.sys)
		if err != nil {
			t.Fatal(err)
		}
		if st.UILang() != c.saved {
			t.Fatalf("保存 %s、系统 %s：%q", c.saved, c.sys, st.UILang())
		}
		if got := readFile(t, filepath.Join(d.Config, "agents.yaml")); got != agents {
			t.Fatalf("agents.yaml 被改了：\n%s", got)
		}
		if got := ResolveUILang(d.Config, envOf(map[string]string{"LANG": "en_US.UTF-8"})); got != c.saved {
			t.Fatalf("先猜的语言应是保存的 %s：%q", c.saved, got)
		}
	}
}

// 先猜的语言（只读）：文件不存在按环境（C、POSIX、C.UTF-8、空都是英文）；文件读不懂或 ui_lang 不认识：中文（deckd 也不会用这份文件）。
func TestN060S3FixResolveUILangShapes(t *testing.T) {
	d := s3Dirs(t)
	for env, want := range map[string]string{"zh_CN.UTF-8": i18n.ZH, "en_US.UTF-8": i18n.EN, "C": i18n.EN, "POSIX": i18n.EN, "C.UTF-8": i18n.EN, "": i18n.EN} {
		if got := ResolveUILang(d.Config, envOf(map[string]string{"LANG": env})); got != want {
			t.Errorf("没有文件、LANG=%q：%q，应为 %q", env, got, want)
		}
	}
	if entries, _ := os.ReadDir(d.Config); len(entries) != 0 {
		t.Fatalf("ResolveUILang 不该写任何东西：%v", entries)
	}
	for _, bad := range []string{"agents: [", "generation: 1\nui_lang: fr\n", ""} {
		if err := os.WriteFile(filepath.Join(d.Config, "agents.yaml"), []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := ResolveUILang(d.Config, envOf(map[string]string{"LANG": "en_US.UTF-8"})); got != i18n.ZH {
			t.Errorf("文件 %q：%q，应为 zh", bad, got)
		}
	}
	// 只读检查（deck settings -check）不创建文件
	d2 := s3Dirs(t)
	if err := CheckSettings(d2.Config); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(d2.Config); len(entries) != 0 {
		t.Fatalf("CheckSettings 不该创建文件：%v", entries)
	}
}

// 运行中 agents.yaml 被删，下一次保存会重读并重新创建它：这是第二条创建路径，同样按全新安装处理、记下语言。
func TestN060S3FixRecreatedAgentsFileRecordsTheLang(t *testing.T) {
	d := s3Dirs(t)
	st, err := OpenWithLang(d, i18n.EN)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"agents.yaml", "projects.yaml"} {
		os.Remove(filepath.Join(d.Config, n))
	}
	set := st.Settings()
	if err := st.ReplaceSettingsAt(set, set.Generation); err == nil {
		t.Fatal("手改（这里是删文件）之后的第一次保存应被拒，读入新的")
	}
	if got := readFile(t, filepath.Join(d.Config, "agents.yaml")); !strings.Contains(got, "ui_lang: en") {
		t.Fatalf("重新创建的 agents.yaml 要记下语言：\n%s", got)
	}
	if st.UILang() != i18n.EN {
		t.Fatalf("重读后的语言：%q", st.UILang())
	}
}
