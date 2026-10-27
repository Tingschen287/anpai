package deckstore

import (
	"errors"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"os"
	"path/filepath"
	"time"

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

type panelStamp struct {
	mod  time.Time
	size int64
}

// syncPanelsLocked：deckd 运行期间 owner 手改了 panels.yaml，就采用磁盘上的，不拿内存里的旧版本覆盖他的改动。
// 改坏了（解析或校验不过）保持内存里的并返回原因：调用方写盘前必须先看这个错，不然会把手改的文件整份覆盖。调用方持 s.mu。
func (s *Store) syncPanelsLocked() error {
	fi, err := os.Stat(filepath.Join(s.dirs.Config, "panels.yaml"))
	if err != nil {
		return nil // 文件没了：保持内存里的，下次存盘写回
	}
	stamp := panelStamp{fi.ModTime(), fi.Size()}
	if stamp == s.panelsAt {
		return nil
	}
	ps, err := LoadPanels(s.dirs.Config)
	if err != nil {
		return i18n.E("err.deckstore.open.2", err)
	}
	s.panels, s.panelsAt = ps, stamp
	return nil
}

// Panels 返回面板注册表。
func (s *Store) Panels() []PanelDef {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncPanelsLocked() // 手改坏了就还给内存里的那份；SetPanel 会把原因报给 owner
	return append([]PanelDef(nil), s.panels...)
}

// SetPanel 挂上或拔掉一块面板（owner 界面调）。
func (s *Store) SetPanel(name string, on bool) error {
	return s.mutate(func() (func() error, []Event, error) {
		if err := s.syncPanelsLocked(); err != nil {
			return nil, nil, err
		}
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
