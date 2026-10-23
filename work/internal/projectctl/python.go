package projectctl

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 选 Python 解释器（n067）。
//
// projectctl.py 要 Python 3.11 及以上（用了 datetime.UTC）。macOS 自带的 /usr/bin/python3 是 3.9，
// 过去这里写死 "python3"，在 Mac 上每条命令都报语法/属性错误。现在按顺序取：
//
//  1. 显式指定的：SetPythonOverride（deck daemon 的 -projectctl-python），或环境变量 DECK_PROJECTCTL_PYTHON。
//     指定了就只认它：版本不够或跑不起来是错误，不悄悄换成别的——owner 写了就是要它。
//  2. PATH 上第一个版本 ≥ 3.11 的 python3 / python3.N（N ≥ 11）。同一个目录里先看 python3，再看 python3.N 由高到低。
//     版本不够的（比如 3.9 的 python3）跳过，接着找后面的。
//  3. 都没有：报清楚的错，列出探测到了什么、怎么指定。
//
// 版本用 `<解释器> -c ...` 问出来，不靠名字猜。结果在进程里缓存；没找到的结果只缓存几秒，装好之后很快能找到。

// EnvPython 是指定 projectctl 用的解释器的环境变量。
const EnvPython = "DECK_PROJECTCTL_PYTHON"

// projectctl.py 要求的最低版本。
const (
	minMajor   = 3
	minMinor   = 11
	minVersion = "3.11"
)

var (
	overrideMu     sync.Mutex
	pythonOverride string
)

// SetPythonOverride 设置显式指定的解释器（空表示不指定，回到环境变量与 PATH 探测）。
func SetPythonOverride(path string) {
	overrideMu.Lock()
	pythonOverride = strings.TrimSpace(path)
	overrideMu.Unlock()
}

type pyVersion struct{ major, minor int }

func (v pyVersion) String() string { return fmt.Sprintf("%d.%d", v.major, v.minor) }
func (v pyVersion) ok() bool {
	return v.major > minMajor || v.major == minMajor && v.minor >= minMinor
}

// probeFunc 问一个解释器的版本；测试里换成假的。
type probeFunc func(exe string) (pyVersion, error)

// parsePyVersion 解 probePython 问回来的 "3.12"。
func parsePyVersion(out string) (pyVersion, error) {
	out = strings.TrimSpace(out)
	parts := strings.SplitN(out, ".", 2)
	if len(parts) != 2 {
		return pyVersion{}, fmt.Errorf("unrecognized version output %q", out)
	}
	maj, e1 := strconv.Atoi(parts[0])
	min, e2 := strconv.Atoi(parts[1])
	if e1 != nil || e2 != nil {
		return pyVersion{}, fmt.Errorf("unrecognized version output %q", out)
	}
	return pyVersion{maj, min}, nil
}

var pyMinorName = regexp.MustCompile(`^python3\.(\d+)$`)

// candidates 列出一个目录里可能的解释器：python3，然后 python3.N（N ≥ 最低小版本）由高到低。
func candidates(dir string) []string {
	var out []string
	if isExec(filepath.Join(dir, "python3")) {
		out = append(out, filepath.Join(dir, "python3"))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	type named struct {
		minor int
		path  string
	}
	var versioned []named
	for _, e := range entries {
		m := pyMinorName.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		p := filepath.Join(dir, e.Name())
		if n >= minMinor && isExec(p) {
			versioned = append(versioned, named{n, p})
		}
	}
	sort.Slice(versioned, func(i, j int) bool { return versioned[i].minor > versioned[j].minor })
	for _, v := range versioned {
		out = append(out, v.path)
	}
	return out
}

// isExec 报告 path（跟随软链）是不是可执行的普通文件。
func isExec(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// resolvePython 是选择逻辑本体，输入都是参数，便于测试。
func resolvePython(override, pathEnv string, probe probeFunc, skipDir func(string) bool) (string, error) {
	if override != "" {
		v, err := probe(override)
		if err != nil {
			return "", i18n.E("err.projectctl.interp.override.fail", override, err)
		}
		if !v.ok() {
			return "", i18n.E("err.projectctl.interp.override.old", override, v.String(), minVersion)
		}
		return override, nil
	}
	var seen []string
	done := map[string]bool{}
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" || !filepath.IsAbs(dir) || skipDir(dir) {
			continue // 相对路径的 PATH 项（含空项＝当前目录）不探测：不执行当前目录里的东西
		}
		for _, exe := range candidates(dir) {
			if done[exe] {
				continue
			}
			done[exe] = true
			v, err := probe(exe)
			if err != nil {
				seen = append(seen, exe+" (n/a)")
				continue
			}
			if v.ok() {
				return exe, nil
			}
			seen = append(seen, exe+" ("+v.String()+")")
		}
	}
	if len(seen) == 0 {
		return "", i18n.E("err.projectctl.interp.notfound.none", minVersion, EnvPython)
	}
	return "", i18n.E("err.projectctl.interp.notfound.seen", minVersion, strings.Join(seen, ", "), EnvPython)
}

type pyCacheEntry struct {
	exe  string
	err  error
	when time.Time
}

var (
	pyCacheMu sync.Mutex
	pyCache   = map[string]pyCacheEntry{}
)

// failTTL 是「没找到」的缓存时间：太长则装好 Python 后迟迟不生效，太短则每条命令都重新探测一遍。
const failTTL = 10 * time.Second

// ResolvePython 返回 projectctl 该用的解释器（绝对路径，或 owner 指定的原样）。见文件头的顺序。
func ResolvePython() (string, error) {
	overrideMu.Lock()
	override := pythonOverride
	overrideMu.Unlock()
	if override == "" {
		override = strings.TrimSpace(os.Getenv(EnvPython))
	}
	pathEnv := os.Getenv("PATH")
	key := override + "\x00" + pathEnv
	pyCacheMu.Lock()
	defer pyCacheMu.Unlock()
	if e, ok := pyCache[key]; ok && (e.err == nil || time.Since(e.when) < failTTL) {
		return e.exe, e.err
	}
	exe, err := resolvePython(override, pathEnv, probePython, skipPythonDir)
	pyCache[key] = pyCacheEntry{exe, err, time.Now()}
	return exe, err
}

// PythonInText 是写给人和模型看的命令文字里用的解释器名（旧界面 internal/ui、internal/queue 拼出来的
// `python3 projectctl.py …` 那类可复制命令）。默认就是 "python3"，文字和以前逐字一样；
// 旧界面启动时（work/main.go）若 ResolvePython 选出了解释器，就换成那个路径——Mac 上 PATH 里的 python3 常常是 3.9，
// 照抄命令会跑不了 projectctl。
var PythonInText = "python3"

// PythonForText 返回命令文字里该写的解释器：默认的 "python3" 原样，其他路径按需加引号。
func PythonForText() string {
	p := PythonInText
	if p == "" || p == "python3" || shellSafe.MatchString(p) {
		if p == "" {
			return "python3"
		}
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", `'"'"'`) + "'"
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_./+@%:,-]+$`)
