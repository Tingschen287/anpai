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
	allowsPos := map[string]token.Pos{}
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
				if sendFuncs[fn.Name.Name] && sel.Sel.Name == "Begin" && allowsPos[fn.Name.Name] == token.NoPos {
					allowsPos[fn.Name.Name] = call.Pos()
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
	// 往会话写字的上游调用只许出现在 sendFuncs 里，每个函数恰好一处（n051 加 SendToMain：转告主 agent 带 --defer-if-busy）
	per := map[string][]site{}
	var where []string
	for _, s := range sends {
		per[s.fn] = append(per[s.fn], s)
		where = append(where, s.fn+" @ "+fset.Position(s.pos).String())
	}
	if len(sends) != len(sendFuncs) {
		t.Fatalf("往会话写字的上游调用应当恰好 %d 处（%v 各一处），实际：%v", len(sendFuncs), sendFuncs, where)
	}
	for name := range sendFuncs {
		if len(per[name]) != 1 {
			t.Fatalf("%s 里应恰好一处 session send，实际：%v", name, where)
		}
		if allowsPos[name] == token.NoPos || allowsPos[name] > per[name][0].pos {
			t.Errorf("%s 必须在执行 session send 之前核对并登记许可（Permit.Begin）", name)
		}
		m, ok := reflect.TypeOf(&Client{}).MethodByName(name)
		if !ok || m.Type.NumIn() < 3 || m.Type.In(2) != reflect.TypeOf(lease.Permit{}) {
			t.Errorf("%s 的第二个参数必须是 lease.Permit", name)
		}
	}
	m, ok := reflect.TypeOf(&Client{}).MethodByName("DeliverAnswer")
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
	// 光看「有错」不够（n042）：/bin/true 空输出现在会报「送达未确认」，拿掉许可检查也照样有错。
	// 要看的是错来自许可，而且上游根本没被调用。
	if err := c.SendMessage(t.Context(), lease.Permit{}, "s1", "x"); err == nil || !strings.Contains(err.Error(), "没有投递许可") || IsDeliveryUnconfirmed(err) {
		t.Fatalf("零值许可必须在调上游之前被拒：%v", err)
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

// n040 终审断点 5：上游 session send --json 报 delivery=typed（到了画面、没确认提交）时，
// adapter 报「送达未确认」，不当成普通失败；typed_not_submitted 等其余失败仍是失败；成功就是成功。
func TestSendMessageReportsUnconfirmedDeliverySeparately(t *testing.T) {
	cases := []struct {
		name, out string
		code      int
		unconf    bool
		ok        bool
	}{
		{"typed", `{"success":false,"delivery":"typed","submitted":false,"error":"message reached 'x' but was never confirmed submitted","code":"DELIVERY_FAILED"}`, 1, true, false},
		{"typed_not_submitted", `{"success":false,"delivery":"typed_not_submitted","submitted":false,"error":"message typed but not submitted","code":"DELIVERY_FAILED"}`, 1, false, false},
		{"no_evidence", `{"success":false,"delivery":"no_evidence","error":"not delivered"}`, 1, false, false},
		{"非 JSON", `Error: boom`, 1, false, false},
		{"成功", `{"success":true,"delivery":"submitted","submitted":true}`, 0, false, true},
		// 上游退出 0 但提交未知（n042 attempt 2 评审 blocker）：不是送达。
		{"成功退出但 unverified", `{"success":true,"delivery":"unverified","submitted":false}`, 0, true, false},
		{"成功退出但没有 submitted", `{"success":true}`, 0, true, false},
		{"成功退出但不是 JSON", `Sent message`, 0, true, false},
	}
	for _, c := range cases {
		dir := t.TempDir()
		argsFile := filepath.Join(dir, "args")
		bin := filepath.Join(dir, "agent-deck")
		script := "#!/bin/sh\necho \"$@\" > \"" + argsFile + "\"\ncat >/dev/null\ncat <<'JSON'\n" + c.out + "\nJSON\nexit " + strconv.Itoa(c.code) + "\n"
		if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		cl, _ := NewClient(bin, "_t", "", "")
		leases, _ := lease.Open(filepath.Join(dir, "leases.json"))
		leases.Grant("s1", "测试")
		p, _ := leases.Automatic("s1")
		err := cl.SendMessage(t.Context(), p, "s1", "继续")
		leases.Close()
		if got := IsDeliveryUnconfirmed(err); got != c.unconf {
			t.Errorf("%s：送达未确认=%v，期望 %v（%v）", c.name, got, c.unconf, err)
		}
		if (err == nil) != c.ok {
			t.Errorf("%s：成功=%v，期望 %v（%v）", c.name, err == nil, c.ok, err)
		}
		if args, _ := os.ReadFile(argsFile); !strings.Contains(string(args), "--json") {
			t.Errorf("%s：session send 要带 --json 才读得到 delivery：%s", c.name, args)
		}
	}
}

// sendFuncs 是允许往会话写字的函数：每个都必须先核对登记许可。
var sendFuncs = map[string]bool{"SendMessage": true, "SendToMain": true}
