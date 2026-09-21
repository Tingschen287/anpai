package agentdeck

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// needTmux 没有 tmux 就跳过，并说清原因。
func needTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("跳过：本机没有 tmux")
	}
}

// newTmuxSession 在指定 socket 上起一个会话，测完收掉。
//
// socket 为空表示默认 server。
func newTmuxSession(t *testing.T, socket, name string) {
	t.Helper()
	args := []string{}
	if socket != "" {
		args = append(args, "-L", socket)
	}
	args = append(args, "new-session", "-d", "-s", name, "sleep 120")
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		t.Fatalf("起 tmux 会话失败（socket=%q）：%v：%s", socket, err, out)
	}
	t.Cleanup(func() {
		kill := []string{}
		if socket != "" {
			kill = append(kill, "-L", socket)
		}
		kill = append(kill, "kill-session", "-t", name)
		_ = exec.Command("tmux", kill...).Run()
		if socket != "" {
			// 连 server 一起收掉，免得在 socket 目录里留下死文件。
			_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
		}
	})
}

// 只有一个 server 上有这个名字时，扫描把它认出来。
func TestResolveSocketFindsTheOnlyHost(t *testing.T) {
	needTmux(t)
	name := "n037scan-unique-" + randomHexPlain(t, 3)
	newTmuxSession(t, "", name) // 默认 server

	socket, err := ResolveSocketFor(context.Background(), name)
	if err != nil {
		t.Fatalf("只有一个 server 上有它，不该失败：%v", err)
	}
	// 默认 server 用空串表示，沿用本包「空即默认」的约定。
	if socket != "" {
		t.Errorf("会话在默认 server 上，socket 名应当是空串，实际 %q", socket)
	}
}

// 非默认 socket 上的会话，扫描要把 socket 名找回来。
//
// 这比 attempt 7 那版强：那版降级时一律按默认 server 连，
// 非默认 socket 上的会话根本连不上。
func TestResolveSocketRecoversANonDefaultSocket(t *testing.T) {
	needTmux(t)
	sock := "n037scan" + randomHexPlain(t, 4)
	name := "n037scan-alt-" + randomHexPlain(t, 3)
	newTmuxSession(t, sock, name)

	got, err := ResolveSocketFor(context.Background(), name)
	if err != nil {
		t.Fatalf("会话在非默认 socket 上，扫描应当找得到：%v", err)
	}
	if got != sock {
		t.Errorf("socket 名是 %q，期望 %q", got, sock)
	}
}

// **本节点最要紧的一条**：同名会话出现在多个 server 上时，必须拒绝。
//
// 不同 tmux server 各有各的命名空间，同名会话完全可能是毫不相干的两个东西。
// 不带 -L 去连，连上的是默认 server 上那个——而且不会报错，
// 人会以为自己进对了地方。连错比连不上严重得多。
//
// attempt 7 的降级路径正是无条件按默认 server 连，独立评审复现了这个误连。
func TestResolveSocketRefusesWhenTheNameIsAmbiguous(t *testing.T) {
	needTmux(t)
	name := "n037scan-collide-" + randomHexPlain(t, 3)
	sock := "n037scan" + randomHexPlain(t, 4)
	newTmuxSession(t, "", name)   // 默认 server
	newTmuxSession(t, sock, name) // 另一个 server，同名

	_, err := ResolveSocketFor(context.Background(), name)
	if err == nil {
		t.Fatal("同名会话存在于两个 server 上，必须拒绝而不是挑一个连")
	}
	var amb *ErrAmbiguousTmuxTarget
	if !errors.As(err, &amb) {
		t.Fatalf("应当是歧义错误，实际：%v", err)
	}
	if len(amb.Sockets) < 2 {
		t.Errorf("歧义错误里该列出全部命中的 server，实际 %v", amb.Sockets)
	}
	// 错误信息要让人看懂发生了什么。
	if !strings.Contains(err.Error(), "拒绝直连") {
		t.Errorf("拒绝理由不清楚：%v", err)
	}
}

// 会话不存在时，说清楚是不存在，而不是含糊地失败。
func TestResolveSocketSaysWhenNothingHostsIt(t *testing.T) {
	needTmux(t)
	_, err := ResolveSocketFor(context.Background(), "n037scan-nobody-"+randomHexPlain(t, 4))
	if err == nil {
		t.Fatal("没有任何 server 有这个会话，应当报错")
	}
	if !strings.Contains(err.Error(), "可能已经结束") {
		t.Errorf("没说清是会话不在了：%v", err)
	}
}

// 死掉的 socket 文件不该让扫描出错。
//
// tmux server 退出后 socket 文件常常留在目录里，对它们 has-session 会失败，
// 扫描要能不当回事地跳过。
func TestResolveSocketToleratesDeadSockets(t *testing.T) {
	needTmux(t)
	deadSock := "n037scandead" + randomHexPlain(t, 4)
	deadName := "n037scan-dead-" + randomHexPlain(t, 3)
	// 起一个再杀掉 server，socket 文件通常会留下。
	newTmuxSession(t, deadSock, deadName)
	_ = exec.Command("tmux", "-L", deadSock, "kill-server").Run()

	name := "n037scan-live-" + randomHexPlain(t, 3)
	newTmuxSession(t, "", name)

	if _, err := ResolveSocketFor(context.Background(), name); err != nil {
		t.Errorf("目录里有死 socket 就扫不下去了：%v", err)
	}
}

// randomHexPlain 是本文件用的随机后缀，避免并发跑测试时会话名撞车。
//
// 不复用 integration_test.go 里的 randomHex：那个文件带真实上游的前置条件，
// 而这一组测试只需要 tmux。
func randomHexPlain(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}
