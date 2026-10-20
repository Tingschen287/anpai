package agentdeck

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 返工 4：外部命令的输出是外部原文，伪造的打码标记在适配层边界上换掉，进不了错误参数，也进不了画面文字。
// 脚本用 printf 的八进制转义输出 U+E000..U+E004。
const forgedOctal = `\356\200\200\356\200\201\356\200\202\356\200\203\356\200\204`

func wireStrings(err error) []string {
	_, args, _ := i18n.Wire(err)
	var out []string
	var walk func(any)
	walk = func(a any) {
		switch v := a.(type) {
		case string:
			out = append(out, v)
		case i18n.Nest:
			for _, x := range v.Args {
				walk(x)
			}
		case i18n.List:
			for _, x := range v.Items {
				walk(x)
			}
		}
	}
	for _, a := range args {
		walk(a)
	}
	return out
}

func TestN060ExecOutputForgedMarkIsNeutralized(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"stdout-json-error": "#!/bin/sh\nprintf '{\"success\":false,\"error\":\"up" + forgedOctal + "stream\"}\\n'\n",
		"stderr-on-failure": "#!/bin/sh\nprintf 'bad" + forgedOctal + "stderr' >&2\nexit 3\n",
	}
	for name, script := range cases {
		t.Run(name, func(t *testing.T) {
			bin := filepath.Join(dir, name)
			if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			c, err := NewClient(bin, "deck", "", "")
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Launch(context.Background(), LaunchRequest{ProjectPath: "/tmp/p", Tool: "claude", Model: "claude-opus-5-5", NoWait: true})
			if err == nil {
				t.Fatal("应该失败")
			}
			ss := wireStrings(err)
			if len(ss) == 0 {
				t.Fatalf("错误参数里没有字符串：%v", err)
			}
			for _, s := range ss {
				if i18n.HasMark(s) {
					t.Errorf("伪造的标记进了错误参数：%q", s)
				}
			}
			if i18n.HasMark(err.Error()) {
				t.Errorf("伪造的标记进了 Error()：%q", err.Error())
			}
			if !strings.Contains(strings.Join(ss, "|"), "�") {
				t.Errorf("伪造的标记应被换成 U+FFFD：%q", ss)
			}
		})
	}
}

// tmux 命令的输出（窗格画面、stderr）同一个边界。
func TestN060TmuxOutputForgedMarkIsNeutralized(t *testing.T) {
	dir := t.TempDir()
	ok := "#!/bin/sh\nprintf 'screen" + forgedOctal + "text\\n'\n"
	bad := "#!/bin/sh\nprintf 'err" + forgedOctal + "x' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(ok), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	lines, err := CaptureSession(context.Background(), "/nonexistent/sock", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if i18n.HasMark(strings.Join(lines, "\n")) {
		t.Errorf("窗格画面里伪造的标记还活着：%q", lines)
	}
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = CaptureSession(context.Background(), "/nonexistent/sock", "s1")
	if err == nil {
		t.Fatal("应该失败")
	}
	ss := wireStrings(err)
	if len(ss) == 0 {
		t.Fatalf("错误参数里没有字符串：%v", err)
	}
	for _, s := range ss {
		if i18n.HasMark(s) {
			t.Errorf("tmux 的 stderr 里伪造的标记进了错误参数：%q", s)
		}
	}
}
