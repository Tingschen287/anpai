package agentdeck

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// n055 评审 1 B1：探测 headless 服务时要带 token（GET /api/menu），发出去之前得先知道对面是谁——否则端口被别的程序或别的用户
// 占着时，token 在第一次探测里就交出去了。这里读 /proc/net/tcp{,6} 找监听这个端口的套接字，再在本用户的进程里找持有它的进程：
// 只有「本用户的 agent-deck 进程」才算自己的服务。读不到、找不到持有者一律按不是自己的处理，不带 token。

// ErrForeignListener 表示端口上在听的不是本用户的 agent-deck（或确认不了）。
var ErrForeignListener = errors.New("端口上在听的不是本用户的 agent-deck")

// CheckWebListener 说明 addr（回环地址:端口）上在听的是谁：没人听返回 ErrWebDown；本用户的 agent-deck 返回 nil；
// 其余返回包着 ErrForeignListener 的说明。
func CheckWebListener(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	p, err := strconv.Atoi(port)
	if err != nil || p <= 0 || p > 65535 {
		return fmt.Errorf("端口 %q 不对", port)
	}
	inodes, uids, err := listeners(p)
	if err != nil {
		return fmt.Errorf("%w：读不了监听表（%v）", ErrForeignListener, err)
	}
	if len(inodes) == 0 {
		return ErrWebDown
	}
	me := os.Getuid()
	for i, ino := range inodes {
		if uids[i] != me {
			return fmt.Errorf("%w：端口 %d 被用户 %d 的进程占着", ErrForeignListener, p, uids[i])
		}
		exe, ok := ownerExe(ino)
		if !ok {
			return fmt.Errorf("%w：找不到持有端口 %d 的进程", ErrForeignListener, p)
		}
		if filepath.Base(exe) != "agent-deck" {
			return fmt.Errorf("%w：端口 %d 被 %s 占着", ErrForeignListener, p, filepath.Base(exe))
		}
	}
	return nil
}

// listeners 返回在 port 上处于 LISTEN 的套接字 inode 与属主 uid（IPv4 与 IPv6）。
func listeners(port int) (inodes []string, uids []int, err error) {
	want := fmt.Sprintf(":%04X", port)
	read := 0
	for _, name := range []string{"net/tcp", "net/tcp6"} {
		f, err := os.Open(filepath.Join(procRoot, name))
		if err != nil {
			continue
		}
		read++
		sc := bufio.NewScanner(f)
		sc.Scan() // 表头
		for sc.Scan() {
			fs := strings.Fields(sc.Text())
			if len(fs) < 10 || fs[3] != "0A" || !strings.HasSuffix(fs[1], want) {
				continue
			}
			uid, err := strconv.Atoi(fs[7])
			if err != nil {
				continue
			}
			inodes, uids = append(inodes, fs[9]), append(uids, uid)
		}
		f.Close()
	}
	if read == 0 {
		return nil, nil, errors.New("没有 /proc/net/tcp")
	}
	return inodes, uids, nil
}

// ownerExe 在看得到的进程里找持有 socket:[inode] 的那个，返回它的可执行文件。
func ownerExe(inode string) (string, bool) {
	target := "socket:[" + inode + "]"
	pids, _ := filepath.Glob(filepath.Join(procRoot, "[0-9]*"))
	for _, pd := range pids {
		fds, err := os.ReadDir(filepath.Join(pd, "fd"))
		if err != nil {
			continue
		}
		for _, fd := range fds {
			if l, err := os.Readlink(filepath.Join(pd, "fd", fd.Name())); err == nil && l == target {
				exe, err := os.Readlink(filepath.Join(pd, "exe"))
				return exe, err == nil
			}
		}
	}
	return "", false
}
