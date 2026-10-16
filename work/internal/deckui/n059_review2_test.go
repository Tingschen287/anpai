package deckui

// 评审 2（Codex gpt-6.1-sol）的探针，收为回归测试（n059 attempt 3）：固定标题限高（B1）、选中的候选在底部溢出时仍可见（已知问题 1）。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Every simulated key has a preceding View, as in the real Bubble Tea loop.
func review2Key(m *Settings, k string) string {
	plainView(m)
	switch k {
	case "pgdown":
		_, c := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		run(m, c)
	case "pgup":
		_, c := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
		run(m, c)
	default:
		press(m, k)
	}
	return plainView(m)
}

func review2Bounds(t *testing.T, m *Settings, v string) {
	t.Helper()
	if n := len(strings.Split(v, "\n")); n > m.height {
		t.Fatalf("%dx%d: rendered %d lines\n%s", m.width, m.height, n, v)
	}
	for _, l := range strings.Split(v, "\n") {
		if w := ansi.StringWidth(l); w > m.width {
			t.Fatalf("%dx%d: rendered %d cells: %q", m.width, m.height, w, l)
		}
	}
}

func TestReview2Layout(t *testing.T) {
	t.Run("all_models_grid", func(t *testing.T) {
		for _, size := range [][2]int{{80, 24}, {81, 25}, {100, 24}, {100, 30}, {119, 24}, {140, 40}} {
			m, _ := n057Fixture()
			m.width, m.height = size[0], size[1]
			for ai := range m.set.Agents {
				a := &m.set.Agents[ai]
				for i := 0; i < 35; i++ {
					id := fmt.Sprintf("probe-a%d-m%02d-%s-tail%02d", ai, i, strings.Repeat("a", 100), i)
					a.Models = append(a.Models, deckstore.ModelDef{ID: id, Tier: deckstore.Frontier})
				}
			}
			if err := m.set.Validate(); err != nil {
				t.Fatal(err)
			}
			if err := m.set.CheckModelIDs(); err != nil {
				t.Fatal(err)
			}
			if err := m.set.CheckMainAgents(); err != nil {
				t.Fatal(err)
			}
			review2Key(m, "2")
			seen := ""
			for i := 0; i < 120; i++ {
				v := plainView(m)
				review2Bounds(t, m, v)
				if !strings.Contains(v, "›") {
					t.Fatalf("cursor missing at %v, cur=%d", size, m.cur)
				}
				seen += v
				review2Key(m, "down")
			}
			for ai := range m.set.Agents {
				for i := 0; i < 35; i++ {
					for _, marker := range []string{fmt.Sprintf("probe-a%d-m%02d-", ai, i), fmt.Sprintf("-tail%02d", i)} {
						if !strings.Contains(seen, marker) {
							t.Fatalf("%v: missing %s", size, marker)
						}
					}
				}
			}
		}
	})
	t.Run("long_project_title", func(t *testing.T) {
		m, _ := n057Fixture()
		m.width, m.height = 80, 24
		m.project().Name = strings.Repeat("项", 900)
		if err := m.set.Validate(); err != nil {
			t.Fatal(err)
		}
		if err := m.set.CheckMainAgents(); err != nil {
			t.Fatal(err)
		}
		initial := len(strings.Split(plainView(m), "\n"))
		paged := len(strings.Split(review2Key(m, "pgdown"), "\n"))
		if initial > m.height || paged > m.height {
			t.Fatalf("80x24, valid 900-rune project name: initial=%d lines, after PgDn=%d lines", initial, paged)
		}
	})
	t.Run("long_agent_title", func(t *testing.T) {
		m, _ := n057Fixture()
		m.width, m.height = 80, 24
		m.agentPtr("claude").Name = strings.Repeat("代", 900)
		if err := m.set.Validate(); err != nil {
			t.Fatal(err)
		}
		if err := m.set.CheckMainAgents(); err != nil {
			t.Fatal(err)
		}
		goRow(t, m, rowGlobal, "claude", "")
		initial := len(strings.Split(review2Key(m, "enter"), "\n"))
		paged := len(strings.Split(review2Key(m, "pgdown"), "\n"))
		if initial > m.height || paged > m.height {
			t.Fatalf("80x24, valid 900-rune agent name: initial=%d lines, after PgDn=%d lines", initial, paged)
		}
	})
	t.Run("browse_long_real_directory", func(t *testing.T) {
		dir := t.TempDir()
		for i := 0; i < 15; i++ {
			dir = filepath.Join(dir, strings.Repeat("目", 60))
		}
		if err := os.MkdirAll(filepath.Join(dir, "child"), 0o755); err != nil {
			t.Fatal(err)
		}
		m, _ := n057Fixture()
		m.width, m.height, m.Start = 80, 24, dir
		review2Bounds(t, m, review2Key(m, "n"))
		review2Bounds(t, m, review2Key(m, "down"))
	})
}

func TestReview2KeyRouting(t *testing.T) {
	t.Run("input_jk_and_tier_KJ", func(t *testing.T) {
		m, api := n057Fixture()
		m.width, m.height = 80, 24
		goRow(t, m, rowGlobal, "claude", "")
		review2Key(m, "enter")
		review2Key(m, "+")
		review2Key(m, "j")
		review2Key(m, "k")
		if m.sub.input.text != "jk" {
			t.Fatalf("j/k swallowed: %+v", m.sub.input)
		}
		review2Key(m, "esc")
		review2Key(m, "esc")
		goRow(t, m, rowTier, "", deckstore.Frontier)
		review2Key(m, "enter")
		review2Key(m, "down")
		review2Key(m, "enter")
		before := append(deckstore.ModelList(nil), m.project().Models[deckstore.Frontier]...)
		review2Key(m, "K")
		if len(before) != 2 || m.project().Models[deckstore.Frontier][0] != before[1] {
			t.Fatal("K did not reorder")
		}
		review2Key(m, "J")
		if m.project().Models[deckstore.Frontier][0] != before[0] || m.dirty || len(api.saved) != 3 {
			t.Fatal("J or autosave regressed")
		}
	})
	t.Run("form_arrows_after_tall_block", func(t *testing.T) {
		m, _ := n057Fixture()
		m.width, m.height = 80, 24
		for i := 0; i < 40; i++ {
			m.agentPtr("claude").Models = append(m.agentPtr("claude").Models, deckstore.ModelDef{ID: fmt.Sprintf("long-%02d-%s", i, strings.Repeat("a", 40)), Tier: deckstore.Frontier})
		}
		review2Key(m, "2")
		review2Key(m, "down")
		m.Start = t.TempDir()
		if err := os.Mkdir(filepath.Join(m.Start, "child"), 0o755); err != nil {
			t.Fatal(err)
		}
		review2Key(m, "n")
		review2Key(m, "down")
		if m.form.cur != 1 {
			t.Fatal("form down swallowed")
		}
		review2Key(m, "up")
		if m.form.cur != 0 {
			t.Fatal("form up swallowed")
		}
	})
	t.Run("scroll_cancels_remove_and_revoke", func(t *testing.T) {
		m, api := n057Fixture()
		m.width, m.height = 80, 24
		m.project().Agents = []string{"claude"}
		m.agentPtr("codex").ExtraArgs = [][]string{{"-c", "sandbox_workspace_write.network_access=true"}}
		goRow(t, m, rowGlobal, "codex", "")
		review2Key(m, "enter")
		review2Key(m, "r")
		if !m.sub.confirm {
			t.Fatal("remove confirmation missing")
		}
		review2Key(m, "pgdown")
		review2Key(m, "r")
		if m.agentPtr("codex") == nil || !m.sub.confirm {
			t.Fatal("scroll accepted remove confirmation")
		}
		review2Key(m, "r")
		if m.agentPtr("codex") != nil {
			t.Fatal("second remove failed")
		}
		m.trust = []deckstore.TrustGrant{{Project: "ai", Tool: "codex", Dir: "/tmp/ai"}}
		review2Key(m, "3")
		review2Key(m, "d")
		review2Key(m, "pgup")
		review2Key(m, "d")
		if len(api.revoked) != 0 || m.revokeAsked == nil {
			t.Fatal("scroll accepted revoke confirmation")
		}
		review2Key(m, "d")
		if len(api.revoked) != 1 {
			t.Fatal("second revoke failed")
		}
	})
	t.Run("candidate_cursor_after_footer_overflow", func(t *testing.T) {
		m, api := n057Fixture()
		m.width, m.height = 80, 24
		api.tools[0].CanList = true
		for i := 0; i < 12; i++ {
			api.models["claude"] = append(api.models["claude"], fmt.Sprintf("candidate-%02d-%s", i, strings.Repeat("x", 100)))
		}
		api.fail = errors.New(strings.Repeat("存不上；", 140)) // 560 runes, within errLong's cap.
		goRow(t, m, rowAgentPick, "codex", "")
		review2Key(m, " ")
		goRow(t, m, rowGlobal, "claude", "")
		review2Key(m, "enter")
		review2Key(m, "+")
		for i := 0; i < 12; i++ {
			v := review2Key(m, "down")
			review2Bounds(t, m, v)
			if m.sub.input.cur != i {
				t.Fatalf("candidate index=%d, wanted %d", m.sub.input.cur, i)
			}
			if !strings.Contains(v, fmt.Sprintf("› candidate-%02d-", i)) {
				paged := review2Key(m, "pgdown")
				if !strings.Contains(paged, fmt.Sprintf("› candidate-%02d-", i)) {
					t.Fatal("selected candidate also unreachable with PgDn")
				}
				after := review2Key(m, "down")
				t.Fatalf("candidate %d invisible after Down; PgDn reveals it; next Down selects %d and resets off=%d (cursor visible=%v)", i, m.sub.input.cur, m.scr.off, strings.Contains(after, fmt.Sprintf("› candidate-%02d-", m.sub.input.cur)))
			}
		}
	})
}
