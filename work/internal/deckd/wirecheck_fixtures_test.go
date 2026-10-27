package deckd

import "github.com/Tingschen287/terminal-deck/work/internal/wirecheck"

// 现有测试里「上游说的话」「用户起的名字」这类中文夹具，登记为外部原文（见 internal/wirecheck）。
// 它们是测试替身塞进去的，不是本项目写的句子；运行时保险不拿它们当本地中文。
// 客户端自己写进请求体的文字不用登记，钩子会自动当外部原文。每条后面写它出自哪个测试。
func init() {
	wirecheck.External(
		"projectctl 超时", // graph_test.go：替身 projectctl 返回的错误
		"没了",            // center_rework_test.go：会话标题夹具
		"子",             // center_rework_test.go：会话标题夹具
		"进程已消失",         // server_test.go：测试里的调用者身份错误
		"核对不了",          // bypass_test.go：替身进程核对返回的错误
		"上游投递失败（其实送到了）", // a3_test.go：替身上游发话返回的错误
		"标题", "子 agent", // n060_fix2_test.go：直接喂给 stopLaunched 的名字夹具
		"上游投递失败",           // agents_test.go：替身上游发话返回的错误
		"进程 42 的命令行带着绕过参数", // bypass_test.go：替身进程核对返回的错误
	)
}
