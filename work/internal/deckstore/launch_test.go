package deckstore

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// 第 3 次实现（n050 评审 R1、S2）：超额例外查有效期、绑定完整启动请求；过期转换一律落盘。

func approvedException(t *testing.T, s *Store, x Session, ttl time.Duration) Decision {
	t.Helper()
	d, err := s.CreateDecision(NewDecision{Project: "deck", Kind: KindCost, Summary: "多开一个", CreatedBy: "main", Scope: SpawnScope(x, project(t, s)), TTL: ttl})
	if err != nil {
		t.Fatal(err)
	}
	if d, err = s.Answer(d.ID, OptApprove, d.Version, "owner"); err != nil {
		t.Fatal(err)
	}
	if d, err = s.BeginExecute(d.ID, d.Scope); err != nil {
		t.Fatal(err)
	}
	return d
}

// 评审复现：批准并开始执行后过了有效期，不能再用于预留；过期状态落盘。
func TestExpiredExecutingExceptionCannotReserve(t *testing.T) {
	dirs := testDirs(t)
	env := DefaultEnvelope("deck")
	env.MaxSubagents = 0
	s := withProject(t, dirs, env)
	now := time.Now()
	s.SetClock(func() time.Time { return now })
	x := sub(Economy)
	d := approvedException(t, s, x, time.Second)
	now = now.Add(2 * time.Second)
	if r, err := s.Reserve(x, d.ID); err == nil {
		t.Fatalf("过期的例外被用掉了：%+v", r)
	}
	s2, err := Open(dirs)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s2.Decision(d.ID); got.State != StateExpired {
		t.Fatalf("过期应落盘：%s", got.State)
	}
	if s2.Slots("deck").Subagents != 0 {
		t.Fatal("过期例外不应留下预留")
	}
}

// 评审复现：批准后项目目录改了（项目 id、包络不变），例外失效。
func TestChangedProjectDirectoryInvalidatesException(t *testing.T) {
	env := DefaultEnvelope("deck")
	env.MaxSubagents = 0
	s := withProject(t, testDirs(t), env)
	x := sub(Economy)
	d := approvedException(t, s, x, time.Hour)
	set := s.Settings()
	set.Projects[0].Path = "/tmp/other-project"
	if err := s.ReplaceSettings(set); err != nil {
		t.Fatal(err)
	}
	if r, err := s.Reserve(x, d.ID); err == nil {
		t.Fatalf("项目目录变了，例外仍被接受：%+v", r)
	}
}

// 例外绑定工作目录、任务、启动参数、请求 id：任一不同都不能用。
func TestExceptionIsBoundToTheWholeLaunchRequest(t *testing.T) {
	env := DefaultEnvelope("deck")
	env.MaxSubagents = 0
	s := withProject(t, testDirs(t), env)
	x := sub(Economy)
	d := approvedException(t, s, x, time.Hour)
	for name, change := range map[string]func(*Session){
		"dir":   func(y *Session) { y.Dir = "/tmp/proj/other" },
		"task":  func(y *Session) { y.TaskSHA256 = strings.Repeat("c", 64) },
		"args":  func(y *Session) { y.ArgsSHA256 = strings.Repeat("c", 64) },
		"reqid": func(y *Session) { y.RequestID = "req-other" },
	} {
		y := x
		change(&y)
		if _, err := s.Reserve(y, d.ID); err == nil {
			t.Fatalf("%s 不同仍接受了例外", name)
		}
	}
	if _, err := s.Reserve(x, d.ID); err != nil {
		t.Fatalf("同一请求应能用：%v", err)
	}
}

// 同一启动请求 id 只能预留一次；指纹不合格直接拒绝（原样校验，不清洗）。
func TestLaunchFingerprintIsRequiredAndRequestIDIsUnique(t *testing.T) {
	s := withProject(t, testDirs(t), DefaultEnvelope("deck"))
	x := sub(Economy)
	if _, err := s.Reserve(x, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reserve(x, ""); err == nil {
		t.Fatal("同一请求 id 预留了两次")
	}
	for name, change := range map[string]func(*Session){
		"无请求 id":   func(y *Session) { y.RequestID = "" },
		"请求 id 含空格": func(y *Session) { y.RequestID = "a b" },
		"相对目录":     func(y *Session) { y.Dir = "proj" },
		"不规整目录":    func(y *Session) { y.Dir = "/tmp/proj/../x" },
		"目录含控制符":   func(y *Session) { y.Dir = "/tmp/\x1b[2Jx" },
		"任务哈希大写":   func(y *Session) { y.TaskSHA256 = strings.Repeat("A", 64) },
		"参数哈希过短":   func(y *Session) { y.ArgsSHA256 = "abc" },
	} {
		y := sub(Economy)
		change(&y)
		if _, err := s.Reserve(y, ""); err == nil {
			t.Fatalf("%s 应被拒绝", name)
		}
	}
}

// 过期转换：回答时发现过期要落盘；读接口只显示为 expired，不改内存（评审 S2）。
func TestExpiryIsPersistedByWritesAndReadsDoNotMutate(t *testing.T) {
	dirs := testDirs(t)
	s := withProject(t, dirs, DefaultEnvelope("deck"))
	now := time.Now()
	s.SetClock(func() time.Time { return now })
	d, err := s.CreateDecision(NewDecision{Project: "deck", Kind: KindDeviation, Summary: "x", CreatedBy: "main", Scope: map[string]string{"action": "x"}, TTL: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if got, _ := s.Decision(d.ID); got.State != StateExpired || got.Version != d.Version {
		t.Fatalf("读到的应显示 expired 且版本不变：%s v%d", got.State, got.Version)
	}
	if s.decs[d.ID].State != StateOpen {
		t.Fatal("读接口不应改内存")
	}
	if _, err := s.Answer(d.ID, OptApprove, d.Version, "owner"); err == nil {
		t.Fatal("过期的决定不能再答")
	}
	s2, err := Open(dirs)
	if err != nil {
		t.Fatal(err)
	}
	if s2.decs[d.ID].State != StateExpired {
		t.Fatalf("回答时发现的过期应落盘：%s", s2.decs[d.ID].State)
	}
}

// 父目录做落盘同步失败也算写失败，存储停止（评审 S3）。目录可写可进、不可读：写临时文件与改名都成功，只有打开目录做同步失败。
func TestDirectorySyncFailureStopsTheStore(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 不受目录权限限制")
	}
	d := testDirs(t)
	s := withProject(t, d, DefaultEnvelope("deck"))
	os.MkdirAll(d.State, 0o700)
	if err := os.Chmod(d.State, 0o300); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(d.State, 0o700)
	_, err := s.CreateDecision(NewDecision{Project: "deck", Kind: KindCost, Summary: "x", CreatedBy: "main"})
	var be *ErrBroken
	if !errors.As(err, &be) || s.Broken() == nil {
		t.Fatalf("目录同步失败应进入停止状态：%v", err)
	}
}

// 预留保存的是副本：调用方之后改自己的 Graph，不影响注册表（评审 S3）。
func TestReserveDoesNotShareTheCallersGraph(t *testing.T) {
	s := withProject(t, testDirs(t), DefaultEnvelope("deck"))
	x := sub(Economy)
	x.Graph = &GraphLink{Node: "n050", Attempt: 3}
	r, err := s.Reserve(x, "")
	if err != nil {
		t.Fatal(err)
	}
	x.Graph.Node = "被调用方改了"
	if got, _ := s.Session(r.Key); got.Graph == nil || got.Graph.Node != "n050" {
		t.Fatalf("注册表不应跟着变：%+v", got.Graph)
	}
}
