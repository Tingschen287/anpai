package layout

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// n069：终端标签标题。owner 在 iTerm2 看到标签只写「tmux」；诊断样张 7 证明 tmux 的 set-titles 会把标题送到标签上。

func tmuxOut(t *testing.T, sock string, args ...string) string {
	t.Helper()
	c := exec.Command("tmux", append([]string{"-S", sock}, args...)...)
	c.Env = append(os.Environ(), "TMUX=")
	out, err := c.Output()
	if err != nil {
		t.Fatalf("tmux %v：%v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// 外层终端实际收到的标题 = set-titles-string 展开后的结果。
func titleNow(t *testing.T, sock string) string {
	return tmuxOut(t, sock, "display-message", "-p", "#{T:set-titles-string}")
}

func TestBaseConfSetsTitles(t *testing.T) {
	conf := BaseConf(Spec{Title: "安排 Anpai"})
	for _, want := range []string{"set -g @deck_title '安排 Anpai'", "set -g set-titles-string '#{@deck_title}'", "set -g set-titles on"} {
		if !strings.Contains(conf, want) {
			t.Fatalf("配置里缺 %q：\n%s", want, conf)
		}
	}
	if strings.Contains(BaseConf(Spec{}), "set-titles") {
		t.Fatal("没给标题就不设（布局包不替调用方猜名字）")
	}
	// 标题里有引号也不能破坏配置
	if c := BaseConf(Spec{Title: "it's"}); !strings.Contains(c, `'it'\''s'`) {
		t.Fatalf("单引号要转义：%s", c)
	}
}

// 新建的布局：配置里带标题，tmux 展开后就是品牌名。
func TestNewLayoutCarriesTitle(t *testing.T) {
	_, s, _ := realLayoutWith(t, "安排 Anpai")
	if got := titleNow(t, s.LayoutSocket); got != "安排 Anpai" {
		t.Fatalf("标签标题：%q", got)
	}
}

// 复用已经在运行的布局（旧配置起的，没有 set-titles）：deck up 补设；再调一次不出错；换语言只改文字。
func TestEnsureTitleOnRunningLayoutAndLanguageSwitch(t *testing.T) {
	_, s, _ := realLayoutWith(t, "") // 旧布局：没有标题配置
	sock := s.LayoutSocket
	if got := titleNow(t, sock); strings.Contains(got, "Anpai") {
		t.Fatalf("前提：旧布局不该有品牌标题：%q", got)
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ { // 幂等
		if err := agentdeck.LayoutEnsureTitle(ctx, sock, "安排 Anpai"); err != nil {
			t.Fatal(err)
		}
	}
	if got := titleNow(t, sock); got != "安排 Anpai" {
		t.Fatalf("补设之后：%q", got)
	}
	if on := tmuxOut(t, sock, "show-options", "-gv", "set-titles"); on != "on" {
		t.Fatalf("set-titles：%q", on)
	}
	// 界面语言换成英文：侧栏只改文字
	if err := agentdeck.LayoutSetTitle(ctx, sock, "Anpai"); err != nil {
		t.Fatal(err)
	}
	if got := titleNow(t, sock); got != "Anpai" {
		t.Fatalf("英文：%q", got)
	}
	// 标题里的特殊字符原样作文字，不被 tmux 当格式展开
	if err := agentdeck.LayoutSetTitle(ctx, sock, "#(touch /tmp/n069-pwned)#{pane_id}"); err != nil {
		t.Fatal(err)
	}
	if got := titleNow(t, sock); strings.Contains(got, "%") {
		t.Fatalf("文字不该被当格式展开成 pane id：%q", got)
	}
	if _, err := os.Stat("/tmp/n069-pwned"); err == nil {
		t.Fatal("标题里的 #() 不该被执行")
	}
}

func TestEnsureTitleWithoutServerFails(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("没有 tmux")
	}
	err := agentdeck.LayoutEnsureTitle(context.Background(), filepath.Join(t.TempDir(), "nosuch"), "x")
	if err == nil {
		t.Fatal("没有 server 应报错（deck up 只提示，不拦着进布局）")
	}
}

// realLayoutWith 同 realLayout，但可以指定标题。
func realLayoutWith(t *testing.T, title string) (TmuxMux, Spec, Built) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("没有 tmux")
	}
	dir, err := os.MkdirTemp("", "dly")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "deck")
	t.Cleanup(func() {
		c := exec.Command("tmux", "-S", sock, "kill-server")
		c.Env = append(os.Environ(), "TMUX=")
		c.Run()
		os.RemoveAll(dir)
	})
	fake := filepath.Join(dir, "fake deck")
	os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 600\n"), 0o755)
	s := Spec{Deck: fake, LayoutSocket: sock, AgentSocket: filepath.Join(dir, "agent"), Width: 160, Height: 40, Title: title}
	s.Panels = defaultsForTest()
	conf := filepath.Join(dir, "base.conf")
	os.WriteFile(conf, []byte(BaseConf(s)), 0o600)
	m := TmuxMux{Socket: sock}
	b, err := Build(m, s, conf)
	if err != nil {
		t.Fatal(err)
	}
	return m, s, b
}

func defaultsForTest() []deckstore.PanelDef { return deckstore.DefaultPanels() }
