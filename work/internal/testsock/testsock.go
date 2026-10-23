// Package testsock 给测试一个短目录放 Unix socket（n067）。
//
// t.TempDir() 在 macOS 上是 /var/folders/xx/.../T/TestName123/001，加上文件名常常超过 sun_path 的 104 字节，
// bind 报 invalid argument。要在测试里监听 Unix socket（或把 socket 路径交给 tmux -S）的，都用这里的目录。
package testsock

import (
	"os"
	"testing"
)

// Dir 在 /tmp 下建一个很短的目录（/tmp/dk123456789），测试结束删掉。
func Dir(t testing.TB) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "dk")
	if err != nil {
		t.Fatalf("建短目录失败：%v", err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

// Path 是短目录里一个名字的完整路径，每次调用建一个新目录，互不干扰。
func Path(t testing.TB, name string) string {
	t.Helper()
	return Dir(t) + "/" + name
}
