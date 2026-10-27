package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// 只执行临时目录里的假 tmux（非登录 /bin/sh），不创建 server/socket，不连真实 tmux。
func TestN069ApplierHonoursOverallDeadline(t *testing.T) {
	d := t.TempDir()
	script := `#!/bin/sh
case "$3" in
list-panes)
 case "$6" in
 *pane_pid*) ;;
 *) printf '%%4\tpanel:right:quota\n'; exit 0;;
 esac
 printf '%%1\t100\tcenter\tmain\t100\t40\t1\t50\n'
 printf '%%2\t101\tsidebar\tmain\t32\t50\t0\t50\n'
 printf '%%3\t102\ttabs\tmain\t100\t1\t0\t50\n'
 if [ ! -e "$N069_REMOVED" ]; then
 printf '%%4\t103\tpanel:right:quota\tmain\t40\t40\t0\t50\n'
 fi
 ;;
kill-pane)
 /bin/sleep 0.2
 /usr/bin/touch "$N069_REMOVED"
 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(d, "tmux"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", d+":"+os.Getenv("PATH"))
	t.Setenv("N069_REMOVED", filepath.Join(d, "removed"))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	a, r, err := layoutPanelApplier(filepath.Join(d, "deck"), filepath.Join(d, "agent"))(ctx, []deckstore.PanelDef{{Name: "quota", Command: "sleep 1", Region: "right", On: false}})
	took := time.Since(start)
	t.Logf("deadline=100ms took=%v attached=%v removed=%v err=%v ctx=%v", took, a, r, err, ctx.Err())
	// 修好之后（Codex 审查 1 的 R1）：总期限传进每一条 tmux 命令，拆窗格的命令（假 tmux 睡 0.2 秒）被取消，如实报错，不继续也不报成功
	if err == nil || len(r) > 0 {
		t.Fatalf("总期限已过，不该成功：removed=%v err=%v", r, err)
	}
	if took > 2*time.Second { // 假 tmux 是 sh，杀掉 sh 后它的 sleep 子进程还握着管道约 0.1 秒，所以不卡死 100 毫秒；只要不是等到命令自己做完加上更久
		t.Fatalf("应在期限附近就返回，实际 %v", took)
	}
}
