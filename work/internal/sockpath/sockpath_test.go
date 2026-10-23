package sockpath

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/testsock"
)

// longPath 造一个一定超长的 socket 路径（目录不必存在）。
func longPath(base string) string {
	return filepath.Join(base, strings.Repeat("d", Max), "deckd.sock")
}

func withTmp(t *testing.T) string {
	t.Helper()
	old := tmpRoot
	tmpRoot = testsock.Dir(t)
	t.Cleanup(func() { tmpRoot = old })
	return tmpRoot
}

// 放得下的路径原样返回，不建任何目录。
func TestFitKeepsShortPaths(t *testing.T) {
	root := withTmp(t)
	p := filepath.Join(testsock.Dir(t), "deckd.sock")
	got, err := Fit(p)
	if err != nil || got != p {
		t.Fatalf("短路径应原样：%q %v", got, err)
	}
	if _, err := os.Stat(FallbackDir()); !os.IsNotExist(err) {
		t.Fatalf("短路径不该建退路目录（%s）：%v", root, err)
	}
	if len(p) > Max {
		t.Fatalf("测试前提：%d 字节的路径应在上限 %d 内", len(p), Max)
	}
}

// 超长路径退到 <退路>/<原路径哈希>/<原文件名>：放得下上限、两层目录都是 0700 且属本用户；同一路径永远得到同一个结果，
// 不同路径各得各的目录（deckd 的单实例锁与 socket 同目录，所以不会互相串）。
func TestFitFallsBackToAShortPrivateDirectory(t *testing.T) {
	withTmp(t)
	a, b := longPath("/x/a"), longPath("/x/b")
	ga, err := Fit(a)
	if err != nil {
		t.Fatal(err)
	}
	if len(ga) > Max || filepath.Base(ga) != "deckd.sock" {
		t.Fatalf("退路应放得下且文件名不变：%q（%d 字节，上限 %d）", ga, len(ga), Max)
	}
	again, _ := Fit(a)
	gb, _ := Fit(b)
	if again != ga || gb == ga || filepath.Dir(gb) == filepath.Dir(ga) {
		t.Fatalf("同一路径同一结果、不同路径不同目录：%q %q %q", ga, again, gb)
	}
	for _, d := range []string{FallbackDir(), filepath.Dir(ga)} {
		fi, err := os.Lstat(d)
		if err != nil {
			t.Fatal(err)
		}
		st := fi.Sys().(*syscall.Stat_t)
		if !fi.IsDir() || fi.Mode().Perm() != 0o700 || int(st.Uid) != os.Getuid() {
			t.Fatalf("%s 应是本用户的 0700 目录：%v uid=%d", d, fi.Mode(), st.Uid)
		}
	}
	// 真能 bind；对照：原来那条超长路径 bind 不了（这就是要退路的原因）
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: ga, Net: "unix"})
	if err != nil {
		t.Fatalf("退路上 bind 失败：%v", err)
	}
	ln.Close()
	if ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: a, Net: "unix"}); err == nil {
		ln.Close()
		t.Fatal("对照失效：超长路径居然能 bind")
	}
}

// 上限本身是对的：正好 Max 字节的路径在本系统上 bind 得上（macOS 104 字节含结尾的 NUL，所以是 103）。
func TestMaxBytePathCanBind(t *testing.T) {
	dir := testsock.Dir(t)
	name := strings.Repeat("s", Max-len(dir)-1)
	p := filepath.Join(dir, name)
	if len(p) != Max {
		t.Fatalf("测试前提：路径应正好 %d 字节，实际 %d", Max, len(p))
	}
	if got, err := Fit(p); err != nil || got != p {
		t.Fatalf("正好 Max 字节应原样：%q %v", got, err)
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: p, Net: "unix"})
	if err != nil {
		t.Fatalf("%d 字节的路径 bind 失败：%v", len(p), err)
	}
	ln.Close()
}

// 退路目录被别人占着（软链、普通文件）：不用，报清楚的错，不把超长路径交给 bind。
func TestFitRefusesAnUnsafeFallbackDirectory(t *testing.T) {
	root := withTmp(t)
	target := testsock.Dir(t)
	if err := os.Symlink(target, FallbackDir()); err != nil {
		t.Fatal(err)
	}
	if got, err := Fit(longPath("/x")); err == nil || !strings.Contains(err.Error(), "不是目录") {
		t.Fatalf("软链应被拒绝：%q %v", got, err)
	}
	os.Remove(FallbackDir())
	if err := os.WriteFile(FallbackDir(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Fit(longPath("/x")); err == nil {
		t.Fatalf("普通文件应被拒绝：%q（%s）", got, root)
	}
}

// 本用户自己的退路目录权限被放宽过：收紧回 0700 再用。
func TestFitTightensOurOwnLooseDirectory(t *testing.T) {
	withTmp(t)
	if err := os.Mkdir(FallbackDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	os.Chmod(FallbackDir(), 0o755)
	got, err := Fit(longPath("/x"))
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(FallbackDir()); fi.Mode().Perm() != 0o700 {
		t.Fatalf("应收紧到 0700：%v（%s）", fi.Mode().Perm(), got)
	}
}

// Check：超长给出带字节数与上限的清楚错误，不超长返回 nil。
func TestCheckReportsLengthAndLimit(t *testing.T) {
	if err := Check("/tmp/x.sock"); err != nil {
		t.Fatal(err)
	}
	err := Check(longPath("/x"))
	if err == nil || !strings.Contains(err.Error(), "太长") {
		t.Fatalf("超长应报错：%v", err)
	}
}
