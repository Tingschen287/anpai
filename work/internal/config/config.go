// Package config 读 Work Deck 的运行配置。
//
// 这里有一条硬规矩：**token 不落盘**。上游 headless 服务的 bearer token 只从
// 环境变量读，配置文件里连这个字段都没有，省得哪天有人顺手写进去再提交上去。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Tingschen287/terminal-deck/work/internal/dispatch"
)

// Config 是 Work Deck 派工要用到的全部设置。
type Config struct {
	// upstream agent-deck 可执行文件路径。
	UpstreamBinary string `json:"upstream_binary"`
	// 隔离用的 profile 名。必须显式写，不给默认值。
	Profile string `json:"profile"`
	// headless 服务地址，例如 127.0.0.1:8420。留空表示不看会话列表，只派工。
	UpstreamAddr string `json:"upstream_addr"`
	// 模型档位表。供应商随时换模型，所以这是配置而不是代码。
	Models []ModelEntry `json:"models"`
	// 派工时传给上游的分组名。
	Group string `json:"group"`
}

// ModelEntry 是档位表里的一行。
type ModelEntry struct {
	ID    string `json:"id"`
	Tool  string `json:"tool"`
	Class string `json:"route_class"`
}

// TokenEnv 是存放上游 token 的环境变量名。
const TokenEnv = "WORKDECK_AGENTDECK_TOKEN"

// Token 从环境变量取 token。取不到就是空，调用方据此决定还能不能读会话列表。
func Token() string { return os.Getenv(TokenEnv) }

// DefaultPath 返回默认配置路径。
func DefaultPath() (string, error) {
	if p := os.Getenv("WORKDECK_CONFIG"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "work-deck", "config.json"), nil
}

// Load 读配置。文件不存在时返回一个明确说明「没有配置」的错误，
// 而不是一份悄悄用着默认值的配置——派工这种事不适合猜。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("没有配置文件 %s：派工需要先写好上游路径、profile 和模型档位表", path)
		}
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("配置 %s 解析失败：%w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("配置 %s 不完整：%w", path, err)
	}
	return &c, nil
}

// Validate 检查配置说不说得清楚。
func (c *Config) Validate() error {
	switch {
	case c.UpstreamBinary == "":
		return fmt.Errorf("没写 upstream_binary")
	case c.Profile == "":
		return fmt.Errorf("没写 profile：留空会落到上游默认 profile，可能覆盖真实会话")
	case len(c.Models) == 0:
		return fmt.Errorf("模型档位表是空的：没有档位表就没法确认模型档位，派工一律会被拒绝")
	}
	for i, m := range c.Models {
		if m.ID == "" || m.Tool == "" || m.Class == "" {
			return fmt.Errorf("模型档位表第 %d 行没写全（要有 id、tool、route_class）", i+1)
		}
	}
	return nil
}

// RouteTable 把配置里的档位表变成派工器能用的形式。
func (c *Config) RouteTable() (*dispatch.RouteTable, error) {
	models := make([]dispatch.Model, 0, len(c.Models))
	for _, m := range c.Models {
		models = append(models, dispatch.Model{
			ID: m.ID, Tool: m.Tool, Class: dispatch.RouteClass(m.Class),
		})
	}
	return dispatch.NewRouteTable(models)
}
