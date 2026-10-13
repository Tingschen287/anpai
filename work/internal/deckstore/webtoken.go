package deckstore

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// n055：deck 自管的 agent-deck headless 服务用的 token。上游 agent-deck web 只接受 --token（会出现在进程列表里）
// 或 --token-file（要求 0600），所以写成文件：放在 deck 的状态目录（0700）里，文件只给本用户读写（0600），
// 只交给上游与 deckd，不打印、不写日志。

// WebTokenFile 是 token 文件的位置。
func WebTokenFile(d Dirs) string { return filepath.Join(d.State, "agentdeck-web.token") }

// EnsureWebToken 返回 token 文件路径与内容：文件不在就生成一个（32 字节随机数的十六进制）。
func EnsureWebToken(d Dirs) (path, token string, err error) {
	path = WebTokenFile(d)
	if err := os.MkdirAll(d.State, 0o700); err != nil {
		return "", "", err
	}
	token, err = ReadWebToken(path)
	if err == nil {
		return path, token, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	token = hex.EncodeToString(b)
	// O_EXCL：两个 deck up 同时生成时只有一个写成，另一个读它写好的
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		token, err = ReadWebToken(path)
		return path, token, err
	}
	if err != nil {
		return "", "", err
	}
	if _, err := f.WriteString(token + "\n"); err != nil {
		f.Close()
		os.Remove(path)
		return "", "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", "", err
	}
	return path, token, nil
}

// ReadWebToken 读 token 文件：必须是本用户的普通文件、权限不宽于 0600、内容非空一行。
func ReadWebToken(path string) (string, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("%s 不是普通文件，不用它当 token", path)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s 的权限是 %o，别的用户也能读：请改成 600（chmod 600）后再启动", path, st.Mode().Perm())
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) != os.Getuid() {
		return "", fmt.Errorf("%s 不属于当前用户，不用它当 token", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" || strings.ContainsAny(tok, " \t\r\n") || len(tok) > 512 {
		return "", fmt.Errorf("%s 的内容不是一个 token（要一行、不含空白）：删掉它再 deck up 会重新生成", path)
	}
	return tok, nil
}
