package agentdeck

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func procargs2(argc int32, execPath string, pad int, args []string, env []string) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, argc)
	b.WriteString(execPath)
	b.WriteByte(0)
	b.Write(make([]byte, pad))
	for _, a := range args {
		b.WriteString(a)
		b.WriteByte(0)
	}
	for _, e := range env {
		b.WriteString(e)
		b.WriteByte(0)
	}
	return b.Bytes()
}

// kern.procargs2 的格式：argc、可执行文件路径、NUL 填充、argc 个参数、环境变量。只取 argc 个参数，环境变量区不会被整段读进来（返工 2 把保证收窄到与 Linux 一致：目标进程改写自己参数区末尾的 NUL 时，最后一个参数可能连上环境变量，见 parseProcargs2 的注释与 procs_fix2_darwin_test.go）。读不足 argc 个参数是错误。
func TestParseProcargs2(t *testing.T) {
	cases := []struct {
		name string
		buf  []byte
		want string
		ok   bool
	}{
		{"常规", procargs2(3, "/usr/local/bin/agent-deck", 3, []string{"agent-deck", "notify-daemon", "--x y"}, []string{"SECRET=abc", "A=b"}), "agent-deck\x00notify-daemon\x00--x y\x00", true},
		{"空参数在中间", procargs2(3, "/bin/x", 1, []string{"x", "", "z"}, []string{"SECRET=abc"}), "x\x00\x00z\x00", true},
		// 返工 2（方案 A）：读不足 argc 个参数是错误，不再「只取到实际有的」（Codex 复审 1 R2-1：旧期望把截断缺陷固化成了成功）
		{"argc 比实际参数多时报错（读不足 argc 个）", procargs2(5, "/bin/x", 1, []string{"x", "y"}, nil), "", false},
		{"没有环境变量", procargs2(1, "/bin/x", 0, []string{"x"}, nil), "x\x00", true},
		{"太短", []byte{1, 0}, "", false},
		{"路径没有结尾", append(binary.LittleEndian.AppendUint32(nil, 1), []byte("/bin/x")...), "", false},
	}
	for _, c := range cases {
		got, err := parseProcargs2(c.buf)
		if (err == nil) != c.ok || (c.ok && string(got) != c.want) {
			t.Errorf("%s：得到 %q err=%v，期望 %q ok=%v", c.name, got, err, c.want, c.ok)
		}
		if bytes.Contains(got, []byte("SECRET")) {
			t.Errorf("%s：环境变量被带出来了：%q", c.name, got)
		}
	}
}

// realProcargs2 按真实内核的版式造 kern.procargs2：argc、可执行文件路径、NUL 填充到字符串区 8 字节对齐
// （第一个参数的偏移 ≡ 4 mod 8）、argc 个参数（可以是空串）、环境变量、苹果自己追加的几个串。
func realProcargs2(argc int32, execPath string, args []string, env []string) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, argc)
	b.WriteString(execPath)
	b.WriteByte(0)
	for b.Len()%procargsAlign != procargsBase {
		b.WriteByte(0)
	}
	for _, a := range args {
		b.WriteString(a)
		b.WriteByte(0)
	}
	for _, e := range env {
		b.WriteString(e)
		b.WriteByte(0)
	}
	for _, a := range []string{"executable_path=" + execPath, "ptr_munge=", "main_stack=0x0"} {
		b.WriteString(a)
		b.WriteByte(0)
	}
	return b.Bytes()
}

func joinNUL(args ...string) string { return strings.Join(args, "\x00") + "\x00" }

// 审查 1 的合成反例（原样保留）：空 argv[0] 不能让环境变量变成参数。这里的填充长度是任意的（2），不是真实内核的对齐，
// 所以只断言环境变量没有进来。
func TestParseProcargs2EmptyArgv0NeverLeaksEnvironment(t *testing.T) {
	got, err := parseProcargs2(procargs2(2, "/bin/x", 2, []string{"", "x"}, []string{"SYNTHETIC_SECRET=review-only"}))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte("SYNTHETIC_SECRET=")) {
		t.Fatalf("empty argv[0] causes environment to escape argv boundary: %q", got)
	}
}

// 真实版式（进程没改写过自己的参数区）：空参数（在开头、连着几个、在中间、全是空的）原样保留，环境变量和苹果追加的串不进来。
// 可执行文件路径从 0 到 17 个字符，把 8 种对齐填充（0～7 个 NUL）各走几遍。
func TestParseProcargs2KeepsEmptyArgumentsAndNeverReadsTheEnvironment(t *testing.T) {
	env := []string{"SYNTHETIC_SECRET=review-only", "A=b"}
	shapes := [][]string{
		{"agent-deck", "notify-daemon", "--x y"},
		{"", "x"},
		{"", "", "x"},
		{"", "", "", "x", "y"},
		{"x", "", "z"},
		{"x", ""},
		{""},
		{"", ""},
	}
	for pathLen := 0; pathLen <= 17; pathLen++ {
		path := "/" + strings.Repeat("p", pathLen)
		for _, args := range shapes {
			buf := realProcargs2(int32(len(args)), path, args, env)
			got, err := parseProcargs2(buf)
			if err != nil {
				t.Errorf("路径 %d 字符 参数 %q：%v", pathLen, args, err)
				continue
			}
			if want := joinNUL(args...); string(got) != want {
				t.Errorf("路径 %d 字符 参数 %q：得到 %q，期望 %q", pathLen, args, got, want)
			}
			if bytes.Contains(got, []byte("SYNTHETIC_SECRET")) || bytes.Contains(got, []byte("executable_path")) {
				t.Errorf("路径 %d 字符 参数 %q：环境变量进来了：%q", pathLen, args, got)
			}
		}
	}
}

// argc 与实际字段对不上、缓冲区被截断、argc 不合理：都不能把环境变量当参数，也不能越界。
func TestParseProcargs2BoundsAndTruncation(t *testing.T) {
	env := []string{"SYNTHETIC_SECRET=review-only"}
	// argc 比实际参数少：只取 argc 个，多出来的参数（和环境变量）不要
	if got, err := parseProcargs2(realProcargs2(1, "/bin/x", []string{"x", "y"}, env)); err != nil || string(got) != "x\x00" {
		t.Errorf("argc 比实际参数少：%q %v", got, err)
	}
	// argc 为 0：没有参数，环境变量也不要
	if got, err := parseProcargs2(realProcargs2(0, "/bin/x", nil, env)); err != nil || len(got) != 0 {
		t.Errorf("argc 为 0：%q %v", got, err)
	}
	// 不合理的 argc：拒绝，不去猜
	for _, argc := range []int32{-1, -1 << 31, maxProcArgc + 1, 1 << 30} {
		if got, err := parseProcargs2(realProcargs2(argc, "/bin/x", []string{"x"}, env)); err == nil {
			t.Errorf("argc %d 应拒绝：%q", argc, got)
		}
	}
	// 截断：在每一个位置切断缓冲区，结果要么报错，要么是参数的一个前缀；任何时候都不含环境变量
	full := realProcargs2(3, "/bin/x", []string{"", "yy", "zzz"}, env)
	wantAll := joinNUL("", "yy", "zzz")
	for cut := 0; cut <= len(full); cut++ {
		got, err := parseProcargs2(full[:cut])
		if err != nil {
			continue
		}
		if !strings.HasPrefix(wantAll, string(got)) {
			t.Fatalf("在 %d 处截断：得到 %q，不是参数 %q 的前缀", cut, got, wantAll)
		}
	}
	// 参数区在最后一个参数中间被截断（没有结尾的 NUL）：报错，不把半个参数交出去
	cut := bytes.Index(full, []byte("zzz")) + 2
	if got, err := parseProcargs2(full[:cut]); err == nil {
		t.Errorf("最后一个参数被截断应报错：%q", got)
	}
	// 整个缓冲区都是 NUL 与空参数：不会越界
	if got, err := parseProcargs2(append(binary.LittleEndian.AppendUint32(nil, 5), make([]byte, 40)...)); err != nil || len(got) > 5 {
		t.Errorf("全是 NUL：%q %v", got, err)
	}
}

// startArgvHelper 起一个命令行完全由调用方决定的进程（测试二进制的副本，睡 60 秒），环境变量只给两项人造的。
func startArgvHelper(t *testing.T, argv []string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestProcHelperSleep$")
	cmd.Args = argv
	cmd.Env = []string{"N067_PROC_HELPER=1", "SYNTHETIC_SECRET=review-only"}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd
}

func waitCmdline(t *testing.T, pid int) []byte {
	t.Helper()
	var got []byte
	for i := 0; i < 200; i++ {
		var err error
		if got, err = nativeCmdline(pid); err == nil && len(got) > 0 {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("读不到进程 %d 的命令行", pid)
	return nil
}

// 真实子进程：argv[0] 为空、前几个参数为空、空参数在中间——命令行原样读出，环境变量（含人造的 SYNTHETIC_SECRET）不出来（进程没改写过自己的参数区时）。
func TestNativeCmdlineKeepsEmptyArgumentsOfARealProcess(t *testing.T) {
	for name, tail := range map[string][]string{
		"空 argv0":          {"-test.run=^TestProcHelperSleep$", "--", "review-marker"},
		"空 argv0 和空 argv1": {"", "-test.run=^TestProcHelperSleep$", "--", "review-marker"},
		"空参数在中间":           {"-test.run=^TestProcHelperSleep$", "--", "", "review-marker"},
	} {
		argv := append([]string{""}, tail...)
		if name == "空参数在中间" {
			argv = append([]string{"helper"}, tail...)
		}
		cmd := startArgvHelper(t, argv)
		got := waitCmdline(t, cmd.Process.Pid)
		if want := joinNUL(argv...); string(got) != want {
			t.Errorf("%s：得到 %q，期望 %q", name, got, want)
		}
		if bytes.Contains(got, []byte("SYNTHETIC_SECRET")) || bytes.Contains(got, []byte("N067_PROC_HELPER")) {
			t.Errorf("%s：环境变量进了命令行：%q", name, got)
		}
	}
}

// 真实子进程、不同长度的可执行文件路径（把 8 种对齐填充都走一遍）：空 argv[0] 都读得对。
func TestNativeCmdlineEmptyArgv0AtEveryPaddingLength(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("没有 sleep")
	}
	data, err := os.ReadFile(sleep)
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	for n := 0; n < 10; n++ {
		p := filepath.Join(dir, "s"+strings.Repeat("x", n))
		if err := os.WriteFile(p, data, 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(p, "30")
		cmd.Args = []string{"", "30"}
		cmd.Env = []string{"SYNTHETIC_SECRET=review-only", "A=b"}
		if err := cmd.Start(); err != nil {
			t.Skipf("起不了复制的 sleep：%v", err)
		}
		got := waitCmdline(t, cmd.Process.Pid)
		cmd.Process.Kill()
		cmd.Wait()
		if string(got) != "\x0030\x00" {
			t.Errorf("路径 %d 字节：得到 %q，期望 %q", len(p), got, "\x0030\x00")
		}
	}
}
