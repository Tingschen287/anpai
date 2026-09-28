package arbiter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/lease"
)

// n040 终审断点 2：Work Deck 自己跑在 tmux 里时（$TMUX 指向外层 server），派出的上游会话不能落到外层 server、
// 也不能因此退出；投递前的客户端探测要找得到它，任务文本要真的送到。
// 复现法：起一个私有 tmux server，把本进程的 TMUX 指向它，再经真实上游派一个 shell 会话。
func TestIntegrationDispatchWorksFromInsideAnotherTmuxServer(t *testing.T) {
	bin := os.Getenv("WORKDECK_AGENTDECK_BIN")
	if bin == "" {
		t.Skip("跳过：没设 WORKDECK_AGENTDECK_BIN")
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	b := make([]byte, 3)
	rand.Read(b)
	tag := hex.EncodeToString(b)
	host := "n042nest" + tag
	clean := func(args ...string) *exec.Cmd {
		cmd := exec.Command("tmux", args...)
		var env []string
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "TMUX=") && !strings.HasPrefix(kv, "TMUX_PANE=") {
				env = append(env, kv)
			}
		}
		cmd.Env = env
		return cmd
	}
	if out, err := clean("-L", host, "new-session", "-d", "-s", "outer", "sleep 300").CombinedOutput(); err != nil {
		t.Skipf("起不了私有 tmux server：%v %s", err, out)
	}
	t.Cleanup(func() { _ = clean("-L", host, "kill-server").Run() })
	sock, err := clean("-L", host, "display-message", "-p", "-t", "outer", "#{socket_path},#{pid},0").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX", strings.TrimSpace(string(sock)))
	t.Setenv("TMUX_PANE", "%0")

	profile := "_n042it" + tag
	c, err := agentdeck.NewClient(bin, profile, "", "")
	if err != nil {
		t.Fatal(err)
	}
	leases, _ := lease.Open(filepath.Join(t.TempDir(), "leases.json"))
	t.Cleanup(leases.Close)
	d := Deck{Client: c, Leases: leases, Clients: c}
	ctx := context.Background()
	title := "n042it-nest" + tag
	res, err := d.Launch(ctx, agentdeck.LaunchRequest{ProjectPath: t.TempDir(), Title: title, Tool: "shell", ModelNotApplicable: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd := exec.Command(bin, "remove", res.SessionID)
		cmd.Env = append(os.Environ(), "AGENTDECK_PROFILE="+profile)
		_ = cmd.Run()
		for _, srv := range [][]string{{}, {"-L", host}} {
			out, _ := clean(append(srv, "ls", "-F", "#{session_name}")...).Output()
			for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if strings.Contains(name, title) {
					_ = clean(append(srv, "kill-session", "-t", "="+name)...).Run()
				}
			}
		}
	})
	time.Sleep(3 * time.Second)

	// 会话必须在默认 server 上，不在外层私有 server 上。
	onServer := func(srv ...string) bool {
		out, _ := clean(append(srv, "ls", "-F", "#{session_name}")...).Output()
		return strings.Contains(string(out), title)
	}
	if onServer("-L", host) {
		t.Fatalf("上游会话落到了外层私有 server %s 上（继承了 TMUX）", host)
	}
	if !onServer() {
		t.Fatal("默认 server 上找不到上游派出的会话")
	}
	if n, err := c.AttachedClients(ctx, res.SessionID); err != nil || n != 0 {
		t.Fatalf("投递前探测应当找得到会话且没有客户端：n=%d err=%v", n, err)
	}
	if err := d.SendMessage(ctx, res.SessionID, "继续"); err != nil {
		t.Fatalf("在 tmux 里运行时任务文本应当送得到：%v", err)
	}
	var pane string
	for i := 0; i < 20; i++ {
		pane, _ = c.PaneOutputPlain(ctx, res.SessionID)
		if strings.Contains(pane, "继续") {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !strings.Contains(pane, "继续") {
		t.Fatal(fmt.Sprintf("会话画面里没有送达的文本：%q", pane))
	}
}
