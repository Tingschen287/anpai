package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// TestE2ESeed 只在 n049 的隔离演示里用（DECK_E2E_SEED=1，且 XDG 目录指向演示用的临时目录）：
// 往 deck 的注册表里放一个演示项目（一个主 agent、两个子 agent），它们的 tmux 会话由演示脚本自己建，
// 里面跑普通 shell，不调用任何模型。主 agent 的 brief 标为已确认（v1 里由 n051 的启动流程确认）。
func TestE2ESeed(t *testing.T) {
	if os.Getenv("DECK_E2E_SEED") != "1" {
		t.Skip("只在 n049 隔离演示里用")
	}
	if home, _ := os.UserHomeDir(); !strings.Contains(os.Getenv("XDG_STATE_HOME"), "e2e") || !strings.Contains(home, "e2e") {
		t.Fatal("拒绝在非演示目录写注册表")
	}
	dirs, err := deckstore.DefaultDirs()
	if err != nil {
		t.Fatal(err)
	}
	st, err := deckstore.Open(dirs)
	if err != nil {
		t.Fatal(err)
	}
	set := st.Settings()
	set.Projects = []deckstore.ProjectDef{{ID: "demo", Name: "演示项目", Path: os.Getenv("HOME"),
		Agents: []string{"claude", "codex", "kimi"}, Models: map[deckstore.Tier]deckstore.ModelList{deckstore.Frontier: {"claude-opus-5-5"}},
		MainAgent: deckstore.MainAgent{Agent: "claude", Tier: deckstore.Frontier}, Envelope: deckstore.DefaultEnvelope("demo")}}
	if err := st.ReplaceSettings(set); err != nil {
		t.Fatal(err)
	}
	type seed struct {
		Name, Role, Tool, Title string
		ID, Tmux                string // 真实 agent-deck 会话的 id 与 tmux 会话名（真实 CLI 复验用）；空则按演示命名
		Brief                   bool   // 主 agent 的 brief 是否已确认
	}
	seeds := []seed{
		{Name: "main", Role: "main", Tool: "claude", Title: "主 agent", Brief: true},
		{Name: "sub1", Role: "sub", Tool: "codex", Title: "写测试"},
		{Name: "sub2", Role: "sub", Tool: "kimi", Title: "查资料"},
	}
	if raw := os.Getenv("DECK_E2E_SESSIONS"); raw != "" {
		seeds = nil
		if err := json.Unmarshal([]byte(raw), &seeds); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range seeds {
		x := struct{ name, role, tool, title string }{s.Name, s.Role, s.Tool, s.Title}
		id, tmux := "ad-"+s.Name, "agentdeck_demo_"+s.Name
		if s.ID != "" {
			id, tmux = s.ID, s.Tmux
		}
		r, err := st.Reserve(deckstore.Session{Project: "demo", Role: x.role, Agent: x.tool, Tool: x.tool, Model: "m", Tier: deckstore.Economy,
			Title: x.title, CreatedBy: "owner", RequestID: "e2e-" + x.name, Dir: os.Getenv("HOME"),
			TaskSHA256: strings.Repeat("a", 64), ArgsSHA256: strings.Repeat("b", 64)}, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := st.LaunchSucceeded(r.Key, id, tmux); err != nil {
			t.Fatal(err)
		}
		if x.role == "main" && s.Brief {
			if err := st.ConfirmBrief(r.Key); err != nil {
				t.Fatal(err)
			}
		}
	}
}
