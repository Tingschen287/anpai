package panels

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 段 3 必改 3（第 2 段复审 4）：旧系统通知与旧决定摘要。
// 码是第 2 段才加的；更早写进磁盘的记录只有中文原文，英文界面读到会原样显示中文。
// 读旧数据（deckstore.Open）时，把码表里的固定句子逐字认出来补上码；认不出的混有外部原文，按兼容策略显示：
// 英文界面整条显示原文、前面加「旧记录」标签（Earlier record），中文界面照旧。

func legacyStore(t *testing.T, notes []deckstore.Notification, decs []deckstore.Decision) *deckstore.Store {
	t.Helper()
	dir := t.TempDir()
	dirs := deckstore.Dirs{Config: filepath.Join(dir, "cfg"), State: filepath.Join(dir, "state")}
	if err := os.MkdirAll(dirs.State, 0o700); err != nil {
		t.Fatal(err)
	}
	var lines []byte
	for _, n := range notes {
		b, _ := json.Marshal(n)
		lines = append(lines, append(b, '\n')...)
	}
	if len(notes) > 0 {
		if err := os.WriteFile(filepath.Join(dirs.State, "notifications.jsonl"), lines, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if len(decs) > 0 {
		b, _ := json.Marshal(decs)
		if err := os.WriteFile(filepath.Join(dirs.State, "decisions.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := deckstore.Open(dirs)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// Codex 复审 4 的反例，名字沿用：viewstate.ErrNotOpen 的固定系统文字，旧形状（无 code/args）。
func TestAuditN060R5LegacyLocalNotification(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	n := deckstore.Notification{Seq: 1, Kind: "view", Source: "deckd", Text: "会话不在打开集合里", At: time.Now()}
	st := legacyStore(t, []deckstore.Notification{n}, nil)
	i18n.SetLang(i18n.EN)
	shown := RenderNotifications(st.Notifications(0, ""), 10)
	if strings.Contains(shown, n.Text) || hasHan(shown) {
		t.Errorf("系统自己的旧通知在 Open 之后英文界面仍是中文：%q", shown)
	}
	if !strings.Contains(shown, "not open") && !strings.Contains(shown, "open set") {
		t.Logf("英文译文：%q", shown)
	}
	// 中文：原文逐字不变
	i18n.SetLang(i18n.ZH)
	if zh := RenderNotifications(st.Notifications(0, ""), 10); !strings.Contains(zh, n.Text) || strings.Contains(zh, "旧记录") {
		t.Errorf("中文界面应原样显示旧通知，不加标签：%q", zh)
	}
}

// 参数是编号、工具名这类标识的旧通知，参数能确定反解；原文不改。
func TestN060S3LegacyNotificationsRecoverIdentifierArguments(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	zhText := i18n.MustTextZH("err.deckd.serverdeletetrust.2")
	enFmt, _ := i18n.TextEN("err.deckd.serverdeletetrust.2")
	n1 := deckstore.Notification{Seq: 1, Kind: "trust", Project: "alpha", Source: "owner", Text: fmt.Sprintf(zhText, "codex"), At: time.Now()}
	n2 := deckstore.Notification{Seq: 2, Kind: "spawn", Project: "alpha", Source: "deckd", Text: fmt.Sprintf(i18n.MustTextZH("err.deckd.serverrunspawn.25"), 3, 2), At: time.Now()}
	st := legacyStore(t, []deckstore.Notification{n1, n2}, nil)
	got := st.Notifications(0, "")
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	for i, want := range []string{n1.Text, n2.Text} {
		if got[i].Text != want {
			t.Errorf("补码不能改原文：%q -> %q", want, got[i].Text)
		}
		if got[i].Code == "" {
			t.Errorf("第 %d 条没有补上码：%+v", i+1, got[i])
		}
	}
	i18n.SetLang(i18n.EN)
	en := RenderNotifications(got, 10)
	if !strings.Contains(en, fmt.Sprintf(enFmt, "codex")) || hasHan(en) || strings.Contains(en, "Earlier record") {
		t.Errorf("英文应是译文、没有汉字、不加旧记录标签：%q", en)
	}
	if !strings.Contains(en, "took over 3 sub-agents and 2 reports") {
		t.Errorf("整数参数没有反解出来：%q", en)
	}
	i18n.SetLang(i18n.ZH)
	zh := RenderNotifications(got, 10)
	if !strings.Contains(zh, n1.Text) || !strings.Contains(zh, n2.Text) {
		t.Errorf("中文应逐字是原文：%q", zh)
	}
}

// 混有外部原文、无法确定反解的旧通知：英文整条显示原文，前面加「旧记录」标签；中文不变。
func TestN060S3LegacyMixedNotificationUsesTheLabel(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	mixed := deckstore.Notification{Seq: 1, Kind: "report", Project: "alpha", Source: "k", Text: "子 agent「写日志」：完成了，共 3 处", At: time.Now()}
	st := legacyStore(t, []deckstore.Notification{mixed}, nil)
	got := st.Notifications(0, "")
	if got[0].Code != "" {
		t.Fatalf("混有外部原文的不该被认成固定句子：%+v", got[0])
	}
	i18n.SetLang(i18n.EN)
	en := RenderNotifications(got, 10)
	if !strings.Contains(en, "[Earlier record] "+mixed.Text) {
		t.Errorf("英文应整条显示原文并加标签：%q", en)
	}
	i18n.SetLang(i18n.ZH)
	zh := RenderNotifications(got, 10)
	if !strings.Contains(zh, mixed.Text) || strings.Contains(zh, "旧记录") || strings.Contains(zh, "Earlier") {
		t.Errorf("中文应原样显示、不加标签：%q", zh)
	}
	// 没有汉字的旧通知（例如当时就是英文原文）不加标签
	plain := deckstore.Notification{Seq: 2, Kind: "report", Source: "k", Text: "all good", At: time.Now()}
	st2 := legacyStore(t, []deckstore.Notification{plain}, nil)
	i18n.SetLang(i18n.EN)
	if s := RenderNotifications(st2.Notifications(0, ""), 10); strings.Contains(s, "Earlier record") {
		t.Errorf("没有汉字的不加标签：%q", s)
	}
}

func legacyDecision(id, kind, summary string) deckstore.Decision {
	now := time.Now()
	return deckstore.Decision{ID: id, Project: "alpha", Kind: kind, Summary: summary, Options: []string{"approve", "reject", "stop"},
		Version: 1, State: deckstore.StateOpen, CreatedBy: "deckd", CreatedAt: now, ExpiresAt: now.Add(time.Hour), UpdatedAt: now}
}

// 旧决定摘要：系统生成的固定句子补码；混有外部原文的加标签；主 agent 自己写的摘要是外部原文，不动。
func TestN060S3LegacyDecisionSummaries(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	trust := legacyDecision("dec-1", deckstore.KindCodexTrust, fmt.Sprintf(i18n.MustTextZH("err.deckd.serverrunspawn.2"), "Codex", "/tmp/alpha"))
	cost := legacyDecision("dec-2", deckstore.KindCost, fmt.Sprintf(i18n.MustTextZH("err.deckd.serverrunspawn.7"), "Claude Code", "frontier", "claude-opus-5-5", "写测试"))
	// 真正的旧系统 cost 决定是 deckd 开子 agent 时造的，一直带 SpawnScope（action=spawn）；返工 2（M5）起这就是「系统写的」的证据
	cost.Scope = map[string]string{"action": "spawn"}
	byMain := legacyDecision("dec-3", deckstore.KindPush, "推送 main 分支到 origin")
	st := legacyStore(t, nil, []deckstore.Decision{trust, cost, byMain})
	ds := st.Decisions("alpha")
	if len(ds) != 3 {
		t.Fatalf("%+v", ds)
	}
	by := map[string]deckstore.Decision{}
	for _, d := range ds {
		by[d.ID] = d
	}
	if by["dec-1"].SummaryCode == "" || by["dec-1"].Summary != trust.Summary {
		t.Errorf("Codex 授权的旧摘要应补码、原文不变：%+v", by["dec-1"])
	}
	if by["dec-2"].SummaryCode != "" || by["dec-3"].SummaryCode != "" {
		t.Errorf("混有外部原文的、主 agent 写的不该补码：%+v %+v", by["dec-2"], by["dec-3"])
	}
	i18n.SetLang(i18n.EN)
	en := RenderDecisions(ds, 0, "")
	if !strings.Contains(en, "rewrite ~/.codex/config.toml in full (adding trust for /tmp/alpha)") {
		t.Errorf("Codex 授权摘要应是英文译文：%q", en)
	}
	if !strings.Contains(en, "[Earlier record] "+cost.Summary) {
		t.Errorf("混有外部原文的旧花额度摘要：英文应整条原文加标签：%q", en)
	}
	if !strings.Contains(en, byMain.Summary) || strings.Contains(en, "[Earlier record] "+byMain.Summary) {
		t.Errorf("主 agent 写的摘要是外部原文，原样显示、不加标签：%q", en)
	}
	i18n.SetLang(i18n.ZH)
	zh := RenderDecisions(ds, 0, "")
	for _, d := range []deckstore.Decision{trust, cost, byMain} {
		if !strings.Contains(zh, d.Summary) {
			t.Errorf("中文应逐字是原摘要 %q：%q", d.Summary, zh)
		}
	}
	if strings.Contains(zh, "旧记录") || strings.Contains(zh, "Earlier") {
		t.Errorf("中文不加标签：%q", zh)
	}
}
