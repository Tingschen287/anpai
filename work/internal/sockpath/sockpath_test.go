package sockpath

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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

// 审查 1 的反例：这两条原始路径的 sha256 前 5 个字节（40 位）相同，旧做法把它们退到同一个目录，共用 socket 和单实例锁。
// 现在哈希至少 128 位，各得各的目录。
const (
	collidingA = "/tmp/n067-review-ssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssss/779990/deckd.sock"
	collidingB = "/tmp/n067-review-ssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssss/1667899/deckd.sock"
)

func TestReviewCollidingSocketPaths(t *testing.T) {
	withTmp(t)
	sa, sb := sha256.Sum256([]byte(collidingA)), sha256.Sum256([]byte(collidingB))
	if !bytes.Equal(sa[:5], sb[:5]) || sa == sb {
		t.Fatal("测试前提：两条路径 sha256 的前 5 个字节相同、整体不同")
	}
	a, ea := Fit(collidingA)
	b, eb := Fit(collidingB)
	if ea != nil || eb != nil {
		t.Fatalf("Fit 出错：%v %v", ea, eb)
	}
	if a == b || filepath.Dir(a) == filepath.Dir(b) {
		t.Fatalf("不同的状态目录共用了 socket 或 deckd.lock 所在目录：%s", a)
	}
}

// 哈希目录名是原路径 sha256 的十六进制前缀，不少于 32 个字符（128 位）；能放得下完整的 64 个字符就用完整的。
func TestFitHashIsAtLeast128Bits(t *testing.T) {
	withTmp(t)
	p := longPath("/x/a")
	got, err := Fit(p)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(filepath.Dir(got))
	sum := sha256.Sum256([]byte(p))
	if len(name) < MinHashChars || !strings.HasPrefix(hex.EncodeToString(sum[:]), name) {
		t.Fatalf("哈希目录名应是 sha256 十六进制的前缀且不少于 %d 个字符：%q", MinHashChars, name)
	}
	if len(name) != 64 {
		t.Fatalf("放得下时应用完整的 sha256（64 个字符）：%q（%d 字节的路径）", name, len(got))
	}
	if len(got) > Max {
		t.Fatalf("放不下：%d > %d", len(got), Max)
	}
}

// 退路目录或文件名长到放不下完整哈希时截短，但不少于 32 个字符；再长就报错，不把超长路径交给 bind。
func TestFitTruncatesTheHashButNeverBelow128Bits(t *testing.T) {
	root := withTmp(t)
	fb := FallbackDir()
	// 文件名占掉一些，留给哈希的恰好 40 个字符
	room := Max - len(fb) - 1 - 1 - 40
	got, err := Fit(filepath.Join("/x", strings.Repeat("d", Max), strings.Repeat("n", room)))
	if err != nil {
		t.Fatal(err)
	}
	if name := filepath.Base(filepath.Dir(got)); len(name) != 40 || len(got) > Max {
		t.Fatalf("应截到 40 个字符、路径放得下：%q %d", name, len(got))
	}
	// 只剩 31 个字符：放弃
	room = Max - len(fb) - 1 - 1 - (MinHashChars - 1)
	if got, err := Fit(filepath.Join("/x", strings.Repeat("d", Max), strings.Repeat("n", room))); err == nil {
		t.Fatalf("哈希不足 128 位的路径应被拒绝：%q（%s）", got, root)
	}
}

// 标记文件记下原始路径；同一原路径再来核对一致、不改标记。
func TestFitWritesAndReusesTheOriginMarker(t *testing.T) {
	withTmp(t)
	p := longPath("/x/a")
	got, err := Fit(p)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(filepath.Dir(got), OriginFile)
	b, err := os.ReadFile(marker)
	if err != nil || string(b) != p {
		t.Fatalf("标记应记下原始路径：%q %v", b, err)
	}
	if fi, _ := os.Stat(marker); fi.Mode().Perm() != 0o600 {
		t.Fatalf("标记权限应是 0600：%v", fi.Mode().Perm())
	}
	again, err := Fit(p)
	if err != nil || again != got {
		t.Fatalf("同一原路径应得到同一个结果：%q %q %v", again, got, err)
	}
	// 目录里只有标记，没有临时文件的残留
	ents, _ := os.ReadDir(filepath.Dir(got))
	if len(ents) != 1 || ents[0].Name() != OriginFile {
		t.Fatalf("目录里应只有标记文件：%v", ents)
	}
}

// 标记对不上（哈希撞了，或目录被别人动过）：报错，不共用这个目录。
func TestFitRefusesAHashDirectoryMarkedForAnotherPath(t *testing.T) {
	withTmp(t)
	p := longPath("/x/a")
	got, err := Fit(p)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(filepath.Dir(got), OriginFile)
	if err := os.WriteFile(marker, []byte("/some/other/path/deckd.sock"), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := Fit(p)
	if err == nil || !strings.Contains(err.Error(), "不共用") {
		t.Fatalf("标记是另一条路径时应拒绝：%q %v", g, err)
	}
	// 标记是空的（写到一半）也不能当成「还没有」去覆盖
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if g, err := Fit(p); err == nil {
		t.Fatalf("空标记应拒绝：%q", g)
	}
	// 标记被换成软链
	os.Remove(marker)
	target := filepath.Join(testsock.Dir(t), "t")
	os.WriteFile(target, []byte(p), 0o600)
	if err := os.Symlink(target, marker); err != nil {
		t.Fatal(err)
	}
	if g, err := Fit(p); err == nil {
		t.Fatalf("软链标记应拒绝：%q", g)
	}
}

// 哈希目录已存在但还没有标记（上次在写标记之前就中断了）：补上标记再用。
func TestFitAdoptsAnExistingHashDirectoryWithoutMarker(t *testing.T) {
	withTmp(t)
	p := longPath("/x/a")
	got, err := Fit(p)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(filepath.Dir(got), OriginFile))
	again, err := Fit(p)
	if err != nil || again != got {
		t.Fatalf("没有标记的本用户目录应补上标记后复用：%q %v", again, err)
	}
	if b, _ := os.ReadFile(filepath.Join(filepath.Dir(got), OriginFile)); string(b) != p {
		t.Fatalf("标记应补上：%q", b)
	}
}
