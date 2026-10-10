package layout

import (
	"fmt"
	"time"
)

// CenterLoop 是中间窗格里 `deck center` 的重连循环（架构稿 §1 Ctrl-Q、§7 必测场景，n049）。
//
// 中间窗格里跑一个连到 agent-deck server 的只读 tmux client。它退出有两种：
//   - 正常断开（退出码 0）：owner 按了 Ctrl-Q（agent-deck 的绑定），或显示的会话被关掉。按隐藏规则处理：
//     重新以只读连到 deck-idle，再告诉 deckd「reset」，deckd 把焦点会话移出打开集合、把新 client 切到上一个打开的会话。
//   - 连不上或出错（非 0）：重新连上后只告诉 deckd「resync」，按当前焦点重做一次切换，不隐藏任何会话。
//
// 不论哪种，连续 MaxQuick 次都在 QuickExit 之内退出，就停下不再重试，显示原因、等 owner 按键（避免空转刷屏）。
type CenterLoop struct {
	// Attach 起一个 client，返回它的进程号与等待函数。
	Attach func() (pid int, wait func() error, err error)
	// Announce 等新 client 连上后通知 deckd（op 为 resync 或 reset）。在后台调用，失败只记下不打断循环。
	Announce func(op string, pid int)
	// Pause 显示原因并等 owner 决定：true 重试，false 退出循环。
	Pause func(reason string) bool
	Now   func() time.Time

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
		start := l.Now()
		pid, wait, err := l.Attach()
		if err == nil {
			go l.Announce(op, pid)
			err = wait()
		}
		if l.Now().Sub(start) < l.QuickExit {
			quick++
		} else {
			quick = 0
		}
		if quick >= l.MaxQuick {
			reason := fmt.Sprintf("中间窗格连续 %d 次在 %s 内断开，已停止重试", quick, l.QuickExit)
			if err != nil {
				reason += "。最后一次：" + err.Error()
			}
			if !l.Pause(reason) {
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
