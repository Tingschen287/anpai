package agentdeck

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// n067 返工 2（owner 选方案 A）：截断与不足一律报错；保证收窄到与 Linux 一致。
// 本文件只用 17ce071 上就有的函数（parseProcargs2、nativeCmdline、procArgsOK），所以能在旧代码上原样编译、先失败。

// Codex 复审 1 的截断反例（R2-1）：argc=3，参数 x、yy、zzz，在 yy 之前（两个参数之间）截断。以前返回 "x\x00" 和 nil。
func TestParseProcargs2TruncatedAtArgumentBoundaryIsAnError(t *testing.T) {
	full := realProcargs2(3, "/bin/x", []string{"x", "yy", "zzz"}, nil)
	cut := bytes.Index(full, []byte("yy"))
	if got, err := parseProcargs2(full[:cut]); err == nil {
		t.Fatalf("argc=3 但只剩 argv[0]，应报错：%q", got)
	}
}

// 在每一个位置切断缓冲区：只要 argc 个参数没有读满，一律报错；读满了（后面只剩环境变量或被切掉）才成功，且结果恰好是那 argc 个参数。
func TestParseProcargs2NeverReturnsAPartialArgv(t *testing.T) {
	for _, args := range [][]string{{"x", "yy", "zzz"}, {"", "yy", "zzz"}, {"", "", "zzz"}, {"x", "", ""}, {"", "", ""}} {
		full := realProcargs2(int32(len(args)), "/bin/x", args, []string{"SYNTHETIC_SECRET=review-only"})
		want := joinNUL(args...)
		// 最后一个参数的结尾 NUL 之后的偏移：到这里为止参数才算读满
		argsEnd := bytes.Index(full, []byte("SYNTHETIC_SECRET"))
		for cut := 0; cut <= len(full); cut++ {
			got, err := parseProcargs2(full[:cut])
			if cut < argsEnd {
				if err == nil {
					t.Errorf("参数 %q 在 %d 处截断（参数读满要到 %d）：应报错，却得到 %q", args, cut, argsEnd, got)
				}
				continue
			}
			if err != nil || string(got) != want {
				t.Errorf("参数 %q 在 %d 处（已读满）：得到 %q err=%v，期望 %q", args, cut, got, err, want)
			}
		}
	}
}

// argc 比实际参数多、后面什么都没有：报错（旧的 TestParseProcargs2「argc 比实际参数多时只取到实际有的」把这个缺陷固化成了成功期望）。
func TestParseProcargs2ArgcLargerThanArgumentsWithNothingAfterIsAnError(t *testing.T) {
	if got, err := parseProcargs2(procargs2(5, "/bin/x", 1, []string{"x", "y"}, nil)); err == nil {
		t.Fatalf("argc=5 但只有 2 个参数，应报错：%q", got)
	}
}

// 收窄后的保证：argc 比实际参数多、后面还有环境变量时，字节上分不出参数和环境变量（和 Linux 一样）。
// 能保证的只有两条：不多读（恰好 argc 个串，不会整段读进环境变量区）；这种凭据外泄的路径不在威胁模型里（只会多出不认识的参数，白名单拒绝）。
func TestParseProcargs2ArgcLargerThanArgumentsReadsExactlyArgcStrings(t *testing.T) {
	got, err := parseProcargs2(realProcargs2(2, "/bin/x", []string{"x"}, []string{"SYNTHETIC_SECRET=review-only", "B=c", "D=e"}))
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(got, []byte{0}); n != 2 || bytes.Contains(got, []byte("B=c")) {
		t.Fatalf("应恰好读 argc=2 个串：%q", got)
	}
}

// 真实子进程改写自己参数区末尾的 NUL（Codex 复审 1 的 native-mutated 反例，改写成测试）。
// 结论按方案 A 收窄：环境变量可能出现在最后一个参数里，这与 Linux /proc/<pid>/cmdline（内核 get_mm_cmdline）一样；
// 但被连成一串的绕过参数仍被 procArgsOK（非 CLI 进程那条路径）拦住。
func TestN067MutateHelper(t *testing.T) {
	if os.Getenv("N067_MUTATE") != "1" {
		return
	}
	a := os.Args[len(os.Args)-1]
	unsafe.Slice(unsafe.StringData(a), len(a)+1)[len(a)] = 'X' // 把最后一个参数的结尾 NUL 改成 X
	os.WriteFile(os.Getenv("N067_READY"), []byte("ready"), 0o600)
	time.Sleep(30 * time.Second)
}

func TestNativeCmdlineMutatedTerminatorMayJoinEnvironmentButGluedBypassIsStillCaught(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	const bypass = "--dangerously-skip-permissions"
	cmd := exec.Command(os.Args[0], "-test.run=^TestN067MutateHelper$", "--", bypass)
	cmd.Env = []string{"SYNTHETIC_SECRET=review-only", "N067_MUTATE=1", "N067_READY=" + ready}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	for i := 0; i < 500; i++ {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// 返工 3（S2）：ready 必须出现。以前这里不断言，子进程没起来时下面的分支都不执行，测试照样绿。
	if _, err := os.Stat(ready); err != nil {
		t.Fatalf("改写参数区的子进程没有就绪（ready 文件没出现），这个测试没有测到东西：%v", err)
	}
	want := LaunchArgs{Tool: "claude", Binary: "claude"}
	pid := cmd.Process.Pid
	got, err := nativeCmdline(pid)
	_, procErr := procArgsOK("", pid, want)
	if err != nil {
		// 读不出来。返工 3（S2、M1）：区分「确认不了」和「普通读不到」，经 procArgsOK 的最终结果都要符合 M1 的规则：
		//   - 确认不了（errProcArgsUnconfirmable）：procArgsOK 也报确认不了，整体拒绝（fail closed）；
		//   - 普通读不到（只有 ESRCH、EINVAL、EPERM）：procArgsOK 跳过这个进程，不当错误；
		//   - 其他读错误：同 UID 的子进程不该出现；若出现，nativeCmdline 必须已把它归为确认不了（上一条），走到这里就是缺陷。
		switch {
		case errors.Is(err, errProcArgsUnconfirmable):
			t.Logf("改写后读命令行确认不了（fail closed，可接受）：%v", err)
			if !errors.Is(procErr, errProcArgsUnconfirmable) {
				t.Fatalf("nativeCmdline 报确认不了（%v），procArgsOK 却是：%v", err, procErr)
			}
		case errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.EPERM):
			t.Logf("改写后读命令行读不到（%v）：procArgsOK 应跳过", err)
			if procErr != nil {
				t.Fatalf("nativeCmdline 读不到（%v）应跳过，procArgsOK 却报错：%v", err, procErr)
			}
		default:
			t.Fatalf("读错误既不是确认不了、也不是读不到（M1 要求其他一切读错误都归为确认不了）：%v", err)
		}
		return
	}
	argv := strings.Split(strings.TrimRight(string(got), "\x00"), "\x00")
	last := argv[len(argv)-1]
	leaked := strings.Contains(string(got), "SYNTHETIC_SECRET")
	t.Logf("改写后最后一个参数：%q（环境变量连进来=%v）", last, leaked)
	if leaked {
		// 方案 A 的限制：环境变量只会出现在最后一个参数里（连串），不会成为独立的参数
		if !strings.HasPrefix(last, bypass) || !strings.Contains(last, "SYNTHETIC_SECRET=review-only") {
			t.Fatalf("环境变量应只连在最后一个参数里：%q", argv)
		}
		for _, a := range argv[:len(argv)-1] {
			if strings.Contains(a, "SYNTHETIC_SECRET") {
				t.Fatalf("环境变量成了独立的参数：%q", argv)
			}
		}
		// 连串里的绕过参数仍被非 CLI 进程那条路径认出来（连串在最后一个元素里，返工 3 的规则也认）
		if procErr == nil {
			t.Fatalf("被连成一串的绕过参数应被拦住：%q", last)
		}
	}
}
