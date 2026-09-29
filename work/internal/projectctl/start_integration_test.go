package projectctl

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// n040 终审断点 4 的真实复现：夹具里 failed 的 f05 经 resume 回到 ready，
// 再经 Start 开工，账本给的是 attempt 2——界面过去会默认成 1。用真实控制器跑在夹具副本上。
func TestIntegrationStartReturnsTheLedgerAttemptForAResumedNode(t *testing.T) {
	script, err := DefaultScript()
	if err != nil {
		t.Skipf("跳过：找不到 projectctl（%v）", err)
	}
	src, _ := filepath.Abs("../../testdata/workdeck-fixture")
	// 控制器要求目录名等于 project_id。
	dst := filepath.Join(t.TempDir(), "workdeck-fixture")
	if out, err := exec.Command("cp", "-r", src, dst).CombinedOutput(); err != nil {
		t.Fatalf("复制夹具失败：%v %s", err, out)
	}
	resume := exec.Command("python3", script, "--format", "json", "resume", "f05_legacy_path", "--actor", "owner", "--reason", "n042 测试：恢复失败节点")
	resume.Dir = dst
	if out, err := resume.CombinedOutput(); err != nil {
		t.Fatalf("resume 失败：%v %s", err, out)
	}
	c := NewClient(dst, script)
	res, err := c.Start(context.Background(), StartRequest{NodeID: "f05_legacy_path", Actor: "workdeck-test",
		Role: "fixture_executor", RouteClass: "balanced", Model: "fixture-model", Reason: "n042 测试"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Attempt != 2 || res.State != "running" || res.EventID == "" {
		t.Errorf("恢复过的节点开工应当拿到 attempt 2：%+v", res)
	}
	// 再开一次必须被控制器拒绝（节点已 running），Start 把原因带出来。
	if _, err := c.Start(context.Background(), StartRequest{NodeID: "f05_legacy_path", Actor: "workdeck-test",
		Role: "fixture_executor", RouteClass: "balanced", Model: "fixture-model", Reason: "重复"}); err == nil {
		t.Error("running 节点重复开工应当被控制器拒绝")
	}
	if _, err := os.Stat(filepath.Join(src, "4.eval", "control", "events.jsonl")); err != nil {
		t.Fatalf("前提：源夹具账本应当还在：%v", err)
	}
}

// n044 attempt 2 评审 blocker 2：开工出岔子（子进程超时、异常退出、回执丢了）不等于没开工。
// 用一个包在真实控制器外面的脚本模拟几种岔子，Start 回头对账：
//   - lost：控制器真的开工了，回执丢了 → 对账确认开工，拿投影里的 attempt；
//   - never：start 根本没跑到控制器 → 确定没开工；
//   - other：控制器开工了，但 actor 不是这次的 → 结果不明，不能当没开工；
//   - 控制器明确拒绝（节点已 running）→ 事件数没变，确定没开工。
func TestIntegrationStartReconcilesWhenTheReceiptGoesWrong(t *testing.T) {
	script, err := DefaultScript()
	if err != nil {
		t.Skipf("跳过：找不到 projectctl（%v）", err)
	}
	fixture := func(t *testing.T) string {
		src, _ := filepath.Abs("../../testdata/workdeck-fixture")
		dst := filepath.Join(t.TempDir(), "workdeck-fixture")
		if out, err := exec.Command("cp", "-r", src, dst).CombinedOutput(); err != nil {
			t.Fatalf("复制夹具失败：%v %s", err, out)
		}
		resume := exec.Command("python3", script, "--format", "json", "resume", "f05_legacy_path", "--actor", "owner", "--reason", "n044 测试：恢复失败节点")
		resume.Dir = dst
		if out, err := resume.CombinedOutput(); err != nil {
			t.Fatalf("resume 失败：%v %s", err, out)
		}
		return dst
	}
	wrapper := func(t *testing.T, mode string) string {
		p := filepath.Join(t.TempDir(), "wrap.py")
		body := `import sys, subprocess
real, mode, args = ` + "\"" + script + "\"" + `, "` + mode + `", sys.argv[1:]
if "start" in args:
    if mode == "never":
        sys.exit(1)
    if mode == "other":
        i = args.index("--actor"); args[i + 1] = "someone-else"
    subprocess.run([sys.executable, real] + args, capture_output=True)
    sys.exit(1)
sys.exit(subprocess.run([sys.executable, real] + args).returncode)
`
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	req := StartRequest{NodeID: "f05_legacy_path", Actor: "workdeck-test", Role: "fixture_executor",
		RouteClass: "balanced", Model: "fixture-model", Reason: "n044 测试"}

	dst := fixture(t)
	res, err := NewClient(dst, wrapper(t, "lost")).Start(context.Background(), req)
	if err != nil || res.Attempt != 2 || !res.Reconciled {
		t.Fatalf("回执丢了但账本已开工：应当对账确认 attempt 2：%+v %v", res, err)
	}
	// 同一个节点此刻已 running：再开一次，控制器明确拒绝，账本事件数不变，确定没开工。
	_, err = NewClient(dst, script).Start(context.Background(), req)
	var ns *ErrStartNotStarted
	if !errors.As(err, &ns) {
		t.Errorf("控制器拒绝开工应当判为确定没开工：%v", err)
	}

	dst = fixture(t)
	_, err = NewClient(dst, wrapper(t, "never")).Start(context.Background(), req)
	if !errors.As(err, &ns) {
		t.Errorf("start 没跑到控制器应当判为确定没开工：%v", err)
	}

	dst = fixture(t)
	_, err = NewClient(dst, wrapper(t, "other")).Start(context.Background(), req)
	if err == nil || errors.As(err, &ns) || !strings.Contains(err.Error(), "可能已经开工") {
		t.Errorf("账本动了、却对不上这次开工：应当报结果不明，不能当没开工：%v", err)
	}
}
