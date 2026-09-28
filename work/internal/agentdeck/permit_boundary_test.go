package agentdeck

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/lease"
)

// 往会话里写字只有一条路，而且那条路先核许可。
//
// 查三件事：
//  1. 包内调 exec / execWithStdin 且前两个参数是字面量 "session"、"send" 的地方，只在 SendMessage 里；
//  2. SendMessage 的第二个参数类型是 lease.Permit（签名由 reflect 核对，改掉就红）；
//  3. SendMessage 里对许可的 Begin 调用（核对并登记在途），出现在那次 exec 之前。
//
// 边界：它按字面量认命令名，拼出来的 "se"+"nd" 看不见——那种写法同时会被 n037 的守边测试
// （TestCommandArgumentsAreBuiltFromConstantsOnly）拦住，见 boundary-limits.md。
func TestEveryWriteIntoASessionNeedsAPermit(t *testing.T) {
	files, fset := packageASTs(t)
	type site struct {
		fn  string
		pos token.Pos
	}
	var sends []site
	var allowsPos token.Pos
	for _, f := range files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if fn.Name.Name == "SendMessage" && sel.Sel.Name == "Begin" && allowsPos == token.NoPos {
					allowsPos = call.Pos()
				}
				var args []ast.Expr
				switch sel.Sel.Name {
				case "exec":
					args = call.Args[1:]
				case "execWithStdin":
					if len(call.Args) > 2 {
						args = call.Args[2:]
					}
				default:
					return true
				}
				if len(args) >= 2 && strLit(args[0]) == "session" && strLit(args[1]) == "send" {
					sends = append(sends, site{fn.Name.Name, call.Pos()})
				}
				return true
			})
		}
	}
	if len(sends) != 1 || sends[0].fn != "SendMessage" {
		var where []string
		for _, s := range sends {
			where = append(where, s.fn+" @ "+fset.Position(s.pos).String())
		}
		t.Fatalf("往会话写字的上游调用应当恰好一处、在 SendMessage 里，实际：%v", where)
	}
	if allowsPos == token.NoPos || allowsPos > sends[0].pos {
		t.Error("SendMessage 必须在执行 session send 之前核对并登记许可（Permit.Begin）")
	}
	m, ok := reflect.TypeOf(&Client{}).MethodByName("SendMessage")
	if !ok || m.Type.NumIn() < 3 || m.Type.In(2) != reflect.TypeOf(lease.Permit{}) {
		t.Error("SendMessage 的第二个参数必须是 lease.Permit")
	}
	m, ok = reflect.TypeOf(&Client{}).MethodByName("DeliverAnswer")
	if !ok || m.Type.NumIn() < 3 || m.Type.In(2) != reflect.TypeOf(lease.Permit{}) {
		t.Error("DeliverAnswer 的第二个参数必须是 lease.Permit")
	}
}

func strLit(e ast.Expr) string {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return s
}

// 零值许可在 adapter 这一层就被挡住，不会跑到上游。
//
// 上游用 /bin/true：它什么都接受、总是成功。许可检查若被拿掉，投递就会「成功」，测试才红得出来——
// 用 /bin/false 的话命令本身失败，拿掉检查照样返回错误，是假绿。
func TestSendMessageRefusesAZeroPermit(t *testing.T) {
	c, err := NewClient("/bin/true", "_t", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SendMessage(t.Context(), lease.Permit{}, "s1", "x"); err == nil {
		t.Fatal("零值许可必须被拒")
	}
}

// 只有空白的消息和空串一样不投（n039 attempt 4 评审）：许可有效、上游随时能成功，也一次都不调。
func TestSendMessageRefusesBlankText(t *testing.T) {
	dir := t.TempDir()
	called := filepath.Join(dir, "called")
	bin := filepath.Join(dir, "agent-deck")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ntouch \""+called+"\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(bin, "_t", "", "")
	if err != nil {
		t.Fatal(err)
	}
	leases, err := lease.Open(filepath.Join(dir, "leases.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(leases.Close)
	if err := leases.Grant("s1", "测试"); err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{"", " \n\t"} {
		p, err := leases.Automatic("s1")
		if err != nil {
			t.Fatal(err)
		}
		if err := c.SendMessage(t.Context(), p, "s1", msg); err == nil || !strings.Contains(err.Error(), "消息是空的") {
			t.Errorf("消息为 %q 时必须拒绝：%v", msg, err)
		}
	}
	if _, err := os.Stat(called); err == nil {
		t.Error("空白消息不该调到上游")
	}
}
