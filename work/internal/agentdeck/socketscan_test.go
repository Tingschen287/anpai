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

	socket, err := ResolveSocketFor(context.Background(), name, "")
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

	got, err := ResolveSocketFor(context.Background(), name, "")
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

	_, err := ResolveSocketFor(context.Background(), name, "")
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
	_, err := ResolveSocketFor(context.Background(), "n037scan-nobody-"+randomHexPlain(t, 4), "")
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

	if _, err := ResolveSocketFor(context.Background(), name, ""); err != nil {
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

// newTmuxSessionWithID 起一个带 AGENTDECK_INSTANCE_ID 的会话，模拟上游派出来的那种。
func newTmuxSessionWithID(t *testing.T, socket, name, instanceID string) {
	t.Helper()
	newTmuxSession(t, socket, name)
	args := []string{}
	if socket != "" {
		args = append(args, "-L", socket)
	}
	args = append(args, "set-environment", "-t", "="+name, "AGENTDECK_INSTANCE_ID", instanceID)
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		t.Fatalf("写不进会话环境：%v：%s", err, out)
	}
}

// **tmux 的 -t 是前缀匹配。** 目标已经结束、只剩一个同前缀会话时，
// 扫描绝不能把它当成目标。
//
// 独立评审实测：`has-session -t target` 在只有 `target-suffix` 时照样 exit 0，
// 加等号才是精确匹配。上一版没带等号，于是这种情况下会判「唯一」然后连错。
func TestResolveSocketDoesNotMatchByPrefix(t *testing.T) {
	needTmux(t)
	sock := "n037scan" + randomHexPlain(t, 4)
	base := "n037scan-prefix-" + randomHexPlain(t, 3)
	// 只建带后缀的那个，目标本身不存在。
	newTmuxSession(t, sock, base+"-suffix")

	if _, err := ResolveSocketFor(context.Background(), base, ""); err == nil {
		t.Fatal("目标不存在、只有同前缀会话时，必须拒绝而不是把它当成目标")
	}
}

// 名字对得上、身份对不上，也不算找到。
//
// 这一条比名字唯一性更根本：会话名会撞，AGENTDECK_INSTANCE_ID 是一对一的。
// 它还顺带覆盖了扫描与 attach 之间的替换竞态——同名会话被换掉了，身份就对不上。
func TestResolveSocketRefusesWhenTheIdentityDoesNotMatch(t *testing.T) {
	needTmux(t)
	sock := "n037scan" + randomHexPlain(t, 4)
	name := "n037scan-id-" + randomHexPlain(t, 3)
	newTmuxSessionWithID(t, sock, name, "the-real-one")

	if _, err := ResolveSocketFor(context.Background(), name, "somebody-else"); err == nil {
		t.Fatal("会话身份对不上，必须拒绝")
	} else {
		// 拒绝的理由要能指导下一步：说清是「同名的是别人」，
		// 而不是笼统的「找不到」——后者会让人以为会话已经结束了。
		for _, must := range []string{"同名的是别人", "the-real-one"} {
			if !strings.Contains(err.Error(), must) {
				t.Errorf("拒绝理由里缺 %q：%v", must, err)
			}
		}
	}

	// 身份对得上时照常找到。
	got, err := ResolveSocketFor(context.Background(), name, "the-real-one")
	if err != nil {
		t.Fatalf("身份对得上却找不到：%v", err)
	}
	if got != sock {
		t.Errorf("socket 名是 %q，期望 %q", got, sock)
	}
}

// 同名且都带身份时，只认身份对得上的那个——这不算歧义。
//
// 名字撞车本身不该让功能失效，只要身份能把它们区分开。
func TestIdentityDisambiguatesSameNameOnTwoServers(t *testing.T) {
	needTmux(t)
	name := "n037scan-idcollide-" + randomHexPlain(t, 3)
	sock := "n037scan" + randomHexPlain(t, 4)
	newTmuxSessionWithID(t, "", name, "instance-A")   // 默认 server
	newTmuxSessionWithID(t, sock, name, "instance-B") // 另一个 server

	got, err := ResolveSocketFor(context.Background(), name, "instance-B")
	if err != nil {
		t.Fatalf("身份能把两个同名会话区分开，不该报歧义：%v", err)
	}
	if got != sock {
		t.Errorf("认到了 %q，期望 %q", got, sock)
	}
}

// 会话名在，但读不到身份——也拒绝，而且要说清是哪一种拒绝。
//
// 这种会话真实存在：上游老版本建的没有 AGENTDECK_INSTANCE_ID，
// 一个不响应的 tmux server 也会让这个变量读不出来。两者都不能放行，
// 但也不能说成「会话不在了」——人会照着去查一个根本没发生的问题。
func TestResolveSocketSaysWhenTheIdentityCannotBeRead(t *testing.T) {
	needTmux(t)
	sock := "n037scan" + randomHexPlain(t, 4)
	name := "n037scan-noid-" + randomHexPlain(t, 3)
	// 故意不写 AGENTDECK_INSTANCE_ID。
	newTmuxSession(t, sock, name)

	_, err := ResolveSocketFor(context.Background(), name, "whoever")
	if err == nil {
		t.Fatal("读不到身份就不能放行")
	}
	for _, must := range []string{"读不到", "AGENTDECK_INSTANCE_ID", "宁可不连"} {
		if !strings.Contains(err.Error(), must) {
			t.Errorf("拒绝理由里缺 %q：%v", must, err)
		}
	}
	// 不能被说成「会话已经结束」——那是另一回事。
	if strings.Contains(err.Error(), "可能已经结束了") {
		t.Errorf("把「读不到身份」说成了「会话结束」：%v", err)
	}
}
