package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/binding"
	"github.com/Tingschen287/terminal-deck/work/internal/routing"
)

// 交付物 routing-decisions.jsonl 的生成器：夹具上经真实派工器跑出审计记录。
// 设 WORKDECK_ROUTING_SAMPLE_OUT 时把记录拷到那个路径；不设时照样核对五种情形。
// 模型名与能力声明都是夹具数据，不代表真实 CLI。
func TestRoutingSampleRecords(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	obsAt := now.Add(-time.Hour)
	both := map[string]string{"resume": routing.Yes, "mcp": routing.Yes}
	tbl, err := NewRouteTable([]Model{
		{ID: "fixture-frontier-a", Tool: "claude", Class: Frontier, Caps: both},
		{ID: "fixture-frontier-b", Tool: "codex", Class: Frontier, Caps: map[string]string{"resume": routing.Yes, "mcp": routing.No}},
		{ID: "fixture-frontier-c", Tool: "opencode", Class: Frontier},
		{ID: "fixture-balanced-d", Tool: "kimi", Class: Balanced, Caps: both},
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	store, err := binding.Open(filepath.Join(dir, "bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "routing-decisions.jsonl")
	caps := []string{"resume", "mcp"}
	d := &Dispatcher{
		Deck:  &fakeLauncher{res: agentdeck.LaunchResult{SessionID: "fixture-sess", Success: true}},
		Store: store, Table: tbl, LogPath: filepath.Join(dir, "dispatch-log.jsonl"), RoutingLog: logPath,
		NodeCaps: map[string][]string{"fixture_escalate_unknown_quota": caps, "fixture_recommend_known": caps},
		Now:      func() time.Time { return now },
	}
	observed := routing.Observed{Obs: []routing.Observation{
		{Model: "fixture-frontier-a", State: routing.QuotaAvailable, At: obsAt, Source: "夹具：Work Deck 读到会话 s-a 正常完成"},
		{Model: "fixture-frontier-b", State: routing.QuotaExhausted, At: obsAt, Source: "夹具：Work Deck 读到会话 s-b 卡在额度上（substate=usage-limit）"},
	}}
	run := func(node, model, reason string, q routing.QuotaSource) error {
		d.Quota = q
		d.Deck.(*fakeLauncher).res.SessionID = node + "-sess"
		_, err := d.Dispatch(context.Background(), Request{
			NodeID: node, Attempt: 1, RequiredClass: Frontier, ModelID: model, ProjectPath: "/tmp/fixture", Message: "继续",
			DispatchedBy: "work-deck", ChosenBy: "owner", ChoiceReason: reason,
		})
		return err
	}
	// 1. 没有额度观测：不推荐，升级给 owner；owner 选 a 并写理由。
	if err := run("fixture_escalate_unknown_quota", "fixture-frontier-a", "a 的 resume 与 mcp 都已声明，只差额度观测；本次按 a 派（夹具理由）", nil); err != nil {
		t.Fatal(err)
	}
	// 2. a 所有因子已知且满足：推荐 a，owner 采纳，不用写理由。
	if err := run("fixture_recommend_known", "fixture-frontier-a", "", observed); err != nil {
		t.Fatal(err)
	}
	// 3. owner 不采纳推荐的 a、改选 c：理由记进审计。
	if err := run("fixture_owner_overrides", "fixture-frontier-c", "想看 opencode 在这类节点上的表现（夹具理由）", observed); err != nil {
		t.Fatal(err)
	}
	// 4. 改选不写理由：拒绝派单，不写路由记录。
	if err := run("fixture_override_no_reason", "fixture-frontier-c", "", observed); err == nil || !strings.Contains(err.Error(), "写明理由") {
		t.Fatalf("改选不写理由必须被拒：%v", err)
	}
	// 5. 选被排除的 b（声明不具备 mcp、额度耗尽）：拒绝派单，不写路由记录。
	if err := run("fixture_recommend_known_b", "fixture-frontier-b", "就要 b", observed); err == nil {
		t.Fatal("选被排除的候选必须被拒")
	}
	d.NodeCaps["fixture_recommend_known_b"] = caps
	if err := run("fixture_recommend_known_b", "fixture-frontier-b", "就要 b", observed); err == nil || !strings.Contains(err.Error(), "不可选") {
		t.Fatalf("选声明不具备 mcp 的 b 必须被拒：%v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("应当恰好 3 条记录（被拒的不写），实际 %d 条", len(lines))
	}
	for i, want := range []string{"本次按 a 派", "采纳了推荐", "改选 fixture-frontier-c；理由：想看 opencode"} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("第 %d 条记录缺 %q：%s", i+1, want, lines[i])
		}
	}
	if out := os.Getenv("WORKDECK_ROUTING_SAMPLE_OUT"); out != "" {
		if err := os.WriteFile(out, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
