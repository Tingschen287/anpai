package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

// --once 不开界面，也要把决策队列的告警画出来：队列文件坏了、上游没配置，都不能一声不吭。
func TestOnceRenderCarriesTheQueueAlert(t *testing.T) {
	snap := projectctl.Snapshot{ProjectID: "p", ProjectRoot: "/tmp/p"}

	dir := t.TempDir()
	t.Setenv("WORKDECK_CONFIG", filepath.Join(dir, "config.json"))
	os.WriteFile(filepath.Join(dir, "queue.json"), []byte(`{"version":1}`), 0o600)
	out := renderOnce(context.Background(), snap, "p", 100)
	if !strings.Contains(out, "决策队列打不开") || strings.Contains(out, "决策队列为空") {
		t.Errorf("队列文件坏了，--once 必须告警：\n%s", out)
	}

	dir = t.TempDir()
	t.Setenv("WORKDECK_CONFIG", filepath.Join(dir, "config.json")) // 没有配置文件：上游取不到
	out = renderOnce(context.Background(), snap, "p", 100)
	if !strings.Contains(out, "没有配置 upstream agent-deck") || strings.Contains(out, "决策队列为空") {
		t.Errorf("没配置上游，--once 必须说明运行时来源取不到：\n%s", out)
	}
}

// --once 与交互界面用同一套 Graph 合并判断：取数失败的空快照不结案旧门。
func TestOnceKeepsGatesWhenTheSnapshotFailed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WORKDECK_CONFIG", filepath.Join(dir, "config.json"))
	gate := projectctl.Node{ID: "g1", Kind: "human_gate", Risk: "high",
		State: projectctl.Known(projectctl.StateReady), Attempt: projectctl.Unknown[int]("没开过工")}
	renderOnce(context.Background(), projectctl.Snapshot{ProjectID: "p", Nodes: []projectctl.Node{gate}}, "p", 100)
	out := renderOnce(context.Background(), projectctl.Snapshot{ProjectID: "p", Problems: []string{"brief 失败"}}, "p", 100)
	if !strings.Contains(out, "1 条待你拍板") {
		t.Errorf("取数失败的空快照不该让门离开队列：\n%s", out)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "queue.json"))
	if !strings.Contains(string(data), `"status": "open"`) {
		t.Errorf("落盘的门应当仍是 open：\n%s", data)
	}
}
