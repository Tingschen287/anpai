// Package sockpath 管 Unix socket 路径的长度（n067）。
//
// sockaddr_un 的 sun_path 在 macOS 上只有 104 字节（含结尾的 NUL），Linux 是 108。路径一长，
// bind、connect 就报 invalid argument，看不出原因。deck 的 socket 路径来自 $XDG_STATE_HOME、$TMUX_TMPDIR 这类
// 用户可改的环境，隔离测试和沙箱里尤其容易超长。
//
// 做法：路径放得下就原样用；放不下就退到 /tmp/deck-<uid>/<原路径哈希>/<原文件名>——同一个原路径永远得到
// 同一个短路径，守护进程和客户端各自算都一致，文件名不变，同目录的伴生文件（deckd.lock）也跟着各得其所。
// 两层目录权限都是 0700，已存在时核对是真目录（不是软链）、属主是本用户。
// 退不了（目录被别人占着、不是目录、建不出来）就报清楚的错，不把超长路径交给 bind。
package sockpath

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// tmpRoot 是退路目录所在的位置；测试里换成别处，免得动真实的 /tmp/deck-<uid>。
var tmpRoot = "/tmp"

// FallbackDir 是退路目录：/tmp/deck-<uid>。
func FallbackDir() string { return filepath.Join(tmpRoot, fmt.Sprintf("deck-%d", os.Getuid())) }

// Check 报告路径是不是放得下；放不下给出清楚的错误。
func Check(path string) error {
	if n := len(path); n > Max {
		return fmt.Errorf("socket 路径太长（%d 字节，本系统上限 %d 字节）：%s", n, Max, path)
	}
	return nil
}

// Fit 返回一个放得下的 socket 路径：preferred 放得下就是它本身，否则是退路目录里的短路径。
func Fit(preferred string) (string, error) {
	if len(preferred) <= Max {
		return preferred, nil
	}
	sum := sha256.Sum256([]byte(preferred))
	// 每个原路径一个子目录：同目录下的伴生文件（deckd 的单实例锁 deckd.lock）按目录区分不同的状态目录，文件名保持原样
	dir := filepath.Join(FallbackDir(), hex.EncodeToString(sum[:5]))
	if err := ensureDir(FallbackDir()); err != nil {
		return "", fmt.Errorf("socket 路径太长（%d 字节，上限 %d 字节：%s），退到 %s 也不行：%w", len(preferred), Max, preferred, FallbackDir(), err)
	}
	if err := ensureDir(dir); err != nil {
		return "", fmt.Errorf("socket 路径太长（%d 字节，上限 %d 字节：%s），退到 %s 也不行：%w", len(preferred), Max, preferred, dir, err)
	}
	short := filepath.Join(dir, filepath.Base(preferred))
	if err := Check(short); err != nil {
		return "", err
	}
	return short, nil
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
