package deckstore

import (
	"errors"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// 面板注册表 panels.yaml（架构稿 §7）：每块面板的命令、区域、是否挂着。窗格里的挂上、拔掉、停放由 n049 的布局做，
// 这里只保存开关状态，供布局读取、由 owner 界面修改。面板跑在布局 server 里就拥有 owner 权限（O7），只装可信面板。

// PanelDef 是一块面板。
type PanelDef struct {
	Name    string `yaml:"name" json:"name"`
	Command string `yaml:"command" json:"command"`
	Region  string `yaml:"region" json:"region"` // right 或 bottom
	On      bool   `yaml:"on" json:"on"`
}

// DefaultPanels 是内置面板。默认挂着「需要你决策」「额度」与下栏通知（owner 确认的原型）。
func DefaultPanels() []PanelDef {
	return []PanelDef{
		{Name: "decisions", Command: "deck panel decisions", Region: "right", On: true},
		{Name: "quota", Command: "deck panel quota", Region: "right", On: true},
		{Name: "progress", Command: "deck panel progress", Region: "right", On: false},
		{Name: "activity", Command: "deck panel activity", Region: "right", On: false},
		{Name: "notifications", Command: "deck panel notifications", Region: "bottom", On: true},
	}
}

func validatePanels(ps []PanelDef) error {
	seen := map[string]bool{}
	for _, p := range ps {
		if !idRe.MatchString(p.Name) || p.Command == "" {
			return i18n.E("err.deckstore.validatepanels.1", p.Name)
		}
		if p.Region != "right" && p.Region != "bottom" {
			return i18n.E("err.deckstore.validatepanels.2", p.Name)
		}
		if seen[p.Name] {
			return i18n.E("err.deckstore.validatepanels.3", p.Name)
		}
		seen[p.Name] = true
	}
	return nil
}

// LoadPanels 读 panels.yaml；不存在就写出默认值。
func LoadPanels(cfgDir string) ([]PanelDef, error) {
	path := filepath.Join(cfgDir, "panels.yaml")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		ps := DefaultPanels()
		return ps, saveYAML(path, struct {
			Panels []PanelDef `yaml:"panels"`
		}{ps})
	}
	if err != nil {
		return nil, err
	}
	var f struct {
		Panels []PanelDef `yaml:"panels"`
	}
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, i18n.E("err.deckstore.loadpanels.1", err)
	}
	return f.Panels, validatePanels(f.Panels)
}

// Panels 返回面板注册表。
func (s *Store) Panels() []PanelDef {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]PanelDef(nil), s.panels...)
}

// SetPanel 挂上或拔掉一块面板（owner 界面调）。
func (s *Store) SetPanel(name string, on bool) error {
	return s.mutate(func() (func() error, []Event, error) {
		next := append([]PanelDef(nil), s.panels...)
		found := false
		for i := range next {
			if next[i].Name == name {
				next[i].On, found = on, true
			}
		}
		if !found {
			return nil, nil, i18n.E("err.deckstore.storesetpanel.1", name)
		}
		s.panels = next
		return func() error {
			return saveYAML(filepath.Join(s.dirs.Config, "panels.yaml"), struct {
				Panels []PanelDef `yaml:"panels"`
			}{next})
		}, []Event{{Kind: "panels"}}, nil
	})
}
