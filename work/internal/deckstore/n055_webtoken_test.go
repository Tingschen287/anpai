package deckstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// n055：自管 headless 服务的 token 文件：没有就生成（0600、0700 目录里），有就复用；权限过宽、软链、空内容一律不用。
func TestEnsureWebToken(t *testing.T) {
	d := testDirs(t)
	path, tok, err := EnsureWebToken(d)
	if err != nil || len(tok) != 64 || path != filepath.Join(d.State, "agentdeck-web.token") {
		t.Fatalf("生成：%s %d %v", path, len(tok), err)
	}
	st, _ := os.Stat(path)
	dst, _ := os.Stat(d.State)
	if st.Mode().Perm() != 0o600 || dst.Mode().Perm() != 0o700 {
		t.Fatalf("权限：文件 %o 目录 %o", st.Mode().Perm(), dst.Mode().Perm())
	}
	_, again, err := EnsureWebToken(d)
	if err != nil || again != tok {
		t.Fatal("第二次复用同一个 token")
	}
	os.Chmod(path, 0o644)
	if _, _, err := EnsureWebToken(d); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("权限过宽要拒绝并说明：%v", err)
	}
	os.Chmod(path, 0o600)
	os.WriteFile(path, []byte("  \n"), 0o600)
	if _, err := ReadWebToken(path); err == nil {
		t.Fatal("空内容不用")
	}
	other := filepath.Join(t.TempDir(), "tok")
	os.WriteFile(other, []byte("abc"), 0o600)
	os.Remove(path)
	os.Symlink(other, path)
	if _, err := ReadWebToken(path); err == nil || !strings.Contains(err.Error(), "不是普通文件") {
		t.Fatalf("软链不用：%v", err)
	}
}
