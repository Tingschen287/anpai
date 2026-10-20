package layout

import (
	"context"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// CenterLoop 是中间窗格里 `deck center` 的重连循环（架构稿 §1 Ctrl-Q、§7 必测场景，n049）。
//
// 中间窗格里跑一个连到 agent-deck server 的只读 tmux client。它退出有两种：
//   - 正常断开（退出码 0）：显示的会话被关掉，或 client 被 detach（Ctrl-Q 由布局 server 截获、经 deckd 隐藏，
//     不会让 client 退出）。按隐藏规则处理：
//     重新以只读连到 deck-idle，再告诉 deckd「reset」，deckd 把焦点会话移出打开集合、把新 client 切到上一个打开的会话。
//   - 连不上或出错（非 0）：重新连上后只告诉 deckd「resync」，按当前焦点重做一次切换，不隐藏任何会话。
//
// 不论哪种，连续 MaxQuick 次都在 QuickExit 之内退出，就停下不再重试，显示原因、等 owner 按键（避免空转刷屏）。
type CenterLoop struct {
	// Attach 起一个 client，返回它的进程号与等待函数。
	Attach func() (pid int, wait func() error, err error)
	// Announce 等新 client 连上后通知 deckd（op 为 resync 或 reset）。在后台调用，失败只记下不打断循环。
	// ctx 在这个 client 退出时取消：旧 client 的通知不能留到新 client 上去执行（n049 评审 1 B1）。
	Announce func(ctx context.Context, op string, pid int)
	// Fence 在每次起 client 之前调用（含第一次）：等 deckd 正在进行的视图操作做完再连新 client。
	// 新旧 client 共用中间窗格的 tty，操作做到一半时连上的新 client 会被当成旧的（n049 评审 2 B1）。
	// 它返回之前不会起新 client，所以只能在确认没有进行中的操作时返回（n049 评审 3 B1-A）。可为 nil。
	Fence func()
	// Pause 显示原因并等 owner 决定：true 重试，false 退出循环。
	Pause func(reason string) bool
	// PauseLive 不为 nil 时代替 Pause：原因给的是函数，每次调用按此刻的界面语言重新拼，
	// 停住等按键期间 owner 改了界面语言，调用方可以重画整屏（n060 段 3）。
	PauseLive func(reason func() string) bool
	Now       func() time.Time

	QuickExit time.Duration // 默认 3 秒
	MaxQuick  int           // 默认 3 次
}

// Run 一直跑到 Pause 返回 false。
func (l *CenterLoop) Run() {
	if l.Now == nil {
		l.Now = time.Now
	}
	if l.QuickExit <= 0 {
		l.QuickExit = 3 * time.Second
	}
	if l.MaxQuick <= 0 {
		l.MaxQuick = 3
	}
	op, quick := "resync", 0
	for {
		if l.Fence != nil {
			l.Fence() // 每次起 client 之前（含第一次：deck center 自己可能刚被重启）
		}
		start := l.Now()
		pid, wait, err := l.Attach()
		var alive time.Duration
		if err == nil {
			ctx, cancel := context.WithCancel(context.Background())
			go l.Announce(ctx, op, pid)
			err = wait()
			alive = l.Now().Sub(start) // client 活了多久：在它退出时量，栅栏的等待不算进去（n049 评审 3 B8）
			cancel()
		} else {
			alive = l.Now().Sub(start)
		}
		if alive < l.QuickExit {
			quick++
		} else {
			quick = 0
		}
		if quick >= l.MaxQuick {
			quick, quickExit, lastErr := quick, l.QuickExit, err
			reason := func() string {
				r := i18n.T("layout.err.center", quick, quickExit)
				if lastErr != nil {
					r += i18n.T("layout.err.center.last") + deckclient.Show(lastErr) // 带码的 tmux 退出说明按界面语言显示（返工 4）；中文与 Present(err.Error()) 逐字相同
				}
				return r
			}
			retry := false
			if l.PauseLive != nil {
				retry = l.PauseLive(reason)
			} else {
				retry = l.Pause(reason())
			}
			if !retry {
				return
			}
			op, quick = "resync", 0
			continue
		}
		if err != nil {
			op = "resync"
		} else {
			op = "reset"
		}
	}
}
