// Package writerlock 保证同一个 agent-deck profile 同一时刻只有一个写入者（n050，架构稿 §3）。
//
// deckd 与旧 Work Deck（work）各自有一份 lease、binding、queue。它们若同时往同一个 profile 的会话里派工、发话、
// 接管，就是两套互不知情的仲裁管同一个会话。进程扫描做不到互斥（检查之后对方照样能启动），所以两边在开放写入口之前
// 都先取同一把生命周期锁：$XDG_STATE_HOME/agent-deck-writers/<profile>.lock，flock，进程退出才释放。
// 取不到的一方只读，并说清楚是谁占着。
package writerlock

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// Holder 是锁文件里记下的持有者，给取不到锁的一方说明是谁占着。
type Holder struct {
	Program string    `json:"program"`
	PID     int       `json:"pid"`
	Since   time.Time `json:"since"`
}

// Lock 是取到的写入者锁。进程退出或调 Release 才释放。
type Lock struct {
	f    *os.File
	Path string
}

// ErrHeld 表示锁被别人占着。
type ErrHeld struct {
	Profile string
	Path    string
	By      *Holder // 读不出来时为 nil
}

func (e *ErrHeld) Error() string { return e.I18n().Error() }

// I18n 给英文界面用。持有者是嵌套码，中文 Error() 和以前逐字一样。
func (e *ErrHeld) I18n() *i18n.Error {
	who := i18n.E("err.writerlock.whounknown")
	if e.By != nil {
		who = i18n.E("err.writerlock.who", e.By.Program, e.By.PID, e.By.Since.Format("2006-01-02 15:04"))
	}
	return i18n.E("err.writerlock.held", e.Profile, who, e.Path)
}

var safeProfile = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// Dir 是锁所在目录：$XDG_STATE_HOME/agent-deck-writers，未设置时用 ~/.local/state/agent-deck-writers。
func Dir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "agent-deck-writers"), nil
}

// Acquire 为 profile 取写入者锁，不阻塞。program 是写进锁文件的程序名（deckd 或 work）。
func Acquire(profile, program string) (*Lock, error) {
	if !safeProfile.MatchString(profile) {
		return nil, i18n.E("err.writerlock.badprofile", profile)
	}
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, profile+".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, i18n.E("err.writerlock.open", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, &ErrHeld{Profile: profile, Path: path, By: readHolder(path)}
	}
	h := Holder{Program: program, PID: os.Getpid(), Since: time.Now()}
	data, _ := json.Marshal(h)
	if err := f.Truncate(0); err == nil {
		f.WriteAt(data, 0)
	}
	return &Lock{f: f, Path: path}, nil
}

// Release 释放锁。
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	l.f.Truncate(0)
	syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	l.f.Close()
	l.f = nil
}

func readHolder(path string) *Holder {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil
	}
	var h Holder
	if json.Unmarshal(data, &h) != nil || h.Program == "" {
		return nil
	}
	return &h
}
