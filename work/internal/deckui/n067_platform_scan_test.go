package deckui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// n067 审查 1：文字表检查器只装载本平台的源文件，_linux.go 里的错键在 Mac 上跑测试时从来没被检查过（反过来也一样）。
// 现在 darwin 与 linux 各装载、各做一遍完整的类型检查（checkModuleAll）。下面的测试把这件事钉住：
// 每个非测试源文件至少被检查一次，且「同一个错放在 _linux.go 里、放在 _darwin.go 里，在任何一个平台上跑都要失败」。

// 模块里所有的非测试 .go 文件（含各平台专用的）。
func allModuleSourceFiles(t *testing.T) []string {
	t.Helper()
	dirs, err := modulePkgDirs()
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, d := range dirs {
		ents, err := os.ReadDir(d)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			n := e.Name()
			if !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
				files = append(files, filepath.Join(d, n))
			}
		}
	}
	sort.Strings(files)
	return files
}

// 每个源文件至少在一个平台上被类型检查；两个平台各自检查的文件集合不同（专用文件只在自己的平台）；耗时写进日志。
func TestN060TypeCheckCoversEveryFileOnBothPlatforms(t *testing.T) {
	t0 := time.Now()
	plats, err := checkModuleAll(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("两个平台的全模块类型检查（缓存可能已热）：%v", time.Since(t0).Round(time.Millisecond))
	if len(plats) != len(typeCheckPlatforms) {
		t.Fatalf("应有 %d 个平台的结果，实际 %d", len(typeCheckPlatforms), len(plats))
	}
	checked := map[string]map[string]bool{} // 平台 -> 文件
	union := map[string]bool{}
	for _, pp := range plats {
		checked[pp.goos] = map[string]bool{}
		n := 0
		for _, cp := range pp.pkgs {
			for _, pf := range cp.files {
				checked[pp.goos][pf.path] = true
				union[pf.path] = true
				n++
			}
		}
		t.Logf("%s：%d 个包，%d 个源文件", pp.goos, len(pp.pkgs), n)
		if n == 0 {
			t.Errorf("%s 平台没有检查任何文件", pp.goos)
		}
	}
	for _, f := range allModuleSourceFiles(t) {
		if !union[f] {
			t.Errorf("%s 在任何平台上都没被类型检查（构建约束同时排除了 %v？）", f, typeCheckPlatforms)
		}
	}
	// 两个平台真的不同：专用文件各在各的平台
	root := moduleRoot()
	for _, c := range []struct{ file, goos, other string }{
		{"internal/projectctl/python_linux.go", "linux", "darwin"},
		{"internal/projectctl/python_darwin.go", "darwin", "linux"},
		{"internal/identity/identity_linux.go", "linux", "darwin"},
		{"internal/identity/identity_darwin.go", "darwin", "linux"},
	} {
		p := filepath.Join(root, c.file)
		if !checked[c.goos][p] || checked[c.other][p] {
			t.Errorf("%s 应只在 %s 平台上被检查：%s=%v %s=%v", c.file, c.goos, c.goos, checked[c.goos][p], c.other, checked[c.other][p])
		}
	}
}

// plantBadKey 往一个源文件末尾种一个不在文字表里的常量键。imports 是单文件的导入，所以另加一行别名导入。
func plantBadKey(t *testing.T, rel string) map[string]string {
	t.Helper()
	path := filepath.Join(moduleRoot(), rel)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := string(data)
	idx := strings.Index(changed, "package ")
	end := idx + strings.Index(changed[idx:], "\n")
	changed = changed[:end] + "\nimport plantedI18n \"github.com/Tingschen287/terminal-deck/work/internal/i18n\"\n" + changed[end+1:]
	changed += "\nfunc plantedBadKey() string { return plantedI18n.T(\"planted.typo.missing\") }\n"
	return map[string]string{path: changed}
}

// 同一个错键，放在 _linux.go 里、放在 _darwin.go 里，在这台机器上跑都要失败（审查 1 的反例 deckui-overlay.go）。
func TestN060KeyInvariantCatchesAWrongKeyInPlatformSpecificFiles(t *testing.T) {
	for _, rel := range []string{
		"internal/projectctl/python_linux.go",
		"internal/projectctl/python_darwin.go",
		"internal/identity/identity_linux.go",
		"internal/identity/identity_darwin.go",
	} {
		t.Run(filepath.Base(rel), func(t *testing.T) {
			probs, err := dynamicCallProblems(plantBadKey(t, rel))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, p := range probs {
				if strings.Contains(p, "planted.typo.missing") && strings.Contains(p, filepath.Base(rel)) {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s 里的错键没被抓到：%v", rel, probs)
			}
		})
	}
}

// 参数里的本地中文扫描同理：种在只属于一个平台的文件里，也要被抓到。
func TestN060ArgScanFindsPlantedChineseInPlatformSpecificFiles(t *testing.T) {
	src := map[string]string{}
	for _, c := range []struct{ rel, fn string }{
		{"internal/agentdeck/procs_linux.go", "plantedLinuxOnly"},
		{"internal/agentdeck/procs_darwin.go", "plantedDarwinOnly"},
	} {
		path := filepath.Join(moduleRoot(), c.rel)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		s := string(data)
		idx := strings.Index(s, "package ")
		end := idx + strings.Index(s[idx:], "\n")
		s = s[:end] + "\nimport plantedI18n \"github.com/Tingschen287/terminal-deck/work/internal/i18n\"\n" + s[end+1:]
		s += "\nfunc " + c.fn + "() { _ = plantedI18n.E(\"err.deckd.serverrunspawn.47\", \"本地中文\") }\n"
		src[path] = s
	}
	hits, err := scanArgChineseAll(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ file, fn string }{
		{"procs_linux.go", "plantedLinuxOnly"},
		{"procs_darwin.go", "plantedDarwinOnly"},
	} {
		found := false
		for _, h := range hits {
			if h.site == c.file+" · "+c.fn {
				found = true
			}
		}
		if !found {
			t.Errorf("%s 里种的本地中文没被扫描抓到", c.file)
		}
	}
}
