package deckstore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 第一次保存（projects.yaml 还不存在）只写成 agents.yaml：缺的那份按第 0 代，同样拒绝（n049 评审 1 B3）。
func TestFirstSaveHalfWrittenIsRefused(t *testing.T) {
	d := testDirs(t)
	old, err := LoadSettings(d.Config) // 首次启动：写出第 0 代 agents.yaml，没有 projects.yaml
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d.Config, "projects.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("前提：projects.yaml 还不存在")
	}
	set := old
	set.Agents = append(append([]AgentDef(nil), old.Agents...), AgentDef{ID: "extra", Name: "Extra", Tool: "extra"})
	set.Projects = []ProjectDef{{ID: "new", Name: "New", Path: "/tmp/new", Agents: []string{"extra"}, Envelope: DefaultEnvelope("new")}}
	saveFault = func(name string) error {
		if name == "projects.yaml" {
			return errors.New("注入：写 projects.yaml 前断电")
		}
		return nil
	}
	err = SaveSettings(d.Config, set)
	saveFault = nil
	if err == nil {
		t.Fatal("注入的故障没有触发")
	}
	var mixed *ErrMixedSettings
	got, err := LoadSettings(d.Config)
	if !errors.As(err, &mixed) || !mixed.ProjMissing || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("新的 agents 配上缺失的 projects 是新旧混合，应拒绝：agents=%d projects=%d err=%v", len(got.Agents), len(got.Projects), err)
	}
	if err := CheckSettings(d.Config); !errors.As(err, &mixed) {
		t.Fatalf("自查也应报出来：%v", err)
	}
	// 按提示恢复：换回 agents.yaml.prev（第 0 代）
	a := filepath.Join(d.Config, "agents.yaml")
	if err := os.Rename(a+".prev", a); err != nil {
		t.Fatal(err)
	}
	if got, err = LoadSettings(d.Config); err != nil || len(got.Agents) != len(old.Agents) {
		t.Fatalf("换回上一代后应能读、且没有半代的 agent：%d %v", len(got.Agents), err)
	}
	// 再保存一次能完整写成
	if err := SaveSettings(d.Config, set); err != nil {
		t.Fatal(err)
	}
	if got, err = LoadSettings(d.Config); err != nil || len(got.Projects) != 1 || genOf(t, a) != 1 {
		t.Fatalf("完整保存后应是第 1 代、带新项目：%+v %v", got.Projects, err)
	}
}

// 提示里的另一条恢复路：没有 .prev 的那份是第一次写，删掉即回到默认。
func TestHalfWrittenWithoutPrevRecoversByDeleting(t *testing.T) {
	d := testDirs(t)
	if err := os.MkdirAll(d.Config, 0o700); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(d.Config, "agents.yaml")
	if err := os.WriteFile(a, []byte("generation: 1\nagents: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mixed *ErrMixedSettings
	if _, err := LoadSettings(d.Config); !errors.As(err, &mixed) {
		t.Fatalf("第 1 代 agents 而没有 projects 应拒绝：%v", err)
	}
	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadSettings(d.Config); err != nil || len(got.Agents) != len(DefaultAgents()) {
		t.Fatalf("删掉后应回到默认：%v", err)
	}
}
