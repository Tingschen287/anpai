package agentdeck

import (
	"context"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"os/exec"
	"strings"
	"time"
)

// ToolInfo 返回一个 CLI 可执行文件的绝对路径与版本（`<binary> --version` 的第一行）。
// 用途：Codex trust 的记住授权以（项目、工具、目录、可执行文件路径、版本）为界，路径或版本变了就重新问 owner
// （架构稿 §14.3，n051）。只读，不碰任何配置。
func ToolInfo(ctx context.Context, binary string) (path, version string, err error) {
	path, err = exec.LookPath(binary)
	if err != nil {
		return "", "", i18n.E("err.agentdeck.toolinfo.1", binary, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "--version").Output() // 与 LookPath 按同一个 PATH 解析
	if err != nil {
		return path, "", i18n.E("err.agentdeck.toolinfo.2", binary, err)
	}
	version, _, _ = strings.Cut(strings.TrimSpace(string(out)), "\n")
	return path, strings.TrimSpace(version), nil
}

// ListModels 跑一个 CLI 列模型的子命令（grok models、agy models、opencode models），20 秒为限，返回标准输出（n057：设置界面加模型时
// 给候选）。会用到该 CLI 自己的登录状态与网络，只在 owner 在设置界面里按键时跑；只读，不改任何配置。
func ListModels(ctx context.Context, binary string, args []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, args...).Output()
	return string(out), err
}
