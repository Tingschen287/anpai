package deckstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Dirs 是 deck 的配置与状态目录。与旧 Work Deck 的目录完全分开（架构稿 §3）。
type Dirs struct {
	Config string // $XDG_CONFIG_HOME/deck：agents.yaml、projects.yaml、panels.yaml（owner 编辑的设置）
	State  string // $XDG_STATE_HOME/deck：注册表、决策、通知、授权记忆、lease、binding、锁与 socket
}

// DefaultDirs 按 XDG 规则给出目录。
func DefaultDirs() (Dirs, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Dirs{}, err
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	st := os.Getenv("XDG_STATE_HOME")
	if st == "" {
		st = filepath.Join(home, ".local", "state")
	}
	return Dirs{Config: filepath.Join(cfg, "deck"), State: filepath.Join(st, "deck")}, nil
}

// writeAtomic 先写临时文件再 rename，目录与文件都只给本用户。
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	return writeAtomic(path, data)
}

// readJSON 读一份 JSON。文件不存在返回 false；文件坏了报错，不当成空的——空的会把已有授权和名额当成没有。
func readJSON(path string, v any) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("%s 解析不了（%v）；不当成空的打开", path, err)
	}
	return true, nil
}
