package deckd

import (
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// Claude Code 2.x 目录信任框（文字取自二进制；默认光标在「No, exit」上）。
const claudeTrustScreen = ` Accessing workspace:

 /tmp/gamma

 Quick safety check: Is this a project you created or one you trust? (Like your own code, a well-known
 open source project, or work from your team). If not, take a moment to review what's in this folder first.

   Yes, I trust this folder
 ❯ No, exit

 Enter to confirm · Esc to cancel`

// Claude Code 2.1.296 的输入框（n055 端到端真机画面）：上下两条横线，中间一行是 ❯ 加不换行空格。
const claudeComposerScreen = " ▐▛███▛█   Claude Code v2.1.296\n\n" +
	"──────────────────────────────────────────────────\n❯\u00a0\n──────────────────────────────────────────────────\n" +
	"  Opus 5.5 | ✍️ 0/1.0m (0%) | demo\n  ⏵⏵ auto mode on (shift+tab to cycle)"

func TestStartupDialogRecognition(t *testing.T) {
	cases := []struct {
		screen string
		dialog bool
	}{
		{claudeTrustScreen, true},
		{"Do you trust the files in this folder?\n❯ 1. Yes, proceed\n  2. No, exit", true},
		{"Is this a directory you created or one you trust?", true},
		{"Yes, I trust these settings\nNo, exit Claude Code", true},
		{"> You are in /tmp/x\n\nDo you trust the contents of this directory?\n› 1. Yes, continue\n  2. No, quit", true},
		{"some tool output\n❯ 1. Allow once\n  2. Deny", true}, // 编号选择菜单
		{claudeComposerScreen, false},
		{"› Explain this codebase\n\n  ⏎ send   ⌃J newline", false}, // Codex 输入框
		{"", false},
		{"1. first item\n2. second item", false}, // 普通编号列表，没有光标
	}
	for i, c := range cases {
		if got := StartupDialog(c.screen) != ""; got != c.dialog {
			t.Errorf("第 %d 个画面：识别为确认=%v，应为 %v\n%s", i, got, c.dialog, c.screen)
		}
	}
}

// addGamma 加一个还没有主 agent 的项目 gamma。
func addGamma(t *testing.T, w *agentWorld) {
	t.Helper()
	set := w.store.Settings()
	g := set.Projects[0]
	g.ID, g.Name, g.Path = "gamma", "Gamma", "/tmp/gamma"
	g.Envelope = deckstore.DefaultEnvelope("gamma")
	set.Projects = append(set.Projects, g)
	if err := w.store.ReplaceSettings(set); err != nil {
		t.Fatal(err)
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("等不到：%s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func mainOf(t *testing.T, w *agentWorld, project string) deckstore.Session {
	t.Helper()
	for _, x := range w.store.Sessions(project) {
		if x.Role == "main" {
			return x
		}
	}
	t.Fatalf("%s 没有主 agent", project)
	return deckstore.Session{}
}

// 主 agent 的 CLI 停在目录信任框上：不送 brief、记下等 owner；owner 选完、画面稳定后只送一次，brief 记为已确认。
func TestMainBriefWaitsForTrustDialog(t *testing.T) {
	w := newAgentWorld(t)
	addGamma(t, w)
	w.up.setOutput(claudeTrustScreen)
	before := w.up.sendCount()
	code, b := w.do("owner", "POST", "/v1/projects/gamma/main", `{}`)
	if code != 201 {
		t.Fatalf("起主 agent：%d %s", code, b)
	}
	m := decode(t, b)
	if m["brief_delivered"] != false || !strings.Contains(m["note"].(string), "目录信任确认") {
		t.Fatalf("停在信任框上：不送，说明原因：%v", m)
	}
	x := mainOf(t, w, "gamma")
	if x.AwaitingOwner == "" || x.BriefConfirmed {
		t.Fatalf("应记为等 owner 处理：%+v", x)
	}
	time.Sleep(30 * time.Millisecond)
	if w.up.sendCount() != before {
		t.Fatal("确认框还在：一个字都不该送")
	}
	w.up.setOutput(claudeComposerScreen) // owner 在中间选了「Yes」，CLI 进到输入框
	waitUntil(t, "brief 送达", func() bool { return w.up.sendCount() == before+1 })
	waitUntil(t, "brief 记为已确认", func() bool { y := mainOf(t, w, "gamma"); return y.BriefConfirmed && y.AwaitingOwner == "" })
	time.Sleep(30 * time.Millisecond)
	if w.up.sendCount() != before+1 {
		t.Fatalf("第一条 brief 只送一次：%d", w.up.sendCount()-before)
	}
	w.up.mu.Lock()
	last := w.up.sends[len(w.up.sends)-1]
	w.up.mu.Unlock()
	if !strings.Contains(last, "agent escalate") {
		t.Fatalf("送的是主 agent 的 brief：%s", last)
	}
}

// 对照：画面一开始就是输入框，照旧当场送（与 n051 一致）。
func TestMainBriefSentAtOnceWithoutDialog(t *testing.T) {
	w := newAgentWorld(t)
	addGamma(t, w)
	w.up.setOutput(claudeComposerScreen)
	code, b := w.do("owner", "POST", "/v1/projects/gamma/main", `{}`)
	if code != 201 || decode(t, b)["brief_delivered"] != true {
		t.Fatalf("没有确认框：当场送达：%d %s", code, b)
	}
	if x := mainOf(t, w, "gamma"); !x.BriefConfirmed || x.AwaitingOwner != "" {
		t.Fatalf("%+v", x)
	}
}

// owner 一直不处理：等到上限就停下说明，不送。
func TestMainBriefGivesUpAfterAwait(t *testing.T) {
	w := newAgentWorld(t)
	w.srv.agents.startup.await = 40 * time.Millisecond
	addGamma(t, w)
	w.up.setOutput(claudeTrustScreen)
	before := w.up.sendCount()
	if code, b := w.do("owner", "POST", "/v1/projects/gamma/main", `{}`); code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	waitUntil(t, "超时说明", func() bool { return strings.Contains(mainOf(t, w, "gamma").Note, "仍停在启动确认上") })
	if w.up.sendCount() != before {
		t.Fatal("超时也不该送")
	}
	waitUntil(t, "超时要通知 owner", func() bool {
		for _, n := range w.store.Notifications(0, "gamma") {
			if strings.Contains(n.Text, "仍停在启动确认上") {
				return true
			}
		}
		return false
	})
	// 超时后不放弃（attempt 2 自查）：owner 之后处理了确认，照样送一次、解除启动期
	w.up.setOutput(claudeComposerScreen)
	waitUntil(t, "超时后处理了确认仍送", func() bool { return w.up.sendCount() == before+1 })
	waitUntil(t, "启动期解除", func() bool { x := mainOf(t, w, "gamma"); return x.BriefConfirmed && !x.StartPending() })
}

// 等的时候会话被停掉：确认消失后也不再送。
func TestMainBriefNotSentAfterStop(t *testing.T) {
	w := newAgentWorld(t)
	addGamma(t, w)
	w.up.setOutput(claudeTrustScreen)
	before := w.up.sendCount()
	if code, b := w.do("owner", "POST", "/v1/projects/gamma/main", `{}`); code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	x := mainOf(t, w, "gamma")
	w.store.SetSessionStatus(x.Key, deckstore.StatusStopped, "owner 停掉")
	w.up.setOutput(claudeComposerScreen)
	time.Sleep(60 * time.Millisecond)
	if w.up.sendCount() != before {
		t.Fatal("会话已停：不再送")
	}
}

// 子 agent 的 CLI 停在确认上：不送任务、提示 owner 接管；确认消失后用自动许可送一次。
func TestSubBriefWaitsForDialog(t *testing.T) {
	w := newAgentWorld(t)
	w.up.setOutput("Do you trust the files in this folder?\n❯ 1. Yes, proceed\n  2. No, exit")
	before := w.up.sendCount()
	code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "写测试", "给 foo 写测试", "n055-sub", "/tmp/alpha/sub", "", "claude", "claude-sonnet-5"))
	if code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	m := decode(t, b)
	if m["brief_delivered"] != false || !strings.Contains(m["note"].(string), "F2") {
		t.Fatalf("子 agent 停在确认上：不送、提示接管：%v", m)
	}
	w.up.setOutput(claudeComposerScreen)
	waitUntil(t, "任务送达", func() bool { return w.up.sendCount() == before+1 })
	w.up.mu.Lock()
	last := w.up.sends[len(w.up.sends)-1]
	w.up.mu.Unlock()
	if !strings.Contains(last, "给 foo 写测试") {
		t.Fatalf("送的是任务：%s", last)
	}
	sess := m["session"].(map[string]any)
	waitUntil(t, "等待标记清掉", func() bool { y, _ := w.store.Session(sess["key"].(string)); return y.AwaitingOwner == "" })
}

// n055：设置里声明的额外参数随启动请求传给上游（n051 只当启动后核对的白名单）。
func TestSpawnPassesDeclaredExtraArgs(t *testing.T) {
	w := newAgentWorld(t)
	set := w.store.Settings()
	for i := range set.Agents {
		if set.Agents[i].ID == "claude" {
			set.Agents[i].ExtraArgs = [][]string{{"--allowedTools", "Bash(deck agent:*)"}}
		}
	}
	if err := w.store.ReplaceSettings(set); err != nil {
		t.Fatal(err)
	}
	code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "写测试", "给 foo 写测试", "n055-extra", "/tmp/alpha", "", "claude", "claude-sonnet-5"))
	if code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	lr := w.up.launches[len(w.up.launches)-1]
	if len(lr.Extra) != 1 || lr.Extra[0][0] != "--allowedTools" || lr.Extra[0][1] != "Bash(deck agent:*)" {
		t.Fatalf("启动请求应带声明的参数：%+v", lr.Extra)
	}
	if want := w.procWant[len(w.procWant)-1]; len(want.Extra) != 1 {
		t.Fatalf("启动后核对的白名单照旧：%+v", want)
	}
}
