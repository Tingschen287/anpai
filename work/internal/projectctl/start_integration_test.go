package projectctl

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
