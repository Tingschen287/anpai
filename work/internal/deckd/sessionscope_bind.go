package deckd

import (
	"context"
	"net/http"
	"os"
	"path/filepath"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/sessionenv"
)

// bindLaunchPath 把 Codex trust 门核准的那个文件绑进要交给上游 agent-deck 的 PATH（n060 段 4 返工，R1）。
//
// 背景：launch 只带工具名，上游起会话时按这个名字在交给它的 PATH 里再找一次。PATH 字符串相同并不保证找到的是同一个文件——
// 核准之后、启动之前，更靠前的目录里新装一个同名文件，字符串一字不变，实际跑的却是另一个文件。
//
// 做法：approved（toolInfo 核准的绝对路径）所在的目录放到 PATH 最前面（其余目录顺序不变，去重、丢相对目录），
// 再核对一次：按这份 PATH 找 binary，找到的必须就是 approved；不是（核准的文件不见了、不再可执行、名字对不上）就拒绝启动，不让上游去找别的。
// base 是这次派工取到的会话 PATH；没开会话 PATH 解析时是 deckd 自己进程的 PATH。返回放了新 PATH 的 ctx。
//
// 范围：只绑定路径。approved 这个路径上的文件被替换（内容换了、符号链接改指别处）是既有的信任边界——trust 门一直以（项目、工具、目录、
// 路径、版本）为界，不核对文件内容，本函数不改变这一点，也不声称覆盖它。
// 副作用：交给上游的 PATH 里，核准文件所在的目录排到最前面，同目录里的其他命令也随之排前（只影响这一次启动的 agent-deck 与它起的会话）。
func bindLaunchPath(ctx context.Context, binary, approved string) (context.Context, error) {
	base, ok := agentdeck.SessionPathOf(ctx)
	if !ok {
		base = os.Getenv("PATH")
	}
	bound, ok := sessionenv.FrontDir(base, filepath.Dir(approved))
	if !ok || sessionenv.LookPath(bound, binary) != filepath.Clean(approved) {
		return ctx, i18n.E("err.deckd.serverrunspawn.bind", binary, approved, sessionenv.LookPath(bound, binary))
	}
	return agentdeck.WithSessionPath(ctx, bound), nil
}

// bindStatus 是 bindLaunchPath 失败时回给调用方的 HTTP 状态：核准的文件和要启动的对不上，请重试，不是请求本身的问题。
const bindStatus = http.StatusConflict
