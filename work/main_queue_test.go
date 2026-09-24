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
