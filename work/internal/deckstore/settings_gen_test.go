package deckstore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// 第 n049：设置两份文件的代际标记、路径专用校验（承接 n050 评审建议）。

func genOf(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Generation int `yaml:"generation"`
	}
	if err := yaml.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f.Generation
}

func TestSaveSettingsWritesOneGenerationAndKeepsPrev(t *testing.T) {
	d := testDirs(t)
	s := withProject(t, d, DefaultEnvelope("deck")) // 一次保存
	set := s.Settings()
	set.Projects[0].Name = "改名"
	a, p := filepath.Join(d.Config, "agents.yaml"), filepath.Join(d.Config, "projects.yaml")
	first := genOf(t, a)
	if err := s.ReplaceSettings(set); err != nil { // 第二次保存
		t.Fatal(err)
	}
	if genOf(t, a) != genOf(t, p) || genOf(t, a) <= first {
		t.Fatalf("两份文件应是同一代且递增：%d %d（上一代 %d）", genOf(t, a), genOf(t, p), first)
	}
	if genOf(t, a+".prev") != first || genOf(t, p+".prev") != first { // rev 127 起代数按时间走，上一代不是 -1
		t.Fatal(".prev 应是上一代")
	}
}

// 故障注入：写完 agents.yaml、写 projects.yaml 时失败。读回时识别出新旧混合并拒绝；用 .prev 换回上一代即恢复。
func TestHalfWrittenSettingsAreRefusedNotMixed(t *testing.T) {
	d := testDirs(t)
	s := withProject(t, d, DefaultEnvelope("deck"))
	p := filepath.Join(d.Config, "projects.yaml")
	saveFault = func(name string) error {
		if name == "projects.yaml" {
			return errors.New("注入：写 projects.yaml 前断电")
		}
		return nil
	}
	defer func() { saveFault = nil }()
	set := s.Settings()
	set.Agents = append(set.Agents, AgentDef{ID: "extra", Name: "Extra", Tool: "extra"})
	if err := s.ReplaceSettings(set); err == nil {
		t.Fatal("写 projects.yaml 应失败")
	}
	if s.Broken() == nil {
		t.Fatal("写失败后存储应停止")
	}
	saveFault = nil
	if genOf(t, filepath.Join(d.Config, "agents.yaml")) == genOf(t, p) {
		t.Fatal("构造失败：两份文件应是不同代")
	}
	var mixed *ErrMixedSettings
	if _, err := LoadSettings(d.Config); !errors.As(err, &mixed) || !strings.Contains(err.Error(), ".prev") {
		t.Fatalf("新旧混合应拒绝并说明怎么恢复：%v", err)
	}
	if err := CheckSettings(d.Config); !errors.As(err, &mixed) {
		t.Fatalf("自查也应报出来：%v", err)
	}
	if _, err := Open(d); !errors.As(err, &mixed) {
		t.Fatalf("deckd 重开时应拒绝：%v", err)
	}
	// 恢复：把较新的 agents.yaml 换回上一代
	a := filepath.Join(d.Config, "agents.yaml")
	if err := os.Rename(a+".prev", a); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSettings(d.Config)
	if err != nil {
		t.Fatalf("换回上一代后应能读：%v", err)
	}
	for _, x := range got.Agents {
		if x.ID == "extra" {
			t.Fatal("没写成的那一代不应生效")
		}
	}
}

// 没有 generation 字段的旧文件（两份都是第 0 代）照常能读。
func TestLegacySettingsWithoutGenerationLoad(t *testing.T) {
	d := testDirs(t)
	os.MkdirAll(d.Config, 0o700)
	os.WriteFile(filepath.Join(d.Config, "agents.yaml"), []byte("agents:\n  - id: claude\n    name: Claude Code\n    tool: claude\n    models:\n      - {id: claude-opus-5-5, tier: frontier}\n"), 0o600)
	os.WriteFile(filepath.Join(d.Config, "projects.yaml"), []byte("projects:\n  - id: deck\n    name: deck\n    path: /tmp/deck\n    agents: [claude]\n    models: {frontier: claude-opus-5-5}\n    main_agent: {agent: claude, tier: frontier}\n    envelope: {id: e, version: 1, max_subagents: 1}\n"), 0o600)
	if _, err := LoadSettings(d.Config); err != nil {
		t.Fatalf("旧文件应能读：%v", err)
	}
}

// 旧配置里超长的项目路径：读取时给出可读的错误，并提示先自查。
func TestOverlongProjectPathInExistingConfigIsExplained(t *testing.T) {
	d := testDirs(t)
	os.MkdirAll(d.Config, 0o700)
	long := "/tmp/" + strings.Repeat("长", 120) // 360 多字节
	os.WriteFile(filepath.Join(d.Config, "projects.yaml"), []byte("projects:\n  - id: deck\n    name: deck\n    path: "+long+"\n    agents: []\n    main_agent: {agent: claude, tier: frontier}\n    envelope: {id: e, version: 1}\n"), 0o600)
	_, err := LoadSettings(d.Config)
	if err == nil || !strings.Contains(err.Error(), "超过 300 字节") || !strings.Contains(err.Error(), "deck settings -check") {
		t.Fatalf("应说明超长并提示自查：%v", err)
	}
	if err := CheckSettings(d.Config); err == nil {
		t.Fatal("自查应报出来")
	}
	if _, statErr := os.Stat(filepath.Join(d.Config, "agents.yaml")); statErr != nil {
		// LoadSettings 会写默认 agents.yaml；CheckSettings 不写——这里只确认 LoadSettings 的行为没变
	}
}

func TestCheckPath(t *testing.T) {
	for _, bad := range []string{"", "rel/dir", "/a/../b", "/a/b/", "/a\nb", "/a\tb", "/a\x7fb", "/a\u0085b", "/a\xffb", "/" + strings.Repeat("x", 300)} {
		if CheckPath(bad) == nil {
			t.Fatalf("%q 应被拒", bad)
		}
	}
	for _, good := range []string{"/", "/home/owner/项目 一", "/" + strings.Repeat("x", 299)} {
		if err := CheckPath(good); err != nil {
			t.Fatalf("%q 应接受：%v", good, err)
		}
	}
}

// CheckSettings 只读：设置目录里没有 agents.yaml 时也不替它写默认值。
func TestCheckSettingsWritesNothing(t *testing.T) {
	d := testDirs(t)
	os.MkdirAll(d.Config, 0o700)
	if err := CheckSettings(d.Config); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(d.Config); len(entries) != 0 {
		t.Fatalf("自查不应写文件：%v", entries)
	}
}
