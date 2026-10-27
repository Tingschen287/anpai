package sessionenv

import (
	"os"
	"path/filepath"
	"strings"
)

// LookPath 在给定的 PATH（冒号分隔）里找可执行文件，返回第一个命中的绝对路径（不解开符号链接：会话里 argv[0] 也是这个路径）。
// name 带斜杠时只看这个路径本身（必须是绝对路径）。找不到返回空串。
// 不看进程自己的 PATH，也不看当前目录：这是「用会话那份 PATH 找」的唯一入口。
func LookPath(path, name string) string {
	if name == "" {
		return ""
	}
	if strings.ContainsRune(name, '/') {
		if filepath.IsAbs(name) && executable(name) {
			return filepath.Clean(name)
		}
		return ""
	}
	for _, d := range filepath.SplitList(path) {
		if !filepath.IsAbs(d) {
			continue
		}
		if c := filepath.Join(d, name); executable(c) {
			return c
		}
	}
	return ""
}

func executable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
}

// Env 返回 base 的副本，PATH 换成 path（没有就加上）。子进程用它，这样 `#!/usr/bin/env node` 之类的脚本也按会话 PATH 找解释器。
func Env(base []string, path string) []string {
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if !strings.HasPrefix(kv, "PATH=") {
			out = append(out, kv)
		}
	}
	return append(out, "PATH="+path)
}

// FrontDir 返回把目录 dir 放到最前面的 PATH（n060 段 4 返工，R1）：其余目录保持原来的顺序，重复的目录（包括 dir 在后面的那一处）去掉，
// 相对目录、含控制字符或过长的目录丢掉（与 cleanList 一样：相对目录会让查找跟着当前目录走）。dir 必须是绝对路径，否则原样返回 path 并报 false。
//
// 用途：Codex trust 门核准的是「按会话 PATH 找到的那个文件」，而上游 agent-deck 起会话时又会按工具名在交给它的 PATH 里再找一次。
// 核准的那个文件所在的目录放到最前，之后任何更靠前的目录里新装的同名文件都顶替不了它。
// 这只绑定「路径」：同一个路径上的文件被替换（内容变了、符号链接被改指）不在它管的范围内。
func FrontDir(path, dir string) (string, bool) {
	if !filepath.IsAbs(dir) {
		return path, false
	}
	front := filepath.Clean(dir)
	if len(front) > maxEntry || hasControl(front) {
		return path, false
	}
	rest, _ := cleanList(filepath.SplitList(path))
	out := []string{front}
	seen := map[string]bool{front: true}
	for _, d := range rest {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return strings.Join(out, ":"), true
}
