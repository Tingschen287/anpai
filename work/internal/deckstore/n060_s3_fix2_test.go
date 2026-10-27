package deckstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 段 3 返工 2，M5：不能凭种类把 cost 决定当成系统摘要。
// 主 agent 能提交 cost（deckd 的 agentKinds 含它），所以认摘要是不是系统写的，要看写摘要的证据：
// deckd 开子 agent 时造的 cost 决定带 SpawnScope（action=spawn），而主 agent 带 action=spawn 的请求在入口被拒绝。

func decWith(id, kind, summary string, scope map[string]string) Decision {
	now := time.Now()
	return Decision{ID: id, Project: "", Kind: kind, Summary: summary, Scope: scope, Options: []string{"approve", "reject", "stop"},
		Version: 1, State: StateOpen, CreatedBy: "deckd", CreatedAt: now, ExpiresAt: now.Add(time.Hour), UpdatedAt: now}
}

func TestN060S3Fix2SystemSummaryNeedsProvenance(t *testing.T) {
	spawn := map[string]string{"action": "spawn"}
	cases := []struct {
		name string
		d    Decision
		want bool
	}{
		{"deckd 开子 agent 的 cost（SpawnScope）", decWith("a", KindCost, "x", spawn), true},
		{"主 agent 提交的 cost（没有 action）", decWith("b", KindCost, "x", nil), false},
		{"主 agent 提交的 cost（别的 action）", decWith("c", KindCost, "x", map[string]string{"action": "other"}), false},
		{"Codex 授权（只有 deckd 造得出）", decWith("d", KindCodexTrust, "x", map[string]string{"action": "codex_trust"}), true},
		{"Codex 授权的旧记录没有范围也认（种类外部提交不了）", decWith("e", KindCodexTrust, "x", nil), true},
		{"别的种类", decWith("f", KindPush, "x", spawn), false},
	}
	for _, c := range cases {
		if got := SystemSummary(c.d); got != c.want {
			t.Errorf("%s：SystemSummary=%v，应是 %v", c.name, got, c.want)
		}
	}
}

// Open 重读旧决定：能证实是系统写的 cost 摘要仍然补码；主 agent 的 cost（同一句话）不补，原文原样。
func TestN060S3Fix2OpenBackfillsOnlyProvenSystemCostSummaries(t *testing.T) {
	d := s3Dirs(t)
	sentence := i18n.MustTextZH("err.viewstate.notopen") // 一句能被 LegacyCode 逐字认出的固定句子
	if _, _, ok := i18n.LegacyCode(sentence); !ok {
		t.Fatal("前提：这句话应该能被 LegacyCode 认出")
	}
	sys := decWith("dec-sys", KindCost, sentence, map[string]string{"action": "spawn"})
	ext := decWith("dec-ext", KindCost, sentence, nil)
	ext.CreatedBy = "main-session"
	raw, _ := json.Marshal([]Decision{sys, ext})
	if err := os.WriteFile(filepath.Join(d.State, "decisions.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	gs, _ := st.Decision("dec-sys")
	ge, _ := st.Decision("dec-ext")
	if gs.SummaryCode != "err.viewstate.notopen" || gs.Summary != sentence {
		t.Errorf("真正的旧系统 cost 摘要应补码、原文不变：%+v", gs)
	}
	if ge.SummaryCode != "" || ge.Summary != sentence {
		t.Errorf("主 agent 的 cost 摘要不该补码：%+v", ge)
	}
}
