package layout

import (
	"context"
	"os"
	"syscall"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// Lock 串行化布局修改（n049 评审 1 S2）：F5/F6/F7 与 client-resized 钩子都用 tmux run-shell -b 在后台跑
// deck layout，快速连按或边按边缩放时会交错执行，停放、并回、整理尺寸互相踩。拿到锁的先做完，其余排队；
// 等 timeout 还拿不到就报错放弃（不强行进去）。锁随进程退出自动释放。
func Lock(path string, timeout time.Duration) (unlock func(), err error) {
	return LockCtx(context.Background(), path, timeout)
}

// LockCtx 同 Lock，但等锁的时候也看 ctx：ctx 取消或到期就放弃，返回带码的错误（n069 Codex 审查 R1：挂拔面板的总 deadline 要管到等锁）。
func LockCtx(ctx context.Context, path string, timeout time.Duration) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
		}
		if err != syscall.EWOULDBLOCK || time.Now().After(deadline) {
			f.Close()
			return nil, i18n.Errorf("layout.err.lock", path, err)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, i18n.Errorf("layout.err.lock", path, ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}
