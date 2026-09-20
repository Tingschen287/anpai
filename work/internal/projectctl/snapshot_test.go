package projectctl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// fixtureRoot 返回夹具项目的路径。
func fixtureRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(moduleRoot(t), "testdata", "workdeck-fixture")
	if _, err := os.Stat(filepath.Join(root, "work-graph.yaml")); err != nil {
		t.Fatalf("夹具不可用，先跑 testdata/rebuild-fixture.sh：%v", err)
	}
	return root
}

// scriptPath 找控制器。找不到就跳过集成测试并说清原因。
func scriptPath(t *testing.T) string {
	t.Helper()
	p, err := DefaultScript()
	if err != nil {
		t.Skipf("跳过：找不到 Graph 控制器，可用 WORKDECK_PROJECTCTL 指定（%v）", err)
	}
	return p
}

func TestSnapshotOnFixtureCoversEveryState(t *testing.T) {
	root := fixtureRoot(t)
	r := NewReader(root, scriptPath(t))
	snap := r.Snapshot(context.Background())

	if len(snap.Problems) > 0 {
		t.Errorf("取数过程报了问题：%v", snap.Problems)
	}
	if snap.ProjectID != "workdeck-fixture" {
		t.Errorf("项目号是 %q，期望 workdeck-fixture", snap.ProjectID)
	}
	if len(snap.Nodes) != 7 {
		t.Fatalf("节点数是 %d，期望 7", len(snap.Nodes))
	}

	want := map[string]State{
		"f01_collect_inputs":  StateAccepted,
		"f02_build_core":      StateRunning,
		"f03_write_report":    StateSubmitted,
		"f04_external_probe":  StateBlocked,
		"f05_legacy_path":     StateFailed,
		"f06_owner_gate":      StateReady,
		"f07_ship_result":     StatePlanned,
	}
	for _, n := range snap.Nodes {
		got, ok := n.State.Get()
		if !ok {
			t.Errorf("节点 %s 状态未知：%s", n.ID, n.State.Reason())
			continue
		}
		if want[n.ID] != got {
			t.Errorf("节点 %s 状态是 %s，期望 %s", n.ID, got, want[n.ID])
		}
	}
}

func TestRunningNodeCarriesAttemptAndElapsed(t *testing.T) {
	r := NewReader(fixtureRoot(t), scriptPath(t))
	snap := r.Snapshot(context.Background())

	running := snap.Running()
	if len(running) != 1 {
		t.Fatalf("运行中节点有 %d 个，期望 1 个", len(running))
	}
	n := running[0]
	attempt, ok := n.Attempt.Get()
	if !ok {
		t.Fatalf("运行中节点拿不到 attempt：%s", n.Attempt.Reason())
	}
	if attempt != 2 {
		t.Errorf("attempt 是 %d，期望 2（夹具里这个节点被打回过一次）", attempt)
	}
	if _, ok := n.Elapsed.Get(); !ok {
		t.Errorf("运行中节点拿不到耗时：%s", n.Elapsed.Reason())
	}
	if _, ok := n.Actor.Get(); !ok {
		t.Errorf("运行中节点拿不到执行者：%s", n.Actor.Reason())
	}
}

func TestWaitingOnOwnerListsBlockedFailedAndSubmitted(t *testing.T) {
	r := NewReader(fixtureRoot(t), scriptPath(t))
	snap := r.Snapshot(context.Background())

	waiting := snap.WaitingOnOwner()
	var ids []string
	for _, n := range waiting {
		ids = append(ids, n.ID)
	}
	if len(waiting) != 3 {
		t.Fatalf("等 owner 的条目有 %d 条，期望 3 条：%v", len(waiting), ids)
	}
	// blocked 最急，排最前。
	if waiting[0].ID != "f04_external_probe" {
		t.Errorf("排最前的是 %s，期望 blocked 的 f04_external_probe", waiting[0].ID)
	}
	if waiting[0].RequiredAction == "" {
		t.Error("blocked 的条目没有带上要 owner 做什么")
	}
}

// 预算里 elapsed 这一维度，控制器不导出已用量。
// 它必须是未知且带原因，绝不能是 0——0 会让人以为一点时间都没花。
func TestElapsedBudgetIsUnknownNotZero(t *testing.T) {
	r := NewReader(fixtureRoot(t), scriptPath(t))
	snap := r.Snapshot(context.Background())

	var found bool
	for _, b := range snap.Budgets {
		if b.Label != "elapsed" {
			continue
		}
		found = true
		if b.Used.IsKnown() {
			t.Error("elapsed 已用量不该是已知的，控制器并不导出它")
		}
		if b.Used.Reason() == "" {
			t.Error("elapsed 已用量是未知，但没有说明原因")
		}
		if _, ok := b.Limit.Get(); !ok {
			t.Errorf("elapsed 上限应当能从 work-graph.yaml 读到：%s", b.Limit.Reason())
		}
	}
	if !found {
		t.Error("预算表里没有 elapsed 这一行")
	}
}

func TestOtherBudgetDimensionsAreKnown(t *testing.T) {
	r := NewReader(fixtureRoot(t), scriptPath(t))
	snap := r.Snapshot(context.Background())

	for _, label := range []string{"worker rounds", "frontier calls", "tokens"} {
		var line *BudgetLine
		for i := range snap.Budgets {
			if snap.Budgets[i].Label == label {
				line = &snap.Budgets[i]
			}
		}
		if line == nil {
			t.Errorf("预算表里没有 %s 这一行", label)
			continue
		}
		if _, ok := line.Used.Get(); !ok {
			t.Errorf("%s 的已用量取不到：%s", label, line.Used.Reason())
		}
		if _, ok := line.Limit.Get(); !ok {
			t.Errorf("%s 的上限取不到：%s", label, line.Limit.Reason())
		}
	}
}

// 这是本轮最重要的一条：取一次快照，项目目录必须一个字节都不变。
func TestSnapshotWritesNothingIntoTheProject(t *testing.T) {
	root := copyFixture(t, fixtureRoot(t))

	r := NewReader(root, scriptPath(t))
	// 先取一次，让控制器该建的锁文件建完，避免把首次创建当成写入。
	assertSnapshotIsLive(t, r.Snapshot(context.Background()))

	before := treeDigest(t, root)
	for i := 0; i < 3; i++ {
		assertSnapshotIsLive(t, r.Snapshot(context.Background()))
	}
	after := treeDigest(t, root)

	if before != after {
		t.Errorf("取快照改动了项目文件：前 %s，后 %s", before, after)
	}
}

// 投影比账本旧的时候，来自投影的数字一律转未知。
func TestStaleProjectionTurnsProjectionNumbersUnknown(t *testing.T) {
	root := copyFixture(t, fixtureRoot(t))

	// 把投影的生成时间往前拨一天，模拟别人写了账本而投影没跟上。
	path := filepath.Join(root, "PROJECT-STATUS.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	lines := strings.Split(string(data), "\n")
	for i, ln := range lines {
		if strings.HasPrefix(ln, "- 生成时间：") {
			lines[i] = "- 生成时间：" + old
		}
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewReader(root, scriptPath(t))
	snap := r.Snapshot(context.Background())

	if !snap.Fresh.Stale {
		t.Fatal("投影明显落后了，却没有被判为过期")
	}
	if snap.Fresh.Note == "" {
		t.Error("判为过期却没有给出说明")
	}
	for _, n := range snap.Nodes {
		if n.Attempt.IsKnown() {
			t.Errorf("投影已过期，节点 %s 的 attempt 不该还是已知的", n.ID)
		}
		if n.Elapsed.IsKnown() {
			t.Errorf("投影已过期，节点 %s 的耗时不该还是已知的", n.ID)
		}
	}
	for _, b := range snap.Budgets {
		if b.Used.IsKnown() {
			t.Errorf("投影已过期，%s 的已用量不该还是已知的", b.Label)
		}
	}
	// 状态本身来自控制器而不是投影，所以它必须照常可用。
	for _, n := range snap.Nodes {
		if !n.State.IsKnown() {
			t.Errorf("节点 %s 的状态来自控制器，不该因为投影过期而丢失", n.ID)
		}
	}
}

// copyFixture 把夹具复制到临时位置再返回新的项目根。
//
// 控制器要求项目目录名必须等于 project_id，所以不能直接用 t.TempDir() 的随机名，
// 得在里面再建一层同名目录。忽略这一点时每条命令都会验证失败，
// 而「什么都没跑」和「跑了但没写文件」在目录哈希上长得一模一样。
func copyFixture(t *testing.T, src string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), filepath.Base(src))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	copyTree(t, src, root)
	return root
}

// assertSnapshotIsLive 确认这一次取数真的跑通了控制器。
//
// 节点清单来自 work-graph.yaml，就算每条命令都失败，快照里照样有七个节点，
// 所以必须查状态是不是真取到了，否则这就是一次自我安慰的断言。
func assertSnapshotIsLive(t *testing.T, snap Snapshot) {
	t.Helper()
	if len(snap.Nodes) == 0 {
		t.Fatal("快照里没有节点，取数没真的跑起来")
	}
	if len(snap.Problems) > 0 {
		t.Fatalf("取数报了问题，说明控制器没跑通：%v", snap.Problems)
	}
	for _, n := range snap.Nodes {
		if !n.State.IsKnown() {
			t.Fatalf("节点 %s 的状态没取到（%s），取数没真的跑通", n.ID, n.State.Reason())
		}
	}
	if snap.Chain.Events == 0 {
		t.Fatal("账本事件数是 0，取数没真的跑通")
	}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// treeDigest 把整棵目录树摘成一个哈希，路径和内容都算进去。
func treeDigest(t *testing.T, root string) string {
	t.Helper()
	var paths []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)

	h := sha256.New()
	for _, p := range paths {
		rel, _ := filepath.Rel(root, p)
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(h, "%s\x00%x\x00", rel, sha256.Sum256(data))
	}
	return hex.EncodeToString(h.Sum(nil))
}
