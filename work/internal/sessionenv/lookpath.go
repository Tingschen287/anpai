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
