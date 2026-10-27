package agentdeck

import "github.com/Tingschen287/terminal-deck/work/internal/wirecheck"

// 现有测试里中文夹具登记为外部原文（见 internal/wirecheck）：测试替身塞进去的文字，不是本项目写的句子。
func init() {
	wirecheck.External(
		"做点什么", // argallow_test.go：codex 位置参数提示词夹具
	)
}
