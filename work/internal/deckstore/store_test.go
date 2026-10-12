package deckstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testDirs(t *testing.T) Dirs {
	t.Helper()
	root := t.TempDir()
	return Dirs{Config: filepath.Join(root, "cfg"), State: filepath.Join(root, "state")}
}

func withProject(t *testing.T, d Dirs, env Envelope) *Store {
	t.Helper()
	s, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	set := s.Settings()
	set.Projects = []ProjectDef{{
		ID: "deck", Name: "Agent Deck", Path: "/tmp/proj",
		Agents:    []string{"claude", "codex", "kimi"},
		Models:    map[Tier]string{Frontier: "claude-opus-5-5", Balanced: "kimi-code/k3", Economy: "gpt-5.6-luna"},
		MainAgent: MainAgent{Agent: "claude", Tier: Frontier},
		Envelope:  env,
	}}
	if err := s.ReplaceSettings(set); err != nil {
		t.Fatal(err)
	}
	return s
}

// ---------- 设置 ----------

func TestDefaultAgentsAreWrittenAndValid(t *testing.T) {
	d := testDirs(t)
	s, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Settings().Agents) != 5 {
		t.Fatalf("应生成五家默认 agent：%+v", s.Settings().Agents)
	}
	if _, err := os.Stat(filepath.Join(d.Config, "agents.yaml")); err != nil {
		t.Fatalf("应写出 agents.yaml 供 owner 修改：%v", err)
	}
}

func TestResolveTierUsesProjectSelection(t *testing.T) {
	s := withProject(t, testDirs(t), DefaultEnvelope("deck"))
	r, err := s.Settings().ResolveTier("deck", Balanced)
	if err != nil || r.Model != "kimi-code/k3" || r.Agent.ID != "kimi" {
		t.Fatalf("balanced 应解析为 kimi-code/k3：%+v %v", r, err)
	}
	if _, err := s.Settings().ResolveModel("deck", "grok-4.6"); err == nil {
		t.Error("未被本项目选用的 agent（grok）的模型应被拒")
	}
}

func TestSettingsRejectModelInWrongTierOrUnselectedAgent(t *testing.T) {
	s := withProject(t, testDirs(t), DefaultEnvelope("deck"))
	bad := s.Settings()
	bad.Projects[0].Models[Frontier] = "kimi-code/k3" // 全局登记为 balanced
	if err := s.ReplaceSettings(bad); err == nil || !strings.Contains(err.Error(), "放进了") {
		t.Errorf("档位不符应被拒：%v", err)
	}
	bad = s.Settings()
	bad.Projects[0].Models[Balanced] = "grok-4.6" // grok 未被选用
	if err := s.ReplaceSettings(bad); err == nil {
		t.Error("未选用 agent 的模型应被拒")
	}
	bad = s.Settings()
	bad.Projects[0].MainAgent.Agent = "grok"
	if err := s.ReplaceSettings(bad); err == nil {
		t.Error("主 agent 不在选用范围内应被拒")
	}
}

func TestSettingsSurviveReopen(t *testing.T) {
	d := testDirs(t)
	withProject(t, d, DefaultEnvelope("deck"))
	s2, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := s2.Settings().Project("deck")
	if !ok || p.Models[Frontier] != "claude-opus-5-5" {
		t.Fatalf("重开后项目设置应还在：%+v", p)
	}
}

// ---------- 名额预留 ----------

var reqSeq atomic.Int64

// launch 补上启动请求指纹（每次一个新的请求 id）。
func launch(x Session) Session {
	x.RequestID = fmt.Sprintf("req-%d", reqSeq.Add(1))
	x.Dir = "/tmp/proj"
	x.TaskSHA256 = strings.Repeat("a", 64)
	x.ArgsSHA256 = strings.Repeat("b", 64)
	return x
}

func sub(tier Tier) Session {
	return launch(Session{Project: "deck", Role: "sub", Agent: "claude", Tool: "claude", Model: "m", Tier: tier, Title: "t", CreatedBy: "main"})
}

func project(t *testing.T, s *Store) ProjectDef {
	t.Helper()
	p, ok := s.Settings().Project("deck")
	if !ok {
		t.Fatal("没有项目 deck")
	}
	return p
}

func TestReserveEnforcesEnvelope(t *testing.T) {
	s := withProject(t, testDirs(t), Envelope{ID: "e", Version: 1, MaxSubagents: 2, MaxFrontier: 0, TokenSoftCap: 1})
	var over *ErrOverEnvelope
	if _, err := s.Reserve(sub(Frontier), ""); !errors.As(err, &over) {
		t.Fatalf("frontier 上限 0 时应超包络：%v", err)
	}
	r1, err := s.Reserve(sub(Balanced), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reserve(sub(Economy), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reserve(sub(Economy), ""); !errors.As(err, &over) {
		t.Fatalf("第三个子 agent 应超包络：%v", err)
	}
	// 明确失败释放名额
	if err := s.LaunchFailed(r1.Key, false, "起不来"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reserve(sub(Economy), ""); err != nil {
		t.Fatalf("释放后应能再预留：%v", err)
	}
}

func TestUncertainLaunchKeepsTheSlot(t *testing.T) {
	s := withProject(t, testDirs(t), Envelope{ID: "e", Version: 1, MaxSubagents: 1, MaxFrontier: 0, TokenSoftCap: 1})
	r, _ := s.Reserve(sub(Balanced), "")
	s.LaunchFailed(r.Key, true, "回执超时")
	var over *ErrOverEnvelope
	if _, err := s.Reserve(sub(Balanced), ""); !errors.As(err, &over) {
		t.Fatalf("结果不明时应继续占着名额：%v", err)
	}
}

// 并发预留不会突破包络：检查与预留在同一把锁里。
func TestConcurrentReservesDoNotExceedEnvelope(t *testing.T) {
	s := withProject(t, testDirs(t), Envelope{ID: "e", Version: 1, MaxSubagents: 3, MaxFrontier: 0, TokenSoftCap: 1})
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Reserve(sub(Economy), ""); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 3 {
		t.Fatalf("20 个并发预留应恰好成功 3 个，得到 %d", ok)
	}
}

func TestLaunchingBecomesUncertainAfterRestart(t *testing.T) {
	d := testDirs(t)
	s := withProject(t, d, DefaultEnvelope("deck"))
	r, _ := s.Reserve(sub(Economy), "")
	s2, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	x, _ := s2.Session(r.Key)
	if x.Status != StatusUncertain {
		t.Fatalf("重启后仍在启动中的会话应为 uncertain：%s", x.Status)
	}
	if s2.Slots("deck").Subagents != 1 {
		t.Error("uncertain 应继续占名额")
	}
}

// 超额例外：只认本项目、正在执行、scope 与本次请求一致的 cost 决定；用一次即消费，不能再用（n050 评审 B2）。
func TestReserveExceptionRequiresExecutingCostDecision(t *testing.T) {
	env := Envelope{ID: "e", Version: 1, MaxSubagents: 0, MaxFrontier: 0, TokenSoftCap: 1}
	s := withProject(t, testDirs(t), env)
	req := sub(Economy)
	scope := SpawnScope(req, project(t, s))
	d, _ := s.CreateDecision(NewDecision{Project: "deck", Kind: KindCost, Summary: "多开一个", Scope: scope, CreatedBy: "main"})
	if _, err := s.Reserve(req, d.ID); err == nil {
		t.Fatal("未批准的决定不能当例外")
	}
	d, _ = s.Answer(d.ID, OptApprove, d.Version, "owner")
	if _, err := s.Reserve(req, d.ID); err == nil {
		t.Fatal("批准但未进入执行的决定不能当例外")
	}
	if _, err := s.BeginExecute(d.ID, scope); err != nil {
		t.Fatal(err)
	}
	other := req
	other.Model = "another-model"
	if _, err := s.Reserve(other, d.ID); err == nil {
		t.Fatal("批准的是另一个请求，不能用于这次")
	}
	if _, err := s.Reserve(req, d.ID); err != nil {
		t.Fatalf("scope 一致、正在执行的 cost 决定应能当例外：%v", err)
	}
	if got, _ := s.Decision(d.ID); got.State != StateApplied {
		t.Fatalf("用过的例外应标为 applied：%s", got.State)
	}
	if _, err := s.Reserve(req, d.ID); err == nil {
		t.Fatal("同一个例外不能用第二次")
	}
}

// 并发用同一个例外：恰好一次成功。
func TestConcurrentUseOfOneExceptionSucceedsOnce(t *testing.T) {
	env := Envelope{ID: "e", Version: 1, MaxSubagents: 0, MaxFrontier: 0, TokenSoftCap: 1}
	s := withProject(t, testDirs(t), env)
	req := sub(Economy)
	scope := SpawnScope(req, project(t, s))
	d, _ := s.CreateDecision(NewDecision{Project: "deck", Kind: KindCost, Summary: "x", Scope: scope, CreatedBy: "main"})
	d, _ = s.Answer(d.ID, OptApprove, d.Version, "owner")
	s.BeginExecute(d.ID, scope)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Reserve(req, d.ID); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("同一个例外并发使用应恰好成功一次，得到 %d", ok)
	}
}

// ---------- 决策 ----------

func TestDecisionAnswerNeedsCurrentVersion(t *testing.T) {
	s := withProject(t, testDirs(t), DefaultEnvelope("deck"))
	d, _ := s.CreateDecision(NewDecision{Project: "deck", Kind: KindDeviation, Summary: "按状态分组？", Options: []string{"按项目", "按状态"}, CreatedBy: "main"})
	if got := strings.Join(d.Options, ","); got != "按项目,按状态,reject,stop" {
		t.Errorf("选项应补上 reject 与 stop：%s", got)
	}
	if _, err := s.Answer(d.ID, "按项目", d.Version, "owner"); err != nil {
		t.Fatal(err)
	}
	var ve *ErrVersion
	if _, err := s.Answer(d.ID, "按状态", d.Version, "owner"); err == nil || errors.As(err, &ve) == false && !strings.Contains(err.Error(), "不能再答") {
		t.Fatalf("第二次回答应被拒：%v", err)
	}
}

// 带过时版本号的回答被拒，即使决定仍是 open。
func TestAnswerWithWrongVersionIsRefused(t *testing.T) {
	s := withProject(t, testDirs(t), DefaultEnvelope("deck"))
	d, _ := s.CreateDecision(NewDecision{Project: "deck", Kind: KindPush, Summary: "推送？", CreatedBy: "main"})
	var ve *ErrVersion
	if _, err := s.Answer(d.ID, OptApprove, d.Version+7, "owner"); !errors.As(err, &ve) {
		t.Fatalf("版本号不对应被拒：%v", err)
	}
	if got, _ := s.Decision(d.ID); got.State != StateOpen {
		t.Fatalf("被拒的回答不该改变状态：%s", got.State)
	}
}

// 两个回答同时到：恰好一个成功。
func TestConcurrentAnswersOnlyOneWins(t *testing.T) {
	s := withProject(t, testDirs(t), DefaultEnvelope("deck"))
	d, _ := s.CreateDecision(NewDecision{Project: "deck", Kind: KindCost, Summary: "x", CreatedBy: "main"})
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for _, opt := range []string{OptApprove, OptReject, OptStop, OptApprove} {
		wg.Add(1)
		go func(o string) {
			defer wg.Done()
			if _, err := s.Answer(d.ID, o, d.Version, "owner"); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(opt)
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("并发回答应恰好一个成功，得到 %d", wins)
	}
}

func TestExecuteRechecksScope(t *testing.T) {
	s := withProject(t, testDirs(t), DefaultEnvelope("deck"))
	scope := map[string]string{"action": "spawn", "model": "claude-opus-5-5", "task_sha256": "aa"}
	d, _ := s.CreateDecision(NewDecision{Project: "deck", Kind: KindCost, Summary: "x", Scope: scope, CreatedBy: "main"})
	d, _ = s.Answer(d.ID, OptApprove, d.Version, "owner")
	changed := map[string]string{"action": "spawn", "model": "claude-opus-5-5", "task_sha256": "bb"}
	got, err := s.BeginExecute(d.ID, changed)
	if err == nil || got.State != StateStale {
		t.Fatalf("scope 变了应作废为 stale：%+v %v", got, err)
	}
}

// 作废（stale）要落盘：重启后仍是 stale，旧批准不能再执行（n050 评审 B1）。
func TestStaleIsPersistedAcrossRestart(t *testing.T) {
	d := testDirs(t)
	s := withProject(t, d, DefaultEnvelope("deck"))
	scopeA := map[string]string{"action": "spawn", "task_sha256": "aa"}
	dec, _ := s.CreateDecision(NewDecision{Project: "deck", Kind: KindCost, Summary: "x", Scope: scopeA, CreatedBy: "main"})
	dec, _ = s.Answer(dec.ID, OptApprove, dec.Version, "owner")
	s.BeginExecute(dec.ID, map[string]string{"action": "spawn", "task_sha256": "bb"})
	s2, _ := Open(d)
	got, _ := s2.Decision(dec.ID)
	if got.State != StateStale {
		t.Fatalf("重启后应仍是 stale：%s", got.State)
	}
	if _, err := s2.BeginExecute(dec.ID, scopeA); err == nil {
		t.Fatal("作废的批准不能再执行")
	}
}

// scope 里的疑似密钥、控制字符、不合规的键都被拒，不会落盘（n050 评审 B7）。
func TestScopeWithSecretsOrControlCharsIsRejected(t *testing.T) {
	d := testDirs(t)
	s := withProject(t, d, DefaultEnvelope("deck"))
	for _, scope := range []map[string]string{
		{"token": "sk-abcdefghijklmnop"},
		{"title": "a\x1b]0;evil\x07"},
		{"Bad-Key": "x"},
	} {
		if _, err := s.CreateDecision(NewDecision{Project: "deck", Kind: KindCost, Summary: "x", Scope: scope, CreatedBy: "main"}); err == nil {
			t.Errorf("scope %v 应被拒", scope)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(d.State, "decisions.json")); strings.Contains(string(data), "sk-abc") {
		t.Fatal("疑似密钥落了盘")
	}
}

// 返回的是副本：改返回值不影响存储。
func TestReturnedValuesAreCopies(t *testing.T) {
	s := withProject(t, testDirs(t), DefaultEnvelope("deck"))
	dec, _ := s.CreateDecision(NewDecision{Project: "deck", Kind: KindCost, Summary: "x", Scope: map[string]string{"a": "1"}, CreatedBy: "main"})
	dec.Scope["a"] = "2"
	dec.Options[0] = "hacked"
	got, _ := s.Decision(dec.ID)
	if got.Scope["a"] != "1" || got.Options[0] == "hacked" {
		t.Fatalf("改返回值影响了存储：%+v", got)
	}
}

func TestRejectedOrExpiredCannotExecute(t *testing.T) {
	s := withProject(t, testDirs(t), DefaultEnvelope("deck"))
	now := time.Now()
	s.SetClock(func() time.Time { return now })
	d, _ := s.CreateDecision(NewDecision{Project: "deck", Kind: KindCost, Summary: "x", CreatedBy: "main", TTL: time.Hour})
	r, _ := s.Answer(d.ID, OptReject, d.Version, "owner")
	if _, err := s.BeginExecute(r.ID, nil); err == nil {
		t.Error("被拒绝的决定不能执行")
	}
	d2, _ := s.CreateDecision(NewDecision{Project: "deck", Kind: KindCost, Summary: "y", CreatedBy: "main", TTL: time.Hour})
	d2, _ = s.Answer(d2.ID, OptApprove, d2.Version, "owner")
	now = now.Add(2 * time.Hour)
	if got, err := s.BeginExecute(d2.ID, nil); err == nil || got.State != StateExpired {
		t.Fatalf("过期的批准不能执行：%+v %v", got, err)
	}
}

func TestExecutingBecomesUncertainAfterRestart(t *testing.T) {
	d := testDirs(t)
	s := withProject(t, d, DefaultEnvelope("deck"))
	dec, _ := s.CreateDecision(NewDecision{Project: "deck", Kind: KindCost, Summary: "x", CreatedBy: "main"})
	dec, _ = s.Answer(dec.ID, OptApprove, dec.Version, "owner")
	s.BeginExecute(dec.ID, nil)
	s2, _ := Open(d)
	got, _ := s2.Decision(dec.ID)
	if got.State != StateUncertain {
		t.Fatalf("重启后执行中的决定应为 uncertain：%s", got.State)
	}
}

func TestOnlyCreatorCanCancel(t *testing.T) {
	s := withProject(t, testDirs(t), DefaultEnvelope("deck"))
	d, _ := s.CreateDecision(NewDecision{Project: "deck", Kind: KindPush, Summary: "推送？", CreatedBy: "main-a"})
	if err := s.Cancel(d.ID, "other"); err == nil {
		t.Error("非创建者不能撤回")
	}
	if err := s.Cancel(d.ID, "main-a"); err != nil {
		t.Error(err)
	}
}

// ---------- Codex trust 记住授权 ----------

func TestTrustInvalidatesOnDirOrToolChange(t *testing.T) {
	s := withProject(t, testDirs(t), DefaultEnvelope("deck"))
	s.GrantTrust(TrustGrant{Project: "deck", Tool: "codex", ProjectDir: "/proj", Dir: "/p", ToolPath: "/bin/codex", ToolVersion: "0.162.1"})
	if !s.TrustValid("deck", "codex", "/proj", "/p", "/bin/codex", "0.162.1") {
		t.Fatal("同条件应有效")
	}
	for _, c := range [][3]string{{"/q", "/bin/codex", "0.162.1"}, {"/p", "/usr/bin/codex", "0.162.1"}, {"/p", "/bin/codex", "0.163.0"}} {
		if s.TrustValid("deck", "codex", "/proj", c[0], c[1], c[2]) {
			t.Errorf("条件变化应失效：%v", c)
		}
	}
	// 项目目录变了：重新问（n051 评审 1 B3）；没记项目目录的旧记录也重新问
	if s.TrustValid("deck", "codex", "/moved-proj", "/p", "/bin/codex", "0.162.1") {
		t.Fatal("项目目录变了，授权应失效")
	}
	s.GrantTrust(TrustGrant{Project: "old", Tool: "codex", Dir: "/p", ToolPath: "/bin/codex", ToolVersion: "0.162.1"})
	if s.TrustValid("old", "codex", "", "/p", "/bin/codex", "0.162.1") {
		t.Fatal("没记项目目录的旧记录不算有效")
	}
	if ok, _ := s.RevokeTrust("deck", "codex"); !ok || s.TrustValid("deck", "codex", "/proj", "/p", "/bin/codex", "0.162.1") {
		t.Error("撤销后应失效")
	}
}

// ---------- 通知与清洗 ----------

func TestNotificationsAreCleanedAndSeqSurvivesRestart(t *testing.T) {
	d := testDirs(t)
	s := withProject(t, d, DefaultEnvelope("deck"))
	n1, _ := s.Notify("deck", "spawn", "开了 \x1b[31mCodex\x1b[0m \x1b]0;evil\x07 key sk-abcdefghijklmnop", "main")
	if strings.ContainsAny(n1.Text, "\x1b\x07") || strings.Contains(n1.Text, "sk-abc") {
		t.Fatalf("通知应去控制序列并打码：%q", n1.Text)
	}
	s2, _ := Open(d)
	n2, _ := s2.Notify("deck", "x", "y", "")
	if n2.Seq != n1.Seq+1 {
		t.Fatalf("重启后序号应接续：%d → %d", n1.Seq, n2.Seq)
	}
	if got := s2.Notifications(n1.Seq, "deck"); len(got) != 1 || got[0].Seq != n2.Seq {
		t.Fatalf("按 seq 续取不对：%+v", got)
	}
}

func TestCleanLimitsLength(t *testing.T) {
	if got := Clean(strings.Repeat("字", 50), 10); got != strings.Repeat("字", 10)+"…" {
		t.Fatalf("按字符限长：%q", got)
	}
	if got := Clean("a\x1b[2Jb\x1bPdcs\x1b\\c\u009bd", 0); got != "abcd" {
		t.Fatalf("各类控制序列都应去掉：%q", got)
	}
}

// ---------- 存储失败即停 ----------

func TestPersistFailureStopsAllWrites(t *testing.T) {
	d := testDirs(t)
	s := withProject(t, d, DefaultEnvelope("deck"))
	// 把状态目录换成只读，让下一次落盘失败
	os.MkdirAll(d.State, 0o700)
	if err := os.Chmod(d.State, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(d.State, 0o700)
	if os.Geteuid() == 0 {
		t.Skip("root 不受目录权限限制")
	}
	_, err := s.CreateDecision(NewDecision{Project: "deck", Kind: KindCost, Summary: "x", CreatedBy: "main"})
	var be *ErrBroken
	if !errors.As(err, &be) {
		t.Fatalf("落盘失败应进入停止状态：%v", err)
	}
	os.Chmod(d.State, 0o700)
	if _, err := s.Reserve(sub(Economy), ""); !errors.As(err, &be) {
		t.Fatalf("停止后的一切写入都应被拒：%v", err)
	}
	if s.Broken() == nil {
		t.Error("Broken() 应报告停止原因")
	}
}
