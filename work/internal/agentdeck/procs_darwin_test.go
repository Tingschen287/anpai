package agentdeck

import (
	"bytes"
	"encoding/binary"
	"testing"
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

// kern.procargs2 的格式：argc、可执行文件路径、NUL 填充、argc 个参数、环境变量。只取参数，环境变量（可能含凭据）一个字节都不带出来。
func TestParseProcargs2(t *testing.T) {
	cases := []struct {
		name string
		buf  []byte
		want string
		ok   bool
	}{
		{"常规", procargs2(3, "/usr/local/bin/agent-deck", 3, []string{"agent-deck", "notify-daemon", "--x y"}, []string{"SECRET=abc", "A=b"}), "agent-deck\x00notify-daemon\x00--x y\x00", true},
		{"空参数在中间", procargs2(3, "/bin/x", 1, []string{"x", "", "z"}, []string{"SECRET=abc"}), "x\x00\x00z\x00", true},
		{"argc 比实际参数多时只取到实际有的", procargs2(5, "/bin/x", 1, []string{"x", "y"}, nil), "x\x00y\x00", true},
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
