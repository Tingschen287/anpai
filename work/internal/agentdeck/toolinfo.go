package agentdeck

import (
	"bytes"
	"context"
	"errors"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/sessionenv"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type sessionPathKey struct{}

// WithSessionPath 在 ctx 里放一份会话 PATH（n060 段 4）。deckd 一次派工（或一次列工具）取一份，放进 ctx：
// 找 CLI（deckd.scopedToolInfo、scopedListModels，调用方先在这份 PATH 里解出可执行文件的绝对路径，再交给本包的 ToolVersionAt、ListModelsAt）
// 与 Client.Launch（把它交给 `agent-deck launch`，agent-deck 起的会话用的就是启动者的 PATH）用的是同一份，
// trust 门记下的可执行文件才一定是会话里实际跑的那一个。ctx 里没有就用进程自己的 PATH（旧行为）。
func WithSessionPath(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, sessionPathKey{}, path)
}

// sessionEnv 是当前环境，PATH 换成会话 PATH。
func sessionEnv(path string) []string { return sessionenv.Env(os.Environ(), path) }

// SessionPathOf 取 ctx 里的会话 PATH。
func SessionPathOf(ctx context.Context) (string, bool) {
	p, ok := ctx.Value(sessionPathKey{}).(string)
	return p, ok && p != ""
}

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
	version, _, _ = strings.Cut(strings.TrimSpace(i18n.Neutralize(string(out))), "\n")
	return path, strings.TrimSpace(version), nil
}

// ListModels 跑一个 CLI 列模型的子命令（grok models、agy models、opencode models），20 秒为限，返回标准输出（n057：设置界面加模型时
// 给候选）。会用到该 CLI 自己的登录状态与网络，只在 owner 在设置界面里按键时跑；只读，不改任何配置。
func ListModels(ctx context.Context, binary string, args []string) (string, error) {
	return listModels(ctx, binary, args, nil)
}

// ListModelsAt 同 ListModels，但可执行文件是调用方在会话 PATH 里解出来的绝对路径 exe（与 ToolInfo 查到的、会话里实际跑的是同一个），
// 子进程的 PATH 也设成这份会话 PATH（n060 段 4）。
func ListModelsAt(ctx context.Context, exe string, args []string, sessionPath string) (string, error) {
	return listModels(ctx, exe, args, sessionEnv(sessionPath))
}

// maxListOutput 是列模型命令输出的上限：`codex debug models` 带着大段说明文字，几十万字节是常态，8 MiB 之外的当异常。
const maxListOutput = 8 << 20

// listWait 是列模型命令的时限（var 只为让测试缩短它）。
var listWait = 20 * time.Second

func listModels(ctx context.Context, exe string, args, env []string) (string, error) {
	wait := listWait
	ctx2, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	cmd := exec.CommandContext(ctx2, exe, args...)
	cmd.Env = env
	killGroupOnCancel(cmd)
	var out bytes.Buffer
	lw := &limitedWriter{w: &out, max: maxListOutput}
	cmd.Stdout = lw
	err := cmd.Run()
	switch {
	case lw.over: // 写满之后进程被 SIGPIPE 收掉，Run 返回的是退出状态，不是写失败，所以单独记一笔
		return "", i18n.E("err.agentdeck.listmodels.toolarge", exe)
	case err != nil && errors.Is(ctx2.Err(), context.DeadlineExceeded):
		return "", i18n.E("err.agentdeck.listmodels.timeout", exe, wait.String())
	}
	return i18n.Neutralize(out.String()), err
}

var errListTooLarge = errors.New("list output too large")

// limitedWriter 写满 max 字节后报错，让进程以写失败结束（不是无限读）。
type limitedWriter struct {
	w    *bytes.Buffer
	max  int
	over bool
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.w.Len()+len(p) > l.max {
		l.over = true
		return 0, errListTooLarge
	}
	return l.w.Write(p)
}

// ToolVersionAt 跑 `<exe> --version` 取第一行：exe 是调用方在会话 PATH 里解出来的绝对路径，子进程的 PATH 设成这份会话 PATH
// （`#!/usr/bin/env node` 之类的脚本也按会话 PATH 找解释器）。10 秒为限（n060 段 4）。
func ToolVersionAt(ctx context.Context, exe, sessionPath string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "--version")
	cmd.Env = sessionEnv(sessionPath)
	killGroupOnCancel(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	version, _, _ := strings.Cut(strings.TrimSpace(i18n.Neutralize(string(out))), "\n")
	return strings.TrimSpace(version), nil
}

// killGroupOnCancel 让超时、取消时杀掉 CLI 的整个进程组，并且不等孙进程放开管道：CLI（node 脚本等）会再起子进程，
// 只杀它自己的话孙进程还握着标准输出，Wait 要等到它们退出才返回，设置界面就卡在「正在取」上（n060 段 4）。
func killGroupOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // 新会话：整组可杀，也不碰 deckd 的控制终端
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = time.Second
}
