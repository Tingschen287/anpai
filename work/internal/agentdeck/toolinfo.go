package agentdeck

import (
	"context"
	"fmt"
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
		return "", "", fmt.Errorf("找不到 %s：%w", binary, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "--version").Output() // 与 LookPath 按同一个 PATH 解析
	if err != nil {
		return path, "", fmt.Errorf("读不到 %s 的版本：%w", binary, err)
	}
	version, _, _ = strings.Cut(strings.TrimSpace(string(out)), "\n")
	return path, strings.TrimSpace(version), nil
}
