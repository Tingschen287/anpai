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
//
// 复审 2 之后（owner 选根治方案）的口径：总期限只在「步骤之间」看。一个步骤（这里是拆一个窗格）一旦开始就做完（不随期限取消），
// 期限到了就在下一个步骤的边界停下——所以第一块的拆除跨过期限仍成功，第二块不再开始，整次调用如实报错。
// （改前的断言是「单个 kill-pane 被期限取消、不报成功」：那正是复审 2 的 R2-2 指出的、会把布局留在半途的做法。）
func TestN069ApplierHonoursOverallDeadline(t *testing.T) {
	d := t.TempDir()
	script := `#!/bin/sh
case "$3" in
list-panes)
 case "$6" in
 *pane_pid*) ;;
 *)
 [ -e "$N069_REMOVED.%4" ] || printf '%%4\tpanel:right:quota\n'
 [ -e "$N069_REMOVED.%5" ] || printf '%%5\tpanel:right:progress\n'
 exit 0;;
 esac
 printf '%%1\t100\tcenter\tmain\t100\t40\t1\t50\n'
 printf '%%2\t101\tsidebar\tmain\t32\t50\t0\t50\n'
 printf '%%3\t102\ttabs\tmain\t100\t1\t0\t50\n'
 [ -e "$N069_REMOVED.%4" ] || printf '%%4\t103\tpanel:right:quota\tmain\t40\t20\t0\t50\n'
 [ -e "$N069_REMOVED.%5" ] || printf '%%5\t104\tpanel:right:progress\tmain\t40\t20\t0\t50\n'
 ;;
kill-pane)
 /bin/sleep 0.2
 /usr/bin/touch "$N069_REMOVED.$5"
 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(d, "tmux"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", d+":"+os.Getenv("PATH"))
	t.Setenv("N069_REMOVED", filepath.Join(d, "removed"))
	defs := []deckstore.PanelDef{
		{Name: "quota", Command: "sleep 1", Region: "right", On: false},
		{Name: "progress", Command: "sleep 1", Region: "right", On: false}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	a, r, err := layoutPanelApplier(filepath.Join(d, "deck"), filepath.Join(d, "agent"))(ctx, defs)
	took := time.Since(start)
	t.Logf("deadline=100ms took=%v attached=%v removed=%v err=%v ctx=%v", took, a, r, err, ctx.Err())
	// 总期限到了：不报成功；已经开始的第一步做完（窗格真的拆了、如实记下），第二步不再开始（窗格还在）
	if err == nil {
		t.Fatalf("总期限已过，整次调用不该报成功：removed=%v", r)
	}
	if len(r) != 1 || r[0] != "quota" {
		t.Fatalf("已开始的一步应做完并记下，之后的不再开始：removed=%v", r)
	}
	if _, e := os.Stat(filepath.Join(d, "removed.%4")); e != nil {
		t.Fatal("第一块的拆除应已完成（不随期限取消）")
	}
	if _, e := os.Stat(filepath.Join(d, "removed.%5")); e == nil {
		t.Fatal("期限已过，第二块的步骤不该开始")
	}
	if took > 3*time.Second {
		t.Fatalf("应在期限附近就返回，实际 %v", took)
	}
}

// 期限在调用开始前就已经过了：一个步骤也不开始，窗格原样留着。
func TestN069ApplierDoesNotStartAStepAfterTheDeadline(t *testing.T) {
	d := t.TempDir()
	script := `#!/bin/sh
case "$3" in
list-panes)
 case "$6" in
 *pane_pid*) ;;
 *) printf '%%4\tpanel:right:quota\n'; exit 0;;
 esac
 printf '%%1\t100\tcenter\tmain\t100\t40\t1\t50\n'
 printf '%%4\t103\tpanel:right:quota\tmain\t40\t40\t0\t50\n'
 ;;
kill-pane)
 /usr/bin/touch "$N069_KILLED"
 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(d, "tmux"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", d+":"+os.Getenv("PATH"))
	t.Setenv("N069_KILLED", filepath.Join(d, "killed"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, r, err := layoutPanelApplier(filepath.Join(d, "deck"), filepath.Join(d, "agent"))(ctx, []deckstore.PanelDef{{Name: "quota", Command: "sleep 1", Region: "right", On: false}})
	if err == nil || len(r) != 0 {
		t.Fatalf("已取消的调用不该成功：%v %v", r, err)
	}
	if _, e := os.Stat(filepath.Join(d, "killed")); e == nil {
		t.Fatal("已取消，不该开始拆窗格")
	}
}
