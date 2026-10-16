package agentdeck

import (
	"strings"
	"testing"
)

// n040 终审断点 3：上游对 shell 这类没有模型的工具不接受 --model；反过来给有模型的工具省掉 --model，
// 等于让上游替你挑默认模型。两种冲突都在派工前拒绝并说清楚怎么改；认不出的自定义工具交给上游。
func TestToolModelContractIsCheckedBeforeLaunch(t *testing.T) {
	cases := []struct {
		tool string
		na   bool
		want string
	}{
		{"shell", false, "model_not_applicable"},
		{"claude", true, "替你挑默认模型"},
		{"codex", true, "替你挑默认模型"},
		{"shell", true, ""},
		{"claude", false, ""},
		{"my-custom-tool", false, ""},
		{"my-custom-tool", true, ""},
	}
	for _, c := range cases {
		err := CheckToolModel(c.tool, c.na)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s na=%v 不该被拒：%v", c.tool, c.na, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s na=%v 应当被拒并提到 %q：%v", c.tool, c.na, c.want, err)
		}
	}
	// 请求层也核：shell 带模型的请求不出门。
	if err := (LaunchRequest{ProjectPath: "/tmp", Tool: "shell", Model: "shell-fixture"}).Validate(); err == nil {
		t.Error("shell 带 --model 的 launch 请求必须在出门前被拒")
	}
}
