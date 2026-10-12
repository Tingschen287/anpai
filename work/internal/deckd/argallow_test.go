package deckd

import (
	"strings"
	"testing"
)

// n051 attempt 3（owner 选 R1-A）：启动后按参数白名单核对；owner 在 deck 设置里逐字声明的额外参数随启动传给核对，
// 明确的绕过参数声明不进来。

func setExtraArgs(t *testing.T, w *agentWorld, agent string, extra [][]string) {
	t.Helper()
	set := w.store.Settings()
	for i := range set.Agents {
		if set.Agents[i].ID == agent {
			set.Agents[i].ExtraArgs = extra
		}
	}
	if err := w.store.ReplaceSettings(set); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchPassesDeclaredExtraArgsToCheck(t *testing.T) {
	w := newAgentWorld(t)
	extra := [][]string{{"--allowedTools", "Read,Write"}}
	setExtraArgs(t, w, "claude", extra)
	spawnedSub(t, w)
	if len(w.procWant) != 1 {
		t.Fatalf("启动后核对一次：%+v", w.procWant)
	}
	got := w.procWant[0]
	if got.Tool != "claude" || got.Binary == "" || len(got.Extra) != 1 || got.Extra[0][1] != "Read,Write" {
		t.Fatalf("核对依据带上工具、可执行文件与 owner 声明的额外参数：%+v", got)
	}
}

func TestDeclaredBypassExtraArgsRefused(t *testing.T) {
	for _, extra := range [][][]string{
		{{"--dangerously-skip-permissions"}},
		{{"--permission-mode", "bypassPermissions"}},
		{{"-c", "approval_policy=never"}},
		{{"-c", `approval_policy="never"`}},
		{{}},
	} {
		w := newAgentWorld(t)
		setExtraArgs(t, w, "claude", extra)
		code, b := w.do("main", "POST", "/v1/agents", spawnJSON("balanced", "写测试", "t", "xa1", "/tmp/alpha", "", "claude", "claude-sonnet-5"))
		if code != 400 {
			t.Errorf("声明了绕过参数 %q：硬拒绝：%d %s", extra, code, b)
		}
		if strings.Contains(b, "session") && len(w.up.launches) != 0 {
			t.Errorf("不该启动：%q", extra)
		}
	}
}
