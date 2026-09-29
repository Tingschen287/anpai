package agentdeck

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// InTmux 说明当前进程是不是跑在 tmux 里面。
//
// 这台机器上嵌套是常态：Work Deck 本来就常开在一个 tmux 窗格里，
// 再进入某个会话就变成两层。
func InTmux() bool { return os.Getenv("TMUX") != "" }

// NestingAdvice 是进入会话前要告诉人的话。
//
// 嵌套之后外层和内层默认是同一个前缀键，直接按会被外层吃掉。
// 这件事必须在进去之前讲，进去之后人就看不到 Work Deck 的界面了。
//
// 上游会话的状态栏写着 ctrl+q detach：上游在它那台 tmux server（默认 server）的 root 表绑了 C-q，
// 只在会话名是 agentdeck_* 时 detach（上游 internal/tmux/tmux.go:2505、6682-6700）。所以提示要看外层是哪台 server（n044，n043 复审新问题 4）：
//   - 外层就是默认 server（owner 日常这样开）：C-q 先被外层按那条绑定处理，外层会话名不是 agentdeck_*，键被吞掉（n037 真机）；
//     只能按两次前缀键再按 d。
//   - 外层是别的 server：外层不认 C-q，原样交给内层，ctrl+q 可用（n043 复审在 tmux -L 私有 server 上实测）。
//   - 不在 tmux 里：进去就是上游会话本身，ctrl+q 可用。
func NestingAdvice(prefix string) string {
	if prefix == "" {
		prefix = "C-b"
	}
	if !InTmux() {
		return fmt.Sprintf("进入后按 Ctrl+Q 退出会话、回到 Work Deck（上游状态栏写的 ctrl+q detach），也可以按 %s d。", prefix)
	}
	if outerIsDefaultServer() {
		return fmt.Sprintf(
			"Work Deck 正跑在默认 tmux server 里，进入后是两层：外层和内层都用 %s。"+
				"要操作内层得按两次 %s，例如退出会话是 %s %s d。只按一次会被外层吃掉。"+
				"上游状态栏写的 ctrl+q 在这里会被外层吞掉，不起作用。",
			prefix, prefix, prefix, prefix)
	}
	return fmt.Sprintf(
		"Work Deck 跑在另一个 tmux server 里，进入后是两层。退出会话按 Ctrl+Q（上游状态栏写的 ctrl+q detach，外层不拦它）；"+
			"也可以按两次 %s 再按 d，只按一次会被外层吃掉。", prefix)
}

// outerIsDefaultServer 说明 Work Deck 所在的 tmux 是不是默认 server——上游会话也在那台上。
// $TMUX 形如「socket 路径,server pid,会话号」，默认 server 的 socket 名是 default。
func outerIsDefaultServer() bool {
	sock := strings.SplitN(os.Getenv("TMUX"), ",", 2)[0]
	return filepath.Base(sock) == "default"
}

// prepareNested 让一条 tmux 命令能在已有 tmux 会话内部执行。
//
// tmux 默认拒绝嵌套：`sessions should be nested with care, unset $TMUX to force`，
// 退出码 1。实测确认过这句话。清掉 TMUX 就能强制嵌套，退出内层后回到外层，
// 外层会话不受影响。
func prepareNested(cmd *exec.Cmd) *exec.Cmd {
	if !InTmux() {
		return cmd
	}
	base := cmd.Env
	if base == nil {
		base = os.Environ()
	}
	cmd.Env = withoutTmux(base)
	return cmd
}

// withoutTmux 去掉 TMUX 与 TMUX_PANE。
//
// tmux 不带 -L/-S 时按 $TMUX 找 server。Work Deck 自己跑在 tmux 里时，这两个变量指向的是
// 外层那个 server，而 upstream agent-deck 的会话都在默认 server 上（n040 终审断点 2：
// 终审人把 Work Deck 放在私有 tmux 里跑，上游派出的会话落到了私有 server、随即报 error，
// 投递前的客户端探测也找不到它）。所以凡是 Work Deck 起的上游与 tmux 子进程，一律不继承它们。
func withoutTmux(base []string) []string {
	env := make([]string, 0, len(base))
	for _, kv := range base {
		if strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "TMUX_PANE=") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// NestingAdvice 是 Client 版本，供界面在进入前提示用。
func (c *Client) NestingAdvice() string { return NestingAdvice("") }
