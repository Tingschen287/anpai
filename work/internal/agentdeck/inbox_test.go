package agentdeck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// 去重键要和上游 EventFingerprint 认同一件事：空白与大小写不同的同一条记录算同一个键。
func TestFingerprintNormalizesLikeUpstream(t *testing.T) {
	base := InboxRecord{ChildSessionID: "c1", FromStatus: "running", ToStatus: "waiting", LastOutputHash: "h1"}
	variants := []InboxRecord{
		{ChildSessionID: " c1 ", FromStatus: "running", ToStatus: "waiting", LastOutputHash: "h1"},
		{ChildSessionID: "c1", FromStatus: "RUNNING", ToStatus: "Waiting", LastOutputHash: "h1"},
		{ChildSessionID: "c1", FromStatus: " running", ToStatus: "waiting ", LastOutputHash: " h1 "},
	}
	for i, v := range variants {
		if v.Fingerprint() != base.Fingerprint() {
			t.Errorf("变体 %d 与原记录应当同键", i)
		}
	}
	fin := InboxRecord{ChildSessionID: "c4", Kind: "finished", DoneStatus: "ok", DoneSummary: "done"}
	finUpper := InboxRecord{ChildSessionID: "c4", Kind: "finished", DoneStatus: " OK ", DoneSummary: " done "}
	if fin.Fingerprint() != finUpper.Fingerprint() {
		t.Error("完成记录的 done_status 大小写与空白不该影响键")
	}
	// 时间戳不参与；换了输出就是另一条。
	other := base
	other.LastOutputHash = "h2"
	if other.Fingerprint() == base.Fingerprint() {
		t.Error("输出不同应当是不同的键")
	}
	// 与上游拼法逐字一致：source|child|from|to|hash。
	sum := sha256.Sum256([]byte("|c1|running|waiting|h1"))
	if base.Fingerprint() != hex.EncodeToString(sum[:]) {
		t.Error("拼法与上游 EventFingerprint 不一致")
	}
}

// 进程表里找 notify-daemon：程序名含 agent-deck 且参数里有 notify-daemon 才算。
func TestNotifyDaemonRunningReadsTheProcessTable(t *testing.T) {
	dir := t.TempDir()
	old := procRoot
	procRoot = dir
	defer func() { procRoot = old }()
	write := func(pid, cmdline string) {
		os.MkdirAll(filepath.Join(dir, pid), 0o755)
		os.WriteFile(filepath.Join(dir, pid, "cmdline"), []byte(cmdline), 0o644)
	}
	c := &Client{Profile: "_t"}
	write("100", "/usr/bin/bash\x00-c\x00echo notify-daemon\x00")
	write("101", "/home/u/.local/bin/agent-deck\x00list\x00--json\x00")
	// 参数正好是 notify-daemon，但程序不是 agent-deck：不算。
	write("103", "/usr/bin/python3\x00notify-daemon\x00")
	os.MkdirAll(filepath.Join(dir, "self"), 0o755)
	if up, err := c.NotifyDaemonRunning(context.Background()); err != nil || up {
		t.Fatalf("没有 daemon 时应当答 false：%v %v", up, err)
	}
	write("102", "/home/u/.local/bin/agent-deck\x00notify-daemon\x00")
	if up, err := c.NotifyDaemonRunning(context.Background()); err != nil || !up {
		t.Fatalf("有 daemon 时应当答 true：%v %v", up, err)
	}
	procRoot = filepath.Join(dir, "does-not-exist")
	if _, err := c.NotifyDaemonRunning(context.Background()); err == nil {
		t.Error("读不了进程表时应当报错，不能当作没在跑或在跑")
	}
}
