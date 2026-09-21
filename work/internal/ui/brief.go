package ui

import (
	"fmt"
	"strings"

	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

// reviewBrief 写一份评审 brief，就是派出去的那个会话开工时看到的第一段话。
//
// 为什么这件事值得一个专门的函数：上一版界面根本没写过这段话——
// 它启动一个会话，然后什么都不说。会话是空的，人以为评审派出去了。
// 独立评审一句话戳破：「所谓派出评审实际只创建空会话」。
//
// brief 里只放节点自己的信息：节点号、第几次、要读什么、结论怎么写。
// 不放任何凭据，也不放本机路径以外的东西。
func reviewBrief(node projectctl.Node, attempt int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "你是 %s 的独立评审者。执行者是另一个会话，不是你。\n\n", node.ID)

	fmt.Fprintf(&b, "这是第 %d 次 attempt 的提交。\n", attempt)
	if actor, ok := node.Actor.Get(); ok {
		fmt.Fprintf(&b, "执行者：%s。\n", actor)
	}
	if node.ReviewIndependent {
		b.WriteString("这个节点要求评审独立于执行者：你不能是执行者本人，也不能在他的会话里评。\n")
	}
	if node.ReviewRouteClass != "" {
		fmt.Fprintf(&b, "评审档位要求：%s。\n", node.ReviewRouteClass)
	}
	b.WriteString("\n")

	b.WriteString("请按节点合同里的 acceptance_criteria 逐条核验，")
	b.WriteString("自己构造场景验证，不要只读测试名和注释。\n\n")

	b.WriteString("约束：\n")
	b.WriteString("- 不要修改任何代码或交付物，你只评审。\n")
	b.WriteString("- 不要读取或复制任何凭据正文。\n")
	b.WriteString("- 结论只能是 accept 或 rework 之一，写在最后一行，")
	b.WriteString("格式：`VERDICT: accept` 或 `VERDICT: rework`。\n")
	b.WriteString("- 判 rework 时逐条列 blocker，每条给出位置与复现方式。\n")
	return b.String()
}
