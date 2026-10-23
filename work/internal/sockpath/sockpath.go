// Package sockpath 管 Unix socket 路径的长度（n067）。
//
// sockaddr_un 的 sun_path 在 macOS 上只有 104 字节（含结尾的 NUL），Linux 是 108。路径一长，
// bind、connect 就报 invalid argument，看不出原因。deck 的 socket 路径来自 $XDG_STATE_HOME、$TMUX_TMPDIR 这类
// 用户可改的环境，隔离测试和沙箱里尤其容易超长。
//
// 做法：路径放得下就原样用；放不下就退到 /tmp/deck-<uid>/<原路径哈希>/<原文件名>——同一个原路径永远得到
// 同一个短路径，守护进程和客户端各自算都一致，文件名不变，同目录的伴生文件（deckd.lock）也跟着各得其所。
// 哈希是原路径 sha256 的十六进制：放得下就用完整的 64 个字符，放不下就截短，但不少于 32 个字符（128 位）——
// 40 位的哈希几百万次就能撞出两条不同的路径（n067 审查 1 实测），撞上就会共用一个 socket 和一把单实例锁。
// 每个哈希目录里写一个标记文件（origin），记下原始路径；再次用到这个目录时核对一致，不一致就报错，不共用。
// 两层目录权限都是 0700，已存在时核对是真目录（不是软链）、属主是本用户。
// 退不了（目录被别人占着、不是目录、建不出来、标记对不上）就报清楚的错，不把超长路径交给 bind。
package sockpath

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// MinHashChars 是退路目录名里哈希的最少十六进制字符数（128 位）。
const MinHashChars = 32

// OriginFile 是哈希目录里记下原始路径的标记文件名。
const OriginFile = "origin"

// tmpRoot 是退路目录所在的位置；测试里换成别处，免得动真实的 /tmp/deck-<uid>。
var tmpRoot = "/tmp"

// SetFallbackRoot 把退路目录所在的位置换成 root，返回还原函数。只给别的包的测试用：测试里造「退路目录被别人占着」，
// 不能去动真实的 /tmp/deck-<uid>。
func SetFallbackRoot(root string) (restore func()) {
	old := tmpRoot
	tmpRoot = root
	return func() { tmpRoot = old }
}

// FallbackDir 是退路目录：/tmp/deck-<uid>。
func FallbackDir() string { return filepath.Join(tmpRoot, fmt.Sprintf("deck-%d", os.Getuid())) }

// Check 报告路径是不是放得下；放不下给出清楚的错误。
func Check(path string) error {
	if n := len(path); n > Max {
		return fmt.Errorf("socket 路径太长（%d 字节，本系统上限 %d 字节）：%s", n, Max, path)
	}
	return nil
}

// hashChars 算出退路目录名用多少个十六进制字符：完整的 64 个放得下就全用，否则截到刚好放得下，少于 MinHashChars 就放弃。
func hashChars(prefixLen, nameLen int) (int, bool) {
	// <退路目录>/<哈希>/<文件名>
	n := Max - prefixLen - 1 - nameLen - 1
	if n < MinHashChars {
		return 0, false
	}
	return min(n, 2*sha256.Size), true
}

// Fit 返回一个放得下的 socket 路径：preferred 放得下就是它本身，否则是退路目录里的短路径。
func Fit(preferred string) (string, error) {
	if len(preferred) <= Max {
		return preferred, nil
	}
	tooLong := func(where string, err error) error {
		return fmt.Errorf("socket 路径太长（%d 字节，上限 %d 字节：%s），退到 %s 也不行：%w", len(preferred), Max, preferred, where, err)
	}
	fallback := FallbackDir()
	name := filepath.Base(preferred)
	n, ok := hashChars(len(fallback), len(name))
	if !ok {
		return "", tooLong(fallback, fmt.Errorf("退路目录加文件名放不下至少 %d 个字符的哈希目录", MinHashChars))
	}
	sum := sha256.Sum256([]byte(preferred))
	// 每个原路径一个子目录：同目录下的伴生文件（deckd 的单实例锁 deckd.lock）按目录区分不同的状态目录，文件名保持原样
	dir := filepath.Join(fallback, hex.EncodeToString(sum[:])[:n])
	if err := ensureDir(fallback); err != nil {
		return "", tooLong(fallback, err)
	}
	if err := ensureDir(dir); err != nil {
		return "", tooLong(dir, err)
	}
	if err := claimOrigin(dir, preferred); err != nil {
		return "", tooLong(dir, err)
	}
	short := filepath.Join(dir, name)
	if err := Check(short); err != nil {
		return "", err
	}
	return short, nil
}

// claimOrigin 保证 dir 的标记文件记的就是 origin：还没有就写下（先写临时文件再 link，别人看到的要么没有要么写全了）；
// 已有就核对一致，不一致说明这个目录属于另一条原始路径（哈希撞了，或目录被别人动过），不能共用。
func claimOrigin(dir, origin string) error {
	marker := filepath.Join(dir, OriginFile)
	same, err := originMatches(marker, origin)
	if errors.Is(err, os.ErrNotExist) {
		tmp, terr := os.CreateTemp(dir, ".origin-*")
		if terr != nil {
			return terr
		}
		defer os.Remove(tmp.Name())
		if _, terr = tmp.WriteString(origin); terr == nil {
			terr = tmp.Chmod(0o600)
		}
		if cerr := tmp.Close(); terr == nil {
			terr = cerr
		}
		if terr != nil {
			return terr
		}
		if lerr := os.Link(tmp.Name(), marker); lerr != nil && !os.IsExist(lerr) {
			return lerr
		}
		same, err = originMatches(marker, origin)
	}
	if err != nil {
		return err
	}
	if !same {
		return fmt.Errorf("%s 记的原始路径不是 %q：这个目录属于另一条路径，不共用", marker, origin)
	}
	return nil
}

// originMatches 报告标记文件的内容是不是正好等于 origin。标记必须是本用户的普通文件（不跟随软链）。
// 只读 len(origin)+1 个字节来比，文件里的内容不进返回值、不进错误（它是外部来的原文）。
func originMatches(marker, origin string) (bool, error) {
	fi, err := os.Lstat(marker)
	if err != nil {
		return false, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.Mode().IsRegular() || !ok || int(st.Uid) != os.Getuid() {
		return false, fmt.Errorf("%s 不是本用户的普通文件", marker)
	}
	f, err := os.OpenFile(marker, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false, err
	}
	defer f.Close()
	buf := make([]byte, len(origin)+1)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return false, err
	}
	return string(buf[:n]) == origin, nil
}

// ensureDir 保证 dir 是本用户的 0700 目录：不存在就建；存在就核对不是软链、是目录、属主是本用户，权限收紧到 0700。
func ensureDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	// Lstat：不跟随软链。/tmp 人人可写，别人可能事先放一个同名软链或同名目录
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s 不是目录（是软链或别的文件）", dir)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("读不到 %s 的属主", dir)
	}
	if int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s 的属主是 uid %d，不是本用户 %d", dir, st.Uid, os.Getuid())
	}
	if fi.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}
