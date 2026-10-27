package agentdeck

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// n067 返工 3（Codex 复审 2 的 M1、M2、S2）。
//
// 本文件只用 c051f67 上就有的符号（procArgvChecked、procArgsOK、sysctlProcargs、withSysctl、withSelfCheck、
// CheckSessionArgs、nativeCmdline、selfCheckProcargs），所以能在旧代码上原样编译、先失败：
// 失败的是行为（读错误被当成「进程已退出」、自有英文诊断夹在中文里），不是编译。

// 对「进程已不在或无权读」的 errno：读不到，跳过那个进程。注意只有这三个。
var unreadableErrnos = []error{syscall.ESRCH, syscall.EINVAL, syscall.EPERM}

// 其余一切读错误：确认不了。ENOMEM（缓冲不足；SDK sysctl(3) 规定数据比缓冲大时复制能放下的部分并返回 ENOMEM）、EIO、
// EFAULT（不会由目标进程触发，但属于「不是读不到」）、不认识的错误。
var unconfirmableErrnos = []error{syscall.ENOMEM, syscall.EIO, syscall.EFAULT, errors.New("injected unknown failure")}

func truncatedProcargs() []byte {
	// argc=3 只给了 argv[0]
	full := realProcargs2(3, "/bin/x", []string{"x", "yy", "zzz"}, nil)
	return full[:strings.Index(string(full), "yy")]
}

// sysctlScript 按调用次序返回预设的结果；超出预设的次数 t.Fatal（防止无限重读）。
type sysctlStep struct {
	buf []byte
	err error
}

func scriptedSysctl(t *testing.T, steps ...sysctlStep) *int {
	t.Helper()
	calls := new(int)
	withSysctl(t, func(int) ([]byte, error) {
		if *calls >= len(steps) {
			t.Fatalf("重读次数超出预期：第 %d 次读（只预设了 %d 次）", *calls+1, len(steps))
		}
		s := steps[*calls]
		*calls++
		return s.buf, s.err
	})
	return calls
}

// M1：「进程已不在或无权读」才算读不到：读一次就跳过（不重读），既不是错误也不是 CLI 进程。
func TestSysctlUnreadableErrorsStillSkipTheProcess(t *testing.T) {
	withSelfCheck(t, func() error { return nil })
	for _, errno := range unreadableErrnos {
		calls := scriptedSysctl(t, sysctlStep{err: errno})
		argv, ok, err := procArgvChecked("", 4242)
		if err != nil || ok || argv != nil || *calls != 1 {
			t.Errorf("%v：应读一次就跳过：argv=%q ok=%v err=%v calls=%d", errno, argv, ok, err, *calls)
		}
		scriptedSysctl(t, sysctlStep{err: errno})
		if isCLI, err := procArgsOK("", 4242, LaunchArgs{Tool: "claude", Binary: "claude"}); isCLI || err != nil {
			t.Errorf("%v：procArgsOK 应放过：%v %v", errno, isCLI, err)
		}
	}
}

// M1：其他一切读错误（ENOMEM、EIO、…）归为确认不了：首次读到就标记，重读几次，最终仍拒绝；中途有一次完整读成功才通过。
func TestOtherSysctlErrorsAreUnconfirmable(t *testing.T) {
	withSelfCheck(t, func() error { return nil })
	ok1 := realProcargs2(1, "/bin/x", []string{"x"}, nil)
	for _, errno := range unconfirmableErrnos {
		t.Run(errno.Error(), func(t *testing.T) {
			// 三次都读不出：拒绝，且恰好读了 3 次（总共 3 次，不是 3 次之外再加）
			calls := scriptedSysctl(t, sysctlStep{err: errno}, sysctlStep{err: errno}, sysctlStep{err: errno})
			_, ok, err := procArgvChecked("", 4242)
			if ok || !errors.Is(err, errProcArgsUnconfirmable) || *calls != 3 {
				t.Errorf("应确认不了且读 3 次：ok=%v err=%v calls=%d", ok, err, *calls)
			}
			// 原始的系统错误还在错误链里（给人看原因、给测试分类）
			if !errors.Is(err, errno) {
				t.Errorf("错误链里应保留系统错误 %v：%v", errno, err)
			}
			// 中途一次完整读成功：通过
			calls = scriptedSysctl(t, sysctlStep{err: errno}, sysctlStep{buf: ok1})
			argv, ok, err := procArgvChecked("", 4242)
			if err != nil || !ok || len(argv) != 1 || *calls != 2 {
				t.Errorf("第二次读成功应通过：argv=%q ok=%v err=%v calls=%d", argv, ok, err, *calls)
			}
			// procArgsOK 也拒绝（不是「没有这个进程」）
			scriptedSysctl(t, sysctlStep{err: errno}, sysctlStep{err: errno}, sysctlStep{err: errno})
			if _, err := procArgsOK("", 4242, LaunchArgs{Tool: "claude", Binary: "claude"}); !errors.Is(err, errProcArgsUnconfirmable) {
				t.Errorf("procArgsOK 应按确认不了拒绝：%v", err)
			}
		})
	}
}

// M1：重读时一旦发现过截断（或解析不对、或上面那类读错误），之后再遇到任何非成功的结果——包括「读不到」那三种——都保持确认不了；
// 只有之后某次完整读成功才通过。
func TestUnconfirmableIsNotOverwrittenByLaterErrors(t *testing.T) {
	withSelfCheck(t, func() error { return nil })
	ok1 := realProcargs2(1, "/bin/x", []string{"x"}, nil)
	firsts := map[string]sysctlStep{
		"截断":      {buf: truncatedProcargs()},
		"太短":      {buf: []byte{1, 0}},
		"argc 越界": {buf: realProcargs2(-1, "/bin/x", []string{"x"}, nil)},
		"ENOMEM":  {err: syscall.ENOMEM},
		"EIO":     {err: syscall.EIO},
	}
	laters := append(append([]error{}, unreadableErrnos...), unconfirmableErrnos...)
	for name, first := range firsts {
		for _, later := range laters {
			label := fmt.Sprintf("%s 之后 %v", name, later)
			// 第 2 次是 later、第 3 次还是 later：拒绝
			calls := scriptedSysctl(t, first, sysctlStep{err: later}, sysctlStep{err: later})
			_, ok, err := procArgvChecked("", 4242)
			if ok || !errors.Is(err, errProcArgsUnconfirmable) || *calls != 3 {
				t.Errorf("%s：应保持确认不了：ok=%v err=%v calls=%d", label, ok, err, *calls)
			}
			// 第 2 次是 later，第 3 次完整读成功：通过（只有完整读成功才算）
			calls = scriptedSysctl(t, first, sysctlStep{err: later}, sysctlStep{buf: ok1})
			if argv, ok, err := procArgvChecked("", 4242); err != nil || !ok || len(argv) != 1 || *calls != 3 {
				t.Errorf("%s：之后完整读成功应通过：argv=%q ok=%v err=%v calls=%d", label, argv, ok, err, *calls)
			}
			// 经 procArgsOK：同样拒绝
			scriptedSysctl(t, first, sysctlStep{err: later}, sysctlStep{err: later})
			if _, err := procArgsOK("", 4242, LaunchArgs{Tool: "claude", Binary: "claude"}); !errors.Is(err, errProcArgsUnconfirmable) {
				t.Errorf("%s：procArgsOK 应拒绝：%v", label, err)
			}
		}
	}
}

// startSessionWithShellParent 起一个隔离的 tmux 会话：非 CLI 的父 bash 保持运行，下面有一个合法的 claude 命名脚本子进程。
// 返回 socket 和父 bash 的 pid。
func startSessionWithShellParent(t *testing.T) (sock string, shellPID int) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("没有 tmux")
	}
	// socket 路径在 macOS 上不能超过 104 字节：用 /tmp 下的短目录
	sockDir, err := os.MkdirTemp("/tmp", "n67f")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock = filepath.Join(sockDir, "s")
	run := func(a ...string) string {
		out, err := exec.Command("tmux", append([]string{"-S", sock, "-f", "/dev/null"}, a...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v：%v %s", a, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	t.Cleanup(func() { exec.Command("tmux", "-S", sock, "kill-server").Run() })
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/bash\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run("new-session", "-d", "-s", "s1", "bash", "-c", bin+" --model m & wait")
	if _, err := fmt.Sscan(run("display-message", "-p", "-t", "s1", "#{pane_pid}"), &shellPID); err != nil || shellPID <= 0 {
		t.Fatalf("取不到窗格 pid：%v", err)
	}
	oldWait := CLIWait
	CLIWait = time.Second
	t.Cleanup(func() { CLIWait = oldWait })
	time.Sleep(300 * time.Millisecond)
	// 基线：没有注入时通过
	if err := CheckSessionArgs(context.Background(), sock, "s1", LaunchArgs{Tool: "claude", Binary: "claude"}); err != nil {
		t.Fatalf("基线应通过：%v", err)
	}
	return sock, shellPID
}

// M1，端到端（Codex 的 TestReview3SessionRejectsSysctlErrors）：真实隔离 tmux 会话，只对「非 CLI 的父 shell」的读注入错误。
// 缓冲不足或内部读错误不能让 shell 被当成「已退出」而整个会话通过；确知退出或无权读（ESRCH、EINVAL、EPERM）才跳过。
func TestSessionRejectsSysctlErrors(t *testing.T) {
	withSelfCheck(t, func() error { return nil })
	sock, shell := startSessionWithShellParent(t)
	original := sysctlProcargs
	want := LaunchArgs{Tool: "claude", Binary: "claude"}
	inject := func(t *testing.T, firstBuf []byte, then error) *int {
		calls := new(int)
		withSysctl(t, func(p int) ([]byte, error) {
			if p != shell {
				return original(p)
			}
			*calls++
			if *calls == 1 && firstBuf != nil {
				return firstBuf, nil
			}
			return nil, then
		})
		return calls
	}
	for _, afterTruncation := range []bool{false, true} {
		var first []byte
		if afterTruncation {
			first = []byte{1, 0}
		}
		for _, errno := range []error{syscall.ENOMEM, syscall.EIO} {
			t.Run(fmt.Sprintf("拒绝/截断后=%v/%v", afterTruncation, errno), func(t *testing.T) {
				inject(t, first, errno)
				err := CheckSessionArgs(context.Background(), sock, "s1", want)
				if err == nil {
					t.Fatalf("父 shell 的读返回 %v（截断后=%v），整个会话却通过了", errno, afterTruncation)
				}
				if !errors.Is(err, errProcArgsUnconfirmable) {
					t.Errorf("应按确认不了拒绝：%v", err)
				}
			})
		}
	}
	// 截断之后再遇到「读不到」那三种：也保持拒绝（不能被后来的错误覆盖成跳过）
	for _, errno := range unreadableErrnos {
		t.Run(fmt.Sprintf("拒绝/截断后=%v", errno), func(t *testing.T) {
			inject(t, []byte{1, 0}, errno)
			if err := CheckSessionArgs(context.Background(), sock, "s1", want); !errors.Is(err, errProcArgsUnconfirmable) {
				t.Fatalf("截断后遇到 %v 应保持确认不了：%v", errno, err)
			}
		})
	}
	// 正常进程：首次读到 ESRCH、EINVAL、EPERM 仍跳过，会话通过
	for _, errno := range unreadableErrnos {
		t.Run(fmt.Sprintf("跳过/%v", errno), func(t *testing.T) {
			inject(t, nil, errno)
			if err := CheckSessionArgs(context.Background(), sock, "s1", want); err != nil {
				t.Fatalf("父 shell 读不到（%v）应跳过，会话通过：%v", errno, err)
			}
		})
	}
	// 截断一次、之后完整读成功：通过
	t.Run("截断后完整读成功", func(t *testing.T) {
		calls := 0
		withSysctl(t, func(p int) ([]byte, error) {
			if p != shell {
				return original(p)
			}
			calls++
			if calls == 1 {
				return []byte{1, 0}, nil
			}
			return original(p)
		})
		if err := CheckSessionArgs(context.Background(), sock, "s1", want); err != nil {
			t.Fatalf("重读成功应通过：%v", err)
		}
	})
}

// ---------------------------------------------------------------------------------------------------------------------
// M2：本项目自己产生的诊断走 n060 错误码：中文界面里没有本项目写的英文句子，英文界面里没有中文。

// 本项目在 c051f67 上自己写的英文句子的片段（现在必须只出现在英文界面里）。
var formerEnglishFragments = []string{
	"cannot be confirmed", "too short", "out of range", "has no end", "holds only", "truncated", "self check failed",
	"cannot read own", "parsed as", "but os.Args",
}

type procErrCase struct {
	name       string
	make       func(t *testing.T) error
	selfCheck  bool  // 属于运行时自检没过
	errnoInTxt error // 原样带出的系统错误
}

func procErrCases() []procErrCase {
	viaRead := func(buf []byte, err error) func(*testing.T) error {
		return func(t *testing.T) error {
			withSelfCheck(t, func() error { return nil })
			scriptedSysctlForever(t, buf, err)
			_, _, e := procArgvChecked("", 4242)
			return e
		}
	}
	viaSelf := func(own func(own int) ([]byte, error)) func(*testing.T) error {
		return func(t *testing.T) error {
			withSelfCheck(t, selfCheckProcargs)
			withSysctl(t, func(pid int) ([]byte, error) {
				if pid == os.Getpid() {
					return own(pid)
				}
				return nil, errors.New("不应读到别的进程")
			})
			_, e := nativeCmdline(4242)
			return e
		}
	}
	return []procErrCase{
		{name: "太短", make: viaRead([]byte{1, 0}, nil)},
		{name: "argc 越界", make: viaRead(realProcargs2(-1, "/bin/x", []string{"x"}, nil), nil)},
		{name: "路径无结尾", make: viaRead(append([]byte{1, 0, 0, 0}, []byte("/bin/x")...), nil)},
		{name: "只读到 n 个参数", make: viaRead(truncatedProcargs(), nil)},
		{name: "读 sysctl 失败", make: viaRead(nil, syscall.ENOMEM), errnoInTxt: syscall.ENOMEM},
		{name: "自检：读不了自己", make: viaSelf(func(int) ([]byte, error) { return nil, syscall.EPERM }), selfCheck: true, errnoInTxt: syscall.EPERM},
		{name: "自检：自己的数据解析不了", make: viaSelf(func(int) ([]byte, error) { return []byte{1, 0}, nil }), selfCheck: true},
		{name: "自检：和 os.Args 不一致", make: viaSelf(func(int) ([]byte, error) {
			return realProcargs2(2, "/bin/x", []string{"deck", "extra-arg"}, nil), nil
		}), selfCheck: true},
	}
}

func scriptedSysctlForever(t *testing.T, buf []byte, err error) {
	t.Helper()
	withSysctl(t, func(int) ([]byte, error) { return buf, err })
}

func ownerSees(err error) string {
	// 和真实路径一样：外层是 checkSessionOnce 的带码错误，owner 在界面上看到 deckclient.Show 的结果
	return deckclient.Show(i18n.E("err.agentdeck.checksessiononce.2", "synthetic", 4242, err))
}

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// 分类能力保留：errors.Is 认得出确认不了和自检没过。
func TestProcArgsErrorsKeepTheirClassification(t *testing.T) {
	for _, c := range procErrCases() {
		t.Run(c.name, func(t *testing.T) {
			err := c.make(t)
			if err == nil {
				t.Fatal("应报错")
			}
			if !errors.Is(err, errProcArgsUnconfirmable) {
				t.Errorf("应是确认不了：%v", err)
			}
			if got := errors.Is(err, errProcArgsSelfCheck); got != c.selfCheck {
				t.Errorf("自检分类：得到 %v，期望 %v（%v）", got, c.selfCheck, err)
			}
			if c.errnoInTxt != nil && !errors.Is(err, c.errnoInTxt) {
				t.Errorf("系统错误 %v 应留在错误链里：%v", c.errnoInTxt, err)
			}
			if i18n.AsCoded(err) == nil {
				t.Errorf("应带 n060 错误码：%v", err)
			}
			// 外层包一层 checkSessionOnce 的带码错误后，分类仍在（CheckSessionArgs、NotifyDaemonRunning 都靠它）
			outer := i18n.E("err.agentdeck.checksessiononce.2", "synthetic", 4242, err)
			if !errors.Is(outer, errProcArgsUnconfirmable) || errors.Is(outer, errProcArgsSelfCheck) != c.selfCheck {
				t.Errorf("外层包装后分类丢了：%v", outer)
			}
		})
	}
}

// Codex 的 TestReview3ChineseError：中文界面里，整条错误没有本项目自己写的英文句子。
func TestChineseErrorHasNoProjectEnglish(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	for _, c := range procErrCases() {
		t.Run(c.name, func(t *testing.T) {
			err := c.make(t)
			if err == nil {
				t.Fatal("应报错")
			}
			for _, txt := range []string{ownerSees(err), i18n.E("err.agentdeck.checksessiononce.2", "synthetic", 4242, err).Error()} {
				t.Logf("owner 看到（中文）：%s", txt)
				if !hasHan(txt) {
					t.Errorf("中文界面里应是中文：%q", txt)
				}
				for _, frag := range formerEnglishFragments {
					if strings.Contains(txt, frag) {
						t.Errorf("中文界面夹着本项目自己的英文 %q：%s", frag, txt)
					}
				}
				// 系统 errno 文本是外部原文，可以原样带出（并且要真的带出，不能丢）
				if c.errnoInTxt != nil && !strings.Contains(txt, c.errnoInTxt.Error()) {
					t.Errorf("系统错误文本 %q 应原样带出：%s", c.errnoInTxt, txt)
				}
			}
		})
	}
}

// 英文界面里，整条错误没有中文（外层和内层都翻译了），而且内层真的是译文。
func TestEnglishErrorHasNoChinese(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	for _, c := range procErrCases() {
		t.Run(c.name, func(t *testing.T) {
			i18n.SetLang(i18n.ZH)
			err := c.make(t)
			if err == nil {
				t.Fatal("应报错")
			}
			i18n.SetLang(i18n.EN)
			txt := ownerSees(err)
			t.Logf("owner 看到（英文）：%s", txt)
			if hasHan(txt) {
				t.Errorf("英文界面里不该有中文：%s", txt)
			}
			if !strings.Contains(txt, "cannot be confirmed") {
				t.Errorf("英文界面应有译文「cannot be confirmed」：%s", txt)
			}
			if c.selfCheck && !regexp.MustCompile(`(?i)self.?check`).MatchString(txt) {
				t.Errorf("自检失败的英文说明里应提到自检：%s", txt)
			}
			if c.errnoInTxt != nil && !strings.Contains(txt, c.errnoInTxt.Error()) {
				t.Errorf("系统错误文本 %q 应原样带出：%s", c.errnoInTxt, txt)
			}
		})
	}
}
