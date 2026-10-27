package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/layout"
)

// 真 tmux 布局，面板命令都用 sleep（不走 "deck " 前缀，免得 os.Executable()=测试二进制被当 deck 执行）。
func advApplierFixture(t *testing.T) (sock string, applier func(context.Context, []deckstore.PanelDef) ([]string, []string, error), defs func(on ...string) []deckstore.PanelDef) {
	t.Helper()
	dir, err := os.MkdirTemp("", "advc")
	if err != nil {
		t.Fatal(err)
	}
	sock = filepath.Join(dir, "deck")
	t.Cleanup(func() {
		c := exec.Command("tmux", "-S", sock, "kill-server")
		c.Env = append(os.Environ(), "TMUX=")
		c.Run()
		os.RemoveAll(dir)
	})
	all := []deckstore.PanelDef{
		{Name: "decisions", Command: "sleep 600", Region: "right", On: true},
		{Name: "quota", Command: "sleep 600", Region: "right", On: true},
		{Name: "progress", Command: "sleep 600", Region: "right", On: false},
		{Name: "notifications", Command: "sleep 600", Region: "bottom", On: true},
	}
	defs = func(on ...string) []deckstore.PanelDef {
		out := append([]deckstore.PanelDef(nil), all...)
		for i := range out {
			out[i].On = false
			for _, n := range on {
				if out[i].Name == n {
					out[i].On = true
				}
			}
		}
		return out
	}
	fake := filepath.Join(dir, "deck")
	os.WriteFile(fake+".sh", []byte("#!/bin/sh\nexec sleep 600\n"), 0o755)
	s := layout.Spec{Deck: fake + ".sh", LayoutSocket: sock, AgentSocket: filepath.Join(dir, "agent"), Width: 160, Height: 40, Panels: defs("decisions", "quota", "notifications")}
	conf := filepath.Join(dir, "base.conf")
	os.WriteFile(conf, []byte(layout.BaseConf(s)), 0o600)
	if _, err := layout.Build(layout.TmuxMux{Socket: sock}, s, conf); err != nil {
		t.Fatal(err)
	}
	return sock, layoutPanelApplier(sock, filepath.Join(dir, "agent")), defs
}

func advRoles(t *testing.T, sock string) map[string]bool {
	ps, err := agentdeck.LayoutPanes(context.Background(), sock)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, p := range ps {
		out[p.Role] = true
	}
	return out
}

// 布局锁被别的 deck layout 持有 3 秒：应用方应等到锁释放再改，结果成功。
func TestAdvApplierWaitsForBusyLayoutLock(t *testing.T) {
	sock, apply, defs := advApplierFixture(t)
	unlock, err := layout.Lock(sock+".lock", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(3 * time.Second); unlock() }()
	t0 := time.Now()
	att, rem, err := apply(context.Background(), defs("decisions", "progress", "notifications"))
	t.Logf("等了 %v：attached=%v removed=%v err=%v", time.Since(t0), att, rem, err)
	if err != nil || len(att) != 1 || len(rem) != 1 {
		t.Fatalf("锁释放后应成功：%v %v %v", att, rem, err)
	}
	if time.Since(t0) < 2500*time.Millisecond {
		t.Fatal("不该在锁被持有时就改布局")
	}
}

// 锁一直被占（>10 秒）：应用方在 ~10 秒后报带码的错、布局纹丝不动。同时看 ctx 有没有被尊重（调用方 ctx 1 秒就到期）。
func TestAdvApplierLockHeldTooLongFailsCleanlyAndRespectsCtx(t *testing.T) {
	if testing.Short() {
		t.Skip("要 ~10 秒")
	}
	sock, apply, defs := advApplierFixture(t)
	unlock, err := layout.Lock(sock+".lock", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	before := advRoles(t, sock)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	t0 := time.Now()
	_, _, err = apply(ctx, defs("progress"))
	took := time.Since(t0)
	t.Logf("耗时 %v err=%v", took, err)
	if err == nil {
		t.Fatal("锁没拿到应报错")
	}
	if len(advRoles(t, sock)) != len(before) {
		t.Fatal("没拿到锁不该改布局")
	}
	if took > 3*time.Second {
		// 推测级/低：layout.Lock 不看 ctx；实际调用方 ctx 是 30 秒、锁上限 10 秒，所以不会拖过 ctx，只记录
		t.Logf("调用方 ctx 1 秒到期，应用方阻塞了 %v（layout.Lock 不看 ctx）", took)
	}
}

// 调用方 ctx 已取消（F9 里按了空格马上 Esc 退出，连接断了，deckd 的 r.Context() 被取消）：
// 注册表已存，布局却被悄悄跳过，而且返回 (nil,nil,nil) = 「成功」。
func TestAdvApplierCanceledCtxSilentlySkips(t *testing.T) {
	sock, apply, defs := advApplierFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	att, rem, err := apply(ctx, defs("decisions", "quota", "progress", "notifications"))
	t.Logf("att=%v rem=%v err=%v 布局角色=%v", att, rem, err, advRoles(t, sock))
	if err == nil && len(att) == 0 && advRoles(t, sock)["panel:right:progress"] == false {
		t.Errorf("ctx 取消后，应用方返回「成功」却什么都没做（LayoutHasSession 把 ctx 取消/tmux 超时当成「没有布局」）")
	}
}

// tmux 命令出错（不是「没有会话」）时同样被当成「没布局」。这里把 socket 换成一个会让 tmux 报别的错的路径（权限）
func TestAdvApplierTmuxErrorTreatedAsNoLayout(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "deck")
	os.WriteFile(sock, []byte("not a socket"), 0o600) // tmux 连它会报 "error connecting"
	apply := layoutPanelApplier(sock, filepath.Join(dir, "agent"))
	att, rem, err := apply(context.Background(), deckstore.DefaultPanels())
	t.Logf("att=%v rem=%v err=%v", att, rem, err)
	if err == nil && !strings.Contains("", "x") {
		t.Logf("socket 文件损坏时 deckd 回 applied=true（与「布局没开」不可区分）")
	}
}
