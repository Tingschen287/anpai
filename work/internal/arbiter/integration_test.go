package arbiter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/lease"
)

// 真实上游：起一个不带模型的 shell 会话，接管前投得进去，接管期间投不进去，
// 会话画面里只出现接管前那一条。没设 WORKDECK_AGENTDECK_BIN 就跳过；测试结束删掉会话。
func TestIntegrationOwnerTakeoverBlocksRealDelivery(t *testing.T) {
	bin := os.Getenv("WORKDECK_AGENTDECK_BIN")
	if bin == "" {
		t.Skip("跳过：没设 WORKDECK_AGENTDECK_BIN")
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	b := make([]byte, 3)
	rand.Read(b)
	profile := "_n039it" + hex.EncodeToString(b)
	c, err := agentdeck.NewClient(bin, profile, "", "")
	if err != nil {
		t.Fatal(err)
	}
	leases, _ := lease.Open(filepath.Join(t.TempDir(), "leases.json"))
	t.Cleanup(leases.Close)
	d := Deck{Client: c, Leases: leases, Clients: c}
	ctx := context.Background()
	res, err := d.Launch(ctx, agentdeck.LaunchRequest{ProjectPath: t.TempDir(), Title: "n039it-lease", Tool: "shell", ModelNotApplicable: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd := exec.Command(bin, "remove", res.SessionID)
		cmd.Env = append(os.Environ(), "AGENTDECK_PROFILE="+profile)
		_ = cmd.Run()
		out, _ := exec.Command("tmux", "ls", "-F", "#{session_name}").Output()
		for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if strings.Contains(name, "n039it-lease") {
				_ = exec.Command("tmux", "kill-session", "-t", "="+name).Run()
			}
		}
	})
	// 等 shell 就绪再投，这段时间不读画面：实测投递前反复 session output 会让上游的「已提交」确认失败。
	time.Sleep(3 * time.Second)
	// 文本用与 n038 集成测试相同的「继续」：上游判「已提交」看的是正文还在不在输入区，
	// shell 的「xxx: command not found」回显会干扰这个判断，某些 ASCII 单词实测会被误报未提交（issue #1793）。
	// 对 shell 会话，上游总是回 delivery=unverified、submitted=false（它确认不了 shell 有没有接这一轮），
	// Work Deck 照实报「送达未确认」（n042）。是否真的到了，由下面读画面核对。
	if err := d.SendMessage(ctx, res.SessionID, "继续"); err != nil && !agentdeck.IsDeliveryUnconfirmed(err) {
		t.Fatal(err)
	}
	leases.OwnerTakeover(res.SessionID, "upstream")
	if err := d.SendMessage(ctx, res.SessionID, "停下"); err == nil {
		t.Fatal("接管期间投递必须被拒")
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
		t.Fatalf("接管前那一条应当到了：%q", pane)
	}
	if strings.Contains(pane, "停下") {
		t.Fatalf("接管期间那一条不该出现在会话里：%q", pane)
	}

	// 不经 Work Deck 的接管：交还后，用一个真实 tmux 客户端直接连上这个会话，
	// 自动投递必须查到它、登记为 owner 接管、不投递。
	leases.OwnerReturn(res.SessionID, lease.Handback{})
	if err := leases.ConfirmHandback(res.SessionID); err != nil {
		t.Fatal(err)
	}
	listed, err := c.ListSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tmuxName, ok := agentdeck.TmuxSessionOf(listed, res.SessionID)
	if !ok {
		t.Fatal("list 里没有这个会话的 tmux 会话名")
	}
	client := exec.Command("script", "-qfc", "tmux attach -t '="+tmuxName+"'", "/dev/null")
	client.Env = os.Environ()
	for i, kv := range client.Env {
		if strings.HasPrefix(kv, "TMUX=") {
			client.Env[i] = "TMUX_UNSET_FOR_TEST="
		}
	}
	if err := client.Start(); err != nil {
		t.Skip("起不了外部 tmux 客户端（没有 script？）：", err)
	}
	t.Cleanup(func() { _ = client.Process.Kill(); _ = client.Wait() })
	// 按截止时间等，不按次数：全量并行跑时机器忙，每次探测本身就要几秒（attempt 3 全量跑时 40 次 × 250ms 不够）。
	attached := false
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); {
		if n, err := c.AttachedClients(ctx, res.SessionID); err == nil && n > 0 {
			attached = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !attached {
		t.Fatal("外部客户端没连上，测不了")
	}
	err = d.SendMessage(ctx, res.SessionID, "外部")
	if err == nil || !strings.Contains(err.Error(), "tmux 客户端连着会话") {
		t.Fatalf("有外部客户端连着时必须拒绝：%v", err)
	}
	if l, _ := leases.Get(res.SessionID); l.Holder != lease.HolderOwner {
		t.Errorf("外部接入应当登记为 owner 接管：%+v", l)
	}
}
