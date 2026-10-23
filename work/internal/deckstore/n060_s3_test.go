package deckstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 段 3：界面语言存进设置（agents.yaml 的可选字段 ui_lang）；没有这个字段的旧文件照常读取，也不会被改写；
// 只有 owner 在界面里改了语言才写。n059 的自动保存与 n057 的代数保护不能回退。

func s3Dirs(t *testing.T) Dirs {
	t.Helper()
	root := t.TempDir()
	d := Dirs{Config: filepath.Join(root, "cfg"), State: filepath.Join(root, "state")}
	for _, p := range []string{d.Config, d.State} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestN060S3UILangRoundTrip(t *testing.T) {
	d := s3Dirs(t)
	s, err := LoadSettings(d.Config)
	if err != nil {
		t.Fatal(err)
	}
	if s.UILang != "" {
		t.Fatalf("新环境没选过语言：%q", s.UILang)
	}
	for _, lang := range []string{i18n.EN, i18n.ZH} {
		s.UILang = lang
		if err := SaveSettings(d.Config, s); err != nil {
			t.Fatal(err)
		}
		again, err := LoadSettings(d.Config)
		if err != nil {
			t.Fatal(err)
		}
		if again.UILang != lang {
			t.Fatalf("存 %q 读回 %q", lang, again.UILang)
		}
		if !strings.Contains(readFile(t, filepath.Join(d.Config, "agents.yaml")), "ui_lang: "+lang) {
			t.Fatalf("agents.yaml 里应有 ui_lang: %s：\n%s", lang, readFile(t, filepath.Join(d.Config, "agents.yaml")))
		}
		if strings.Contains(readFile(t, filepath.Join(d.Config, "projects.yaml")), "ui_lang") {
			t.Fatal("projects.yaml 不放语言")
		}
		s = again
	}
	// 清回「没选过」：字段不再写出
	s.UILang = ""
	if err := SaveSettings(d.Config, s); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFile(t, filepath.Join(d.Config, "agents.yaml")), "ui_lang") {
		t.Fatal("清空之后不该再写 ui_lang")
	}
	// 经 JSON（界面与 deckd 之间）也带着：cloneSettings 靠它
	s.UILang = i18n.EN
	b, _ := json.Marshal(s)
	var back Settings
	if err := json.Unmarshal(b, &back); err != nil || back.UILang != i18n.EN {
		t.Fatalf("JSON 往返：%+v %v", back, err)
	}
	if c := cloneSettings(s); c.UILang != i18n.EN {
		t.Fatalf("cloneSettings 丢了语言：%+v", c)
	}
}

// 旧设置文件（没有 ui_lang）：照常读取，按「没选过」处理；读取、打开、不改语言的保存都不会把字段补上，
// 也不会为了这个字段去改写文件。
func TestN060S3OldSettingsWithoutUILangAreNotRewritten(t *testing.T) {
	d := s3Dirs(t)
	agents := "generation: 5\nagents:\n  - id: claude\n    name: Claude Code\n    tool: claude\n    binary: claude\n    models:\n      - id: claude-opus-5-5\n        tier: frontier\n"
	projects := "generation: 5\nprojects:\n  - id: p1\n    name: P1\n    path: /tmp/p1\n    agents: [claude]\n    models:\n      frontier: [claude-opus-5-5]\n    main_agent: {agent: claude, tier: frontier}\n    envelope: {id: p1-default, version: 1, max_subagents: 3, max_frontier: 0, token_soft_cap: 2000000}\n"
	if err := os.WriteFile(filepath.Join(d.Config, "agents.yaml"), []byte(agents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.Config, "projects.yaml"), []byte(projects), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSettings(d.Config)
	if err != nil {
		t.Fatal(err)
	}
	if s.UILang != "" || s.Generation != 5 || len(s.Projects) != 1 {
		t.Fatalf("旧文件读取不对：%+v", s)
	}
	if err := CheckSettings(d.Config); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(d.Config, "agents.yaml")); got != agents {
		t.Fatalf("读取改写了 agents.yaml：\n%s", got)
	}
	st, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(d.Config, "agents.yaml")); got != agents {
		t.Fatalf("Open 改写了 agents.yaml：\n%s", got)
	}
	if got := readFile(t, filepath.Join(d.Config, "projects.yaml")); got != projects {
		t.Fatalf("Open 改写了 projects.yaml：\n%s", got)
	}
	if st.UILang() != "" {
		t.Fatalf("没选过语言：%q", st.UILang())
	}
	// 不改语言的保存（例如 deck project add、改模型）：照常写，但不带 ui_lang
	set := st.Settings()
	set.Projects[0].Name = "renamed"
	if err := st.ReplaceSettings(set); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFile(t, filepath.Join(d.Config, "agents.yaml")), "ui_lang") {
		t.Fatal("没选过语言，别的保存不该补上 ui_lang")
	}
}

// 重启之后（重新 Open）语言还在；别的保存不会丢掉它。
func TestN060S3UILangSurvivesRestartAndOtherSaves(t *testing.T) {
	d := s3Dirs(t)
	st, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	set := st.Settings()
	set.UILang = i18n.EN
	if _, err := st.ReplaceSettingsAtGen(set, set.Generation); err != nil {
		t.Fatal(err)
	}
	if st.UILang() != i18n.EN {
		t.Fatalf("保存后 Store 里的语言：%q", st.UILang())
	}
	// 别的保存（带着读到的设置，不改语言）
	set = st.Settings()
	set.Agents[0].Name = "Renamed"
	if _, err := st.ReplaceSettingsAtGen(set, set.Generation); err != nil {
		t.Fatal(err)
	}
	st2, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	if st2.UILang() != i18n.EN || st2.Settings().Agents[0].Name != "Renamed" {
		t.Fatalf("重启后：%q %q", st2.UILang(), st2.Settings().Agents[0].Name)
	}
}

// 语言不认识：保存被拒，手改成不认识的值读取时报错（不猜），deck settings -check 也报。
func TestN060S3UnknownUILangIsRejected(t *testing.T) {
	d := s3Dirs(t)
	s, err := LoadSettings(d.Config)
	if err != nil {
		t.Fatal(err)
	}
	s.UILang = "fr"
	if err := s.Validate(); err == nil {
		t.Fatal("不认识的语言应校验失败")
	} else if _, ok := i18n.TextEN("err.deckstore.settingsvalidate.17"); !ok || !strings.Contains(err.Error(), "fr") {
		t.Fatalf("%v", err)
	}
	if err := SaveSettings(d.Config, s); err == nil {
		t.Fatal("不认识的语言不许存")
	}
	// 手改
	good, _ := LoadSettings(d.Config)
	good.UILang = i18n.EN
	if err := SaveSettings(d.Config, good); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d.Config, "agents.yaml")
	if err := os.WriteFile(p, []byte(strings.Replace(readFile(t, p), "ui_lang: en", "ui_lang: klingon", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSettings(d.Config); err == nil {
		t.Fatal("手改成不认识的语言，读取应报错")
	}
	if err := CheckSettings(d.Config); err == nil {
		t.Fatal("settings -check 应报错")
	}
}

// n057 的代数保护不能回退：改语言和别的设置一样，带旧代数就被拒（别处改过），语言不变。
func TestN060S3LangChangeIsGuardedByGeneration(t *testing.T) {
	d := s3Dirs(t)
	st, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	stale := st.Settings()
	other := st.Settings()
	other.Agents[0].Name = "Changed elsewhere"
	if _, err := st.ReplaceSettingsAtGen(other, other.Generation); err != nil {
		t.Fatal(err)
	}
	stale.UILang = i18n.EN
	_, err = st.ReplaceSettingsAtGen(stale, stale.Generation)
	var changed *ErrSettingsChanged
	if !errors.As(err, &changed) {
		t.Fatalf("旧代数应被拒：%v", err)
	}
	if st.UILang() != "" {
		t.Fatalf("被拒的保存不该改语言：%q", st.UILang())
	}
	// 手改了两份文件：界面带的代数作废，语言也不被盖掉
	fresh := st.Settings()
	p := filepath.Join(d.Config, "agents.yaml")
	if err := os.WriteFile(p, []byte(readFile(t, p)+"# hand edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh.UILang = i18n.EN
	if _, err := st.ReplaceSettingsAtGen(fresh, fresh.Generation); !errors.As(err, &changed) || !changed.HandEdited {
		t.Fatalf("手改过应拒绝：%v", err)
	}
	if st.UILang() != "" {
		t.Fatal("手改保护之下语言不该被改")
	}
}

// 旧通知、旧决定摘要：Open 读到时补码，原文不变，文件不重写。
func TestN060S3OpenBackfillsLegacyCodesInMemoryOnly(t *testing.T) {
	d := s3Dirs(t)
	fixed := i18n.MustTextZH("err.deckd.serverrunspawn.26")
	zhText := strings.Replace(fixed, "%s", "worker", 1)
	n1, _ := json.Marshal(Notification{Seq: 1, Kind: "spawn", Source: "deckd", Text: zhText, At: time.Now()})
	n2, _ := json.Marshal(Notification{Seq: 2, Kind: "report", Source: "k", Text: "子 agent「写日志」：完成了", At: time.Now()})
	n3, _ := json.Marshal(Notification{Seq: 3, Kind: "view", Source: "deckd", Text: "会话不在打开集合里", At: time.Now(), Code: "keep.me"})
	notes := string(n1) + "\n" + string(n2) + "\n" + string(n3) + "\n"
	if err := os.WriteFile(filepath.Join(d.State, "notifications.jsonl"), []byte(notes), 0o600); err != nil {
		t.Fatal(err)
	}
	dec := Decision{ID: "dec-1", Project: "p", Kind: KindCodexTrust, Options: []string{"approve", "reject"}, State: StateOpen,
		Summary: strings.Replace(i18n.MustTextZH("err.deckd.serverrunspawn.4"), "%s", "Codex", 1),
		Version: 1, CreatedBy: "deckd", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), UpdatedAt: time.Now()}
	db, _ := json.Marshal([]Decision{dec})
	if err := os.WriteFile(filepath.Join(d.State, "decisions.json"), db, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	ns := st.Notifications(0, "")
	if len(ns) != 3 {
		t.Fatalf("%+v", ns)
	}
	if ns[0].Code != "err.deckd.serverrunspawn.26" || len(ns[0].Args) != 1 || ns[0].Args[0] != "worker" || ns[0].Text != zhText {
		t.Errorf("固定句子 + 标识参数应补码、原文不变：%+v", ns[0])
	}
	if ns[1].Code != "" {
		t.Errorf("混有外部原文的不该补码：%+v", ns[1])
	}
	if ns[2].Code != "keep.me" {
		t.Errorf("已有码的不动：%+v", ns[2])
	}
	if got := readFile(t, filepath.Join(d.State, "notifications.jsonl")); got != notes {
		t.Error("notifications.jsonl 是追加的历史，Open 不重写")
	}
	if got := readFile(t, filepath.Join(d.State, "decisions.json")); got != string(db) {
		t.Error("decisions.json 不为补码重写（下次保存决定时才带上）")
	}
	d1, ok := st.Decision("dec-1")
	if !ok || d1.SummaryCode != "err.deckd.serverrunspawn.4" || d1.Summary != dec.Summary {
		t.Errorf("Codex 授权旧摘要应补码、原文不变：%+v", d1)
	}
	if !SystemSummaryKind(KindCost) || !SystemSummaryKind(KindCodexTrust) || SystemSummaryKind(KindPush) {
		t.Error("系统生成摘要的类别：花额度与 Codex 授权")
	}
}
