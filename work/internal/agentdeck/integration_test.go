package agentdeck

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 集成测试跑在真实的 upstream agent-deck 上。
//
// 隔离靠三样东西，缺一不可：
//   - 一次性 profile，名字带随机后缀，绝不会撞上使用者的真实 profile
//   - 回环地址加随机 token，服务不对外
//   - 临时项目目录，不碰任何真实业务项目
//
// 上游自己记过一次事故（2025-12-11）：测试跑在真实 profile 上，一次覆盖掉
// 36 个生产会话。所以这里的 profile 名是随机生成的，不接受外部指定。

func upstreamBinary(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("WORKDECK_AGENTDECK_BIN")
	if bin == "" {
		t.Skip("跳过：没有设 WORKDECK_AGENTDECK_BIN，指向构建好的 upstream agent-deck 二进制")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("跳过：WORKDECK_AGENTDECK_BIN 指向的文件不可用（%v）", err)
	}
	return bin
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// liveUpstream 起一个隔离的 headless 上游，返回客户端，并登记清理。
func liveUpstream(t *testing.T) (*Client, string) {
	t.Helper()
	bin := upstreamBinary(t)
	profile := "_n036it" + randomHex(t, 4)
	token := randomHex(t, 16)
	addr := fmt.Sprintf("127.0.0.1:%d", freePort(t))

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, "web", "--no-tui", "--listen", addr, "--token", token)
	cmd.Env = append(os.Environ(), "AGENTDECK_PROFILE="+profile)
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("起不来 upstream headless 服务：%v", err)
	}

	c, err := NewClient(bin, profile, addr, token)
	if err != nil {
		cancel()
		t.Fatal(err)
	}

	// 等服务就绪。
	ready := false
	for i := 0; i < 60; i++ {
		probe, probeCancel := context.WithTimeout(context.Background(), time.Second)
		_, err := c.Sessions(probe)
		probeCancel()
		if err == nil {
			ready = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !ready {
		cancel()
		_ = cmd.Wait()
		t.Fatal("upstream headless 服务 15 秒内没就绪")
	}

	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
		// 收掉本次留下的 tmux 会话与 profile 目录，不给机器留垃圾。
		cleanupTmux(t, profile)
		home, err := os.UserHomeDir()
		if err == nil {
			dir := filepath.Join(home, ".local", "share", "agent-deck", "profiles", profile)
			if strings.Contains(dir, profile) && strings.HasPrefix(profile, "_n036it") {
				_ = os.RemoveAll(dir)
			}
		}
	})
	return c, profile
}

// cleanupTmux 收掉本次测试起的会话。只杀自己造的，按标题前缀认。
func cleanupTmux(t *testing.T, profile string) {
	t.Helper()
	out, err := exec.Command("tmux", "ls", "-F", "#{session_name}").Output()
	if err != nil {
		return // 没有 tmux 服务就没有要收的
	}
	for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.Contains(name, "n036it") {
			_ = exec.Command("tmux", "kill-session", "-t", name).Run()
		}
	}
}

// 派工到真实上游，拿回真实会话号，并确认它确实落进了上游的存储。
//
// 这里同时钉死一条上游的已知约束：headless 服务对菜单快照做了缓存，
// 缓存只在服务自己处理变更时失效。`launch` 是另一个进程，服务不会感知，
// 所以刚派的会话在 /api/sessions 与 SSE 里都看不到，直到服务重启。
// 上游自己的注释承认了这点（internal/web/server.go 的 notifyMenuChanged）。
//
// 断言这条约束，而不是绕开它：上游哪天修好了，这个测试会失败，
// 那正是我们该回来更新依赖清单和 Work Deck 视图逻辑的时候。
func TestIntegrationLaunchLandsInStorageEvenThoughServerCachesIt(t *testing.T) {
	c, profile := liveUpstream(t)
	ctx := context.Background()

	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte("# 夹具项目\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	title := "n036it-" + randomHex(t, 3)

	before, err := c.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Sessions) != 0 {
		t.Fatalf("一次性 profile 里本该没有会话，实际有 %d 个——隔离可能没生效", len(before.Sessions))
	}
	if before.Profile != profile {
		t.Fatalf("上游报告的 profile 是 %q，期望 %q——隔离没生效", before.Profile, profile)
	}

	// 用 shell 而不是任何真实 agent CLI：本测试验的是派工链路，不是模型。
	// shell 没有模型可选，显式声明；这不等于「不知道用哪个模型」。
	res, err := c.Launch(ctx, LaunchRequest{
		ProjectPath: project, Title: title, Group: "n036it",
		Tool: "shell", ModelNotApplicable: true, NoWait: true,
	})
	if err != nil {
		t.Fatalf("派工失败：%v", err)
	}
	if res.SessionID == "" || !res.Success {
		t.Fatalf("上游没有回传可用的会话号：%+v", res)
	}
	if res.Profile != profile {
		t.Errorf("会话落在了 profile %q，期望 %q", res.Profile, profile)
	}

	// 已知约束：同一个服务实例看不到它。给足时间，确认不是时序问题。
	time.Sleep(3 * time.Second)
	stale, err := c.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range stale.Sessions {
		if s.ID == res.SessionID {
			t.Fatalf("上游行为变了：CLI 派的会话现在服务能直接看到了。"+
				"这是好事，但依赖清单第 7 节与 Work Deck 的视图合并逻辑需要同步更新（会话 %s）", s.ID)
		}
	}

	// 但数据确实进了存储：换一个新服务实例就看得见。
	fresh, cleanup := freshServerOn(t, c.Binary, profile)
	defer cleanup()
	list, err := fresh.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found *Session
	for i := range list.Sessions {
		if list.Sessions[i].ID == res.SessionID {
			found = &list.Sessions[i]
		}
	}
	if found == nil {
		t.Fatalf("新起的服务实例也看不到会话 %s，说明它根本没落进存储", res.SessionID)
	}
	if found.Title != title {
		t.Errorf("会话标题是 %q，期望 %q", found.Title, title)
	}
	if found.TmuxSession == "" {
		t.Error("会话没有 tmux 名字，后续节点没法接管")
	}

	// SSE 也要能连上并推出快照（内容同样受上述缓存约束）。
	sseCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	seen := make(chan MenuEvent, 1)
	go func() {
		_ = fresh.WatchMenu(sseCtx, func(ev MenuEvent) {
			select {
			case seen <- ev:
			default:
			}
		})
	}()
	select {
	case ev := <-seen:
		if ev.Profile != profile {
			t.Errorf("菜单事件的 profile 是 %q，期望 %q", ev.Profile, profile)
		}
		var hit bool
		for _, s := range ev.SessionsOf() {
			if s.ID == res.SessionID {
				hit = true
			}
		}
		if !hit {
			t.Errorf("新服务实例的菜单事件里没有会话 %s", res.SessionID)
		}
	case <-sseCtx.Done():
		t.Fatal("20 秒内没收到任何菜单事件")
	}
}

// freshServerOn 在同一个 profile 上另起一个服务实例，用来读取已落盘的会话。
func freshServerOn(t *testing.T, bin, profile string) (*Client, func()) {
	t.Helper()
	token := randomHex(t, 16)
	addr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, "web", "--no-tui", "--listen", addr, "--token", token)
	cmd.Env = append(os.Environ(), "AGENTDECK_PROFILE="+profile)
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("起不来第二个服务实例：%v", err)
	}
	c, err := NewClient(bin, profile, addr, token)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		probe, pc := context.WithTimeout(context.Background(), time.Second)
		_, err := c.Sessions(probe)
		pc()
		if err == nil {
			return c, func() { cancel(); _ = cmd.Wait() }
		}
		time.Sleep(250 * time.Millisecond)
	}
	cancel()
	_ = cmd.Wait()
	t.Fatal("第二个服务实例 15 秒内没就绪")
	return nil, func() {}
}

// 没有 token 就不该拿得到数据。
func TestIntegrationTokenIsEnforced(t *testing.T) {
	c, _ := liveUpstream(t)
	noToken := *c
	noToken.Token = ""
	if _, err := noToken.Sessions(context.Background()); err == nil {
		t.Fatal("没有 token 时不该拿得到会话列表")
	} else if !strings.Contains(err.Error(), "401") {
		t.Errorf("期望 401，实际是：%v", err)
	}
}

// 清单之外的子命令，即使上游真有，也不许通过本包跑。
//
// 例子选的是 `session stop`：上游确实有这条命令，而它正是白名单要挡住的那类——
// Work Deck 没有停掉别人会话的道理。
//
// 这里原先拿 `list` 当例子，attempt 7 把 `list` 放进白名单之后就不能用了。
// 换例子的时候顺手把语义也挑得更准：挡住一条无害的只读命令没什么说服力，
// 挡住一条能停掉别人会话的命令才是这条白名单的意义。
func TestIntegrationRefusedSubcommandNeverReachesUpstream(t *testing.T) {
	c, _ := liveUpstream(t)
	ctx := context.Background()

	// 二级子命令：session 在白名单里有 attach / output / send，但没有 stop。
	if _, err := c.exec(ctx, "session", "stop", "无所谓什么会话号"); err == nil {
		t.Fatal("session stop 必须被拒绝，哪怕上游真的支持它")
	}
	// 一级子命令：上游有 doctor，本包没理由跑它。
	if _, err := c.exec(ctx, "doctor"); err == nil {
		t.Fatal("清单之外的一级子命令必须被拒绝")
	}
}

// 服务的菜单缓存看不见刚 launch 的会话，而 CLI list 看得见。
//
// 这条是按 T 直连那条路的地基。attempt 6 的直连只从 /api/menu 取目标，
// 而独立评审指出：菜单恰好是那个被缓存的接口，所以直连在它自己宣称的
// 主场景（服务看不见新会话）下必然失败。这里把两条来源的差异钉死，
// 并证明 DirectAttachCommand 在那个场景下真的还能造出命令来。
//
// 上游哪天把缓存修好了，这个测试会失败——那正是该回来更新依赖清单、
// 并重新考虑直连是否还需要那条降级的时候。
func TestIntegrationListSeesWhatTheCachedMenuCannot(t *testing.T) {
	c, _ := liveUpstream(t)
	ctx := context.Background()

	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte("# 夹具项目\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	title := "n037it-" + randomHex(t, 3)

	res, err := c.Launch(ctx, LaunchRequest{
		ProjectPath: project, Title: title, Group: "n037it",
		Tool: "shell", ModelNotApplicable: true, NoWait: true,
	})
	if err != nil {
		t.Fatalf("派工失败：%v", err)
	}
	if res.SessionID == "" {
		t.Fatalf("上游没有回传会话号：%+v", res)
	}

	// 给足时间，确认下面看不到不是时序问题。
	time.Sleep(3 * time.Second)

	// 一、菜单看不到它。这是上游的已知约束，不是我们的 bug。
	menu, err := c.MenuSnapshot(ctx)
	if err != nil {
		t.Fatalf("取菜单失败：%v", err)
	}
	if _, _, ok := menu.TmuxTargetOf(res.SessionID); ok {
		t.Fatalf("上游行为变了：菜单现在能看到 CLI 刚 launch 的会话 %s。"+
			"这是好事，但依赖清单与直连那条路的降级逻辑要一起更新", res.SessionID)
	}

	// 二、list 看得到，而且给出了 tmux 会话名。
	listed, err := c.ListSessions(ctx)
	if err != nil {
		t.Fatalf("list --json 失败：%v", err)
	}
	tmuxSession, ok := TmuxSessionOf(listed, res.SessionID)
	if !ok {
		t.Fatalf("list 里也没有会话 %s 的 tmux 会话名：直连那条路没有第二个来源了", res.SessionID)
	}
	if tmuxSession == "" {
		t.Fatal("list 给出的 tmux 会话名是空的")
	}

	// 三、于是直连在这个场景下仍然能造出命令，并且明说自己降级了。
	cmd, note, err := c.DirectAttachCommand(ctx, res.SessionID)
	if err != nil {
		t.Fatalf("菜单看不见时直连也失败了，这条路就白加了：%v", err)
	}
	if cmd == nil {
		t.Fatal("直连没有报错，却也没给出命令")
	}
	if note == "" {
		t.Error("走了降级来源却一声不吭：降级必须说出来")
	}
	if !strings.Contains(strings.Join(cmd.Args, " "), tmuxSession) {
		t.Errorf("直连命令没指向 list 给出的那个 tmux 会话：%v", cmd.Args)
	}
	// list 拿不到 socket 名，所以这条命令不该带 -L。
	for _, a := range cmd.Args {
		if a == "-L" {
			t.Errorf("list 没有 socket 名，命令里却出现了 -L：%v", cmd.Args)
		}
	}
}
