package sessionenv

import (
	"os"
	"os/exec"
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

// executable 问的是「当前进程的有效身份真能执行它吗」，不是「任意一类用户有执行位」（n060 段 4 返工 2，R1b）。
//
// 旧写法只看 Mode().Perm()&0o111：文件被改成 0645（属主不能执行、其他人能）时它仍判可执行，而实际的查找（Go 的 exec.LookPath、shell）会跳过这个文件，
// 去跑后面目录里的同名文件——设置里显示的、trust 门核准的、绑定核对放行的，就和真正会被启动的对不上。
// 现在对绝对路径直接调 exec.LookPath：它在 unix 上走 faccessat(X_OK)（有效 uid/gid，含属组与目录的搜索权限、noexec 挂载等），
// 与实际启动用的是同一套判断，系统调用不可用（ENOSYS、容器 seccomp 的 EPERM）时才退回权限位。darwin、linux 都能编译，不引入依赖。
// 仍要求是普通文件（跟随符号链接）。设置里的「已装」查找、trust 门、启动前的绑定核对都经 LookPath，用的就是这一个判断。
//
// 以 root 运行时的差别（只分析）：内核对 root 的 X_OK 要求至少有一个执行位，所以 0644 的文件 root 仍不能执行，
// 0645、0100 这类 root 能执行；root 不受属主/属组位限制，这与 exec.LookPath 一致，此处不另做处理。
func executable(p string) bool {
	if !filepath.IsAbs(p) { // 只处理绝对路径：相对路径会让 exec.LookPath 去看当前目录
		return false
	}
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	_, err = exec.LookPath(p) // 带斜杠：只做 findExecutable，不看 PATH
	return err == nil
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
