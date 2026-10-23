package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/layout"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
	"github.com/Tingschen287/terminal-deck/work/internal/sockpath"
	"github.com/Tingschen287/terminal-deck/work/internal/testsock"
)

// TMUX_TMPDIR 很长（隔离环境、沙箱）：布局 server 的 socket 是 deck 自己用 -S 起的，退到短路径；
// agent-deck 的 socket 在 tmux 自己的位置，deck 挪不了，起服务前给出清楚的报错。
func TestTmuxSocketsWithAVeryLongTmuxTmpdir(t *testing.T) {
	long := filepath.Join(testsock.Dir(t), strings.Repeat("t", sockpath.Max))
	t.Setenv("TMUX_TMPDIR", long)
	raw := tmuxSocket("deck")
	if len(raw) <= sockpath.Max {
		t.Fatalf("前提：路径应超长：%d", len(raw))
	}
	got := defaultLayoutSocket()
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(got)) })
	if len(got) > sockpath.Max || got == raw || filepath.Base(got) != "deck" {
		t.Fatalf("布局 socket 应退到短路径（文件名不变）：%q", got)
	}
	err := socketProblem(got, tmuxSocket("default"))
	if err == nil || !strings.Contains(err.Error(), "-agent-socket") || !strings.Contains(err.Error(), "TMUX_TMPDIR") {
		t.Fatalf("agent-deck 的 socket 超长应说清楚怎么办：%v", err)
	}
	// 短的 TMUX_TMPDIR 照旧：路径和以前一样，核对通过
	t.Setenv("TMUX_TMPDIR", "/tmp")
	if p := defaultLayoutSocket(); p != tmuxSocket("deck") {
		t.Fatalf("放得下时不该变：%q", p)
	}
	if err := socketProblem(defaultLayoutSocket(), tmuxSocket("default")); err != nil {
		t.Fatalf("短路径应通过：%v", err)
	}
}

// 指定了 projectctl 的解释器就带给 deckd（布局 tmux 后台窗口的环境可能比 owner 现在的旧）；没指定则命令行和以前一样。
func TestDeckdCommandCarriesTheProjectctlPython(t *testing.T) {
	spec := layout.Spec{Deck: "/opt/deck", LayoutSocket: "/tmp/l", AgentSocket: "/tmp/a"}
	plain := deckdCommand(spec, "", envMap(nil))
	if strings.Contains(plain, "projectctl-python") {
		t.Fatalf("没指定时不该带：%s", plain)
	}
	with := deckdCommand(spec, "", envMap(map[string]string{projectctl.EnvPython: "/opt/py 3.12/bin/python3.12"}))
	if !strings.Contains(with, "-projectctl-python '/opt/py 3.12/bin/python3.12'") {
		t.Fatalf("应带上并正确引用：%s", with)
	}
	if !strings.HasPrefix(with, plain) {
		t.Fatalf("其余部分不变：\n%s\n%s", plain, with)
	}
}
