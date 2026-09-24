package queue

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
)

// 这组测试用真实的 upstream agent-deck 二进制跑夹具。没设 WORKDECK_AGENTDECK_BIN 就跳过。
//
// 数据目录一律指到临时的 XDG_DATA_HOME，不碰真实的 ~/.local/share/agent-deck；
// 不启动 notify-daemon，心跳文件由测试自己写。

func upstreamBin(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("WORKDECK_AGENTDECK_BIN")
	if bin == "" {
		t.Skip("跳过：没设 WORKDECK_AGENTDECK_BIN，不跑真实上游")
	}
	return bin
}

func repoTestdata(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata")
}

// fixtureDataHome 把 inbox 夹具拷进一个临时的 XDG_DATA_HOME，返回 agent-deck 数据目录。
func fixtureDataHome(t *testing.T) string {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	dst := filepath.Join(xdg, "agent-deck")
	src := filepath.Join(repoTestdata(t), "inbox-fixture")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if d.Name() == "README.md" {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func writeHeartbeat(t *testing.T, dataDir string, at time.Time) {
	t.Helper()
	p := filepath.Join(dataDir, "runtime", "notify-daemon.heartbeat")
	if err := os.WriteFile(p, []byte(at.UTC().Format(time.RFC3339Nano)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixtureProfile 是 inbox 夹具记录里写的 profile；读夹具的客户端用它。
const fixtureProfile = "_n038fixture"

func testProfile(t *testing.T) string {
	b := make([]byte, 3)
	rand.Read(b)
	return "_n038it" + hex.EncodeToString(b)
}

func treeDigest(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		data, _ := os.ReadFile(p)
		h.Write([]byte(rel))
		h.Write(data)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))
}

// notify-daemon 没在跑时必须告警。三种心跳都用真实二进制跑：
//   - 没有心跳、心跳 10 分钟前：上游答 not running，告警。
//   - 心跳 40 秒前：上游仍答 running（90 秒窗口内），Work Deck 按自己的 15 秒门槛告警——daemon 刚退出就是这样。
//   - 心跳新鲜：看进程表。本测试不启动 daemon，本机通常没有 daemon 进程，必须告警。
//     本机恰好有 daemon 时，这一步模拟不出「本目录的 daemon 已退出」（进程表分不出数据目录），
//     不做断言，只记一笔——那正是 test-report 写明的剩余窗口。
//
// 上游 writer-status 不论结果退出码都是 0，只能看 running 字段。
func TestIntegrationWriterStatusDrivesTheAlert(t *testing.T) {
	bin := upstreamBin(t)
	data := fixtureDataHome(t)
	c, err := agentdeck.NewClient(bin, fixtureProfile, "", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	hostDaemon, err := c.NotifyDaemonRunning(ctx)
	if err != nil {
		t.Fatal(err)
	}

	in, recs := Poll(ctx, c, time.Now())
	if in.Writer.Running || in.Alert() == "" {
		t.Fatalf("没有心跳时必须告警：running=%v alert=%q", in.Writer.Running, in.Alert())
	}
	if len(recs) == 0 {
		t.Error("daemon 不在时，夹具里已有的记录照样该读出来")
	}

	writeHeartbeat(t, data, time.Now().Add(-10*time.Minute))
	in, _ = Poll(ctx, c, time.Now())
	if in.Writer.Running || in.Alert() == "" || in.Writer.AgeSeconds < 500 {
		t.Fatalf("心跳陈旧时必须告警：%+v alert=%q", in.Writer, in.Alert())
	}

	writeHeartbeat(t, data, time.Now().Add(-40*time.Second))
	in, _ = Poll(ctx, c, time.Now())
	if !in.Writer.Running {
		t.Fatalf("心跳 40 秒前，上游应当仍答 running（这正是要补的窗口）：%+v", in.Writer)
	}
	if !strings.Contains(in.Alert(), "秒没更新") {
		t.Fatalf("心跳过了 15 秒门槛必须告警：%q", in.Alert())
	}

	writeHeartbeat(t, data, time.Now())
	in, _ = Poll(ctx, c, time.Now())
	if !in.Writer.Running {
		t.Fatalf("心跳新鲜时 writer-status 应当答 running：%+v", in.Writer)
	}
	if hostDaemon {
		t.Log("本机有 notify-daemon 进程：进程表分不出数据目录，这一步不做断言")
	} else if !strings.Contains(in.Alert(), "进程表里找不到") {
		t.Fatalf("心跳新鲜但本机没有 daemon 进程，必须告警：%q", in.Alert())
	}
}

// export 是非消费式的：读两次拿到同样的记录，数据目录一个字节都不变。
// 夹具里 parent-1 与 _unowned 各有一份相同的 child-a，队列里只留一条。
//
// export 会为记录里的 profile 建 state.db（有文档的副作用），所以只比 inboxes/ 与 runtime/ 两棵子树：
// 那才是记录本身，drain 会动、export 不该动的地方。
func TestIntegrationExportIsNonConsumingAndDeduped(t *testing.T) {
	bin := upstreamBin(t)
	data := fixtureDataHome(t)
	writeHeartbeat(t, data, time.Now())
	c, err := agentdeck.NewClient(bin, fixtureProfile, "", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	digest := func() string {
		return treeDigest(t, filepath.Join(data, "inboxes")) + treeDigest(t, filepath.Join(data, "runtime"))
	}
	before := digest()

	first, err := c.InboxExport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.InboxExport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(second) || len(first) == 0 {
		t.Fatalf("两次读取条数不同或为空：%d vs %d", len(first), len(second))
	}
	if after := digest(); after != before {
		t.Fatal("export 改动了 inbox 或完成账本：它应当不消费、不截断任何东西")
	}

	s, _ := Open(filepath.Join(t.TempDir(), "queue.json"))
	now := time.Now()
	s.Merge(SourceSession, FromInbox(first, now), now)
	s.Merge(SourceSession, FromInbox(second, now), now.Add(time.Minute))
	got := map[string]Category{}
	for _, it := range s.Items() {
		if _, dup := got[it.Source.SessionID]; dup {
			t.Errorf("会话 %s 出现了两条", it.Source.SessionID)
		}
		got[it.Source.SessionID] = it.Category
	}
	want := map[string]Category{"child-a": CatSessionWaiting, "child-b": CatSessionError, "child-c": CatSessionFinished}
	if len(got) != len(want) {
		t.Fatalf("队列条目是 %v，期望 %v（child-d 翻到 idle，不需要人拍板）", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s 的类别是 %s，期望 %s", k, got[k], v)
		}
	}
}

// 回答一条会话条目，选择真的投递进发起会话。
//
// 起一个不带模型的 shell 会话，造一条指向它的等待记录，回答「继续」，
// 再用只读预览看会话画面里出现了这两个字。测试结束收掉会话，不在 tmux 里留垃圾。
func TestIntegrationAnswerReachesTheOriginatingSession(t *testing.T) {
	bin := upstreamBin(t)
	data := fixtureDataHome(t)
	writeHeartbeat(t, data, time.Now())
	profile := testProfile(t)
	c, err := agentdeck.NewClient(bin, profile, "", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	res, err := c.Launch(ctx, agentdeck.LaunchRequest{
		ProjectPath: t.TempDir(), Title: "n038it-answer", Group: "n038it",
		Tool: "shell", ModelNotApplicable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd := exec.Command(bin, "remove", res.SessionID)
		cmd.Env = append(os.Environ(), "AGENTDECK_PROFILE="+profile)
		_ = cmd.Run()
		out, _ := exec.Command("tmux", "ls", "-F", "#{session_name}").Output()
		for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if strings.Contains(name, "n038it-answer") {
				_ = exec.Command("tmux", "kill-session", "-t", "="+name).Run()
			}
		}
	})

	rec := agentdeck.InboxRecord{
		ChildSessionID: res.SessionID, ChildTitle: "n038it-answer", Profile: profile,
		FromStatus: "running", ToStatus: "waiting", Timestamp: time.Now(), LastOutputHash: "h-live",
	}
	s, _ := Open(filepath.Join(t.TempDir(), "queue.json"))
	now := time.Now()
	s.Merge(SourceSession, FromInbox([]agentdeck.InboxRecord{rec}, now), now)
	id := s.Items()[0].ID

	it, err := Respond(ctx, s, c, id, "continue", now)
	if err != nil {
		t.Fatal(err)
	}
	if !it.Answer.Delivered || it.Status != StatusAnswered {
		t.Fatalf("投递没成功：%+v", it.Answer)
	}
	var pane string
	for i := 0; i < 20; i++ {
		pane, err = c.PaneOutputPlain(ctx, res.SessionID)
		if err == nil && strings.Contains(pane, "继续") {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !strings.Contains(pane, "继续") {
		t.Fatalf("会话画面里没看到投递的文字：%q（err=%v）", pane, err)
	}

	// 别的 profile 的会话不代投。
	other := rec
	other.Profile = "_someone_else"
	if err := c.DeliverAnswer(ctx, other, "继续"); err == nil {
		t.Error("别的 profile 的会话应当拒绝投递")
	}
}
