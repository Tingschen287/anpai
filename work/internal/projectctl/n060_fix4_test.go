package projectctl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 返工 4：projectctl 子进程的输出和项目文件是外部原文，伪造的打码标记在边界上换掉。
const forgedOctal = `\356\200\200\356\200\201\356\200\202\356\200\203\356\200\204`

func argStrings(err error) []string {
	_, args, _ := i18n.Wire(err)
	var out []string
	for _, a := range args {
		if s, ok := a.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func TestN060ProjectctlOutputForgedMarkIsNeutralized(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"envelope-error": "#!/bin/sh\nprintf '{\"ok\":false,\"error\":\"a" + forgedOctal + "b\"}\\n'\n",
		"stderr":         "#!/bin/sh\nprintf 'bad" + forgedOctal + "x' >&2\nexit 3\n",
	}
	for name, script := range cases {
		t.Run(name, func(t *testing.T) {
			bin := filepath.Join(dir, name)
			if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			c := NewClient(dir, "ignored.py")
			c.Python = bin
			_, err := c.run(context.Background(), "validate")
			if err == nil {
				t.Fatal("应该失败")
			}
			ss := argStrings(err)
			if len(ss) == 0 {
				t.Fatalf("错误参数里没有字符串：%v", err)
			}
			for _, s := range ss {
				if i18n.HasMark(s) {
					t.Errorf("伪造的标记进了错误参数：%q", s)
				}
			}
			if !strings.Contains(strings.Join(ss, "|"), "�") {
				t.Errorf("伪造的标记应被换成 U+FFFD：%q", ss)
			}
		})
	}
}

func TestN060ProjectFilesForgedMarkIsNeutralized(t *testing.T) {
	dir := t.TempDir()
	graph := "nodes:\n  - id: n1\n    title: \"t" + i18n.RedactMark + "x\"\n"
	if err := os.WriteFile(filepath.Join(dir, "work-graph.yaml"), []byte(graph), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := LoadGraph(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Nodes) != 1 || !strings.Contains(g.Nodes[0].Title, "\ufffd") {
		t.Fatalf("work-graph.yaml 的标记应被换成 U+FFFD：%+v", g.Nodes)
	}
	for _, n := range g.Nodes {
		if i18n.HasMark(n.Title) {
			t.Errorf("work-graph.yaml 里伪造的标记还活着：%q", n.Title)
		}
	}
	status := "## 下一可执行节点\n| id |\n|---|\n| n1" + i18n.RedactMark + " |\n"
	if err := os.WriteFile(filepath.Join(dir, "PROJECT-STATUS.md"), []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := LoadProjection(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.States) == 0 {
		t.Fatal("PROJECT-STATUS.md 没解析出节点，测试没有测到东西")
	}
	for id := range p.States {
		if i18n.HasMark(id) {
			t.Errorf("PROJECT-STATUS.md 里伪造的标记还活着：%q", id)
		}
	}
}
