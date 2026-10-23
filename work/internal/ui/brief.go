package ui

import (
	"fmt"
	"strings"

	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

// executorBrief 写一份执行 brief：派出去的执行会话开工时看到的第一段话。
//
// n039 attempt 3 评审指出：界面派执行者时从不带任务文本，起来的会话是空的，界面却说「已派」——
// 和 reviewBrief 当初要修的是同一个洞。brief 只说节点号、第几次、去哪里读合同、按什么规矩交，
// 合同全文让执行者自己用 projectctl brief 读，不在这里抄第二份。不放凭据。
//
// 项目根、控制器的完整命令和这次的 actor 都写明（n047）：只说「用控制器」时，n045 五轮的执行者
// 每一轮都去上级目录翻找控制器、猜 actor，越出了读范围。actor 必须是这次开工记进账本的那个，
// 否则 submit 被控制器拒。不知道的值留成 <...>，让人一眼看出缺什么，不编。
func executorBrief(node projectctl.Node, attempt int, root, script, actor string) string {
	or := func(v, placeholder string) string {
		if strings.TrimSpace(v) == "" {
			return placeholder
		}
		return shq(v)
	}
	ctl := "cd " + or(root, "<项目根>") + " && " + projectctl.PythonForText() + " " + or(script, "<控制器脚本路径>")

	var b strings.Builder
	fmt.Fprintf(&b, "你是 %s 的执行者，这是第 %d 次 attempt。\n", node.ID, attempt)
	if node.Title != "" {
		fmt.Fprintf(&b, "节点：%s。\n", node.Title)
	}
	if node.RouteClass != "" {
		fmt.Fprintf(&b, "节点档位要求：%s。\n", node.RouteClass)
	}
	if strings.TrimSpace(root) != "" {
		fmt.Fprintf(&b, "项目根：%s。\n", root)
	}
	b.WriteString("\n")
	b.WriteString("开工前：\n")
	b.WriteString("- 读项目根的 AGENTS.md，按里面的规矩做。\n")
	fmt.Fprintf(&b, "- 读 %s 的节点合同，逐条对照 acceptance_criteria：\n  %s brief %s\n", node.ID, ctl, shq(node.ID))
	b.WriteString("\n")
	b.WriteString("约束：\n")
	b.WriteString("- 只在项目根之内、按节点合同的 read_scope 读。要读项目根以外的东西，先停下来问，不要自己去找。\n")
	b.WriteString("- 只改节点合同允许的写入范围。\n")
	b.WriteString("- 不要读取或复制任何凭据正文。\n")
	b.WriteString("- 你不能验收自己的产出：不要运行 review，评审由另一个会话做。\n")
	b.WriteString("\n")
	b.WriteString("做完后提交证据。actor 原样照抄，它是这次开工记进账本的执行者；摘要换成你的一句话：\n")
	fmt.Fprintf(&b, "  %s submit %s --actor %s --summary %s\n", ctl, shq(node.ID), or(actor, "<开工时的 actor>"), shq("<一句话说明这次做了什么>"))
	return b.String()
}

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
	// n045 attempt 2 的评审写完报告就自己运行了 review，账本先于 owner 核对落了验收（n047）。
	b.WriteString("- 不要运行 projectctl 的 review、submit 或任何写账本的命令：owner 看过报告后自己记账。\n")
	fmt.Fprintf(&b, "- 评审报告写到项目根下的 %s（只写这一个文件），owner 记账时要引用它。\n", reviewReportPath(node.ID, attempt))
	b.WriteString("- 不要读取或复制任何凭据正文。\n")
	b.WriteString("- 结论只能是 accept 或 rework 之一，写在报告文件的最后一行（owner 按报告记账，不看会话画面），")
	// 格式说明里故意不出现「VERDICT: 结论词」的原样：brief 本身会留在评审会话画面上，
	// 写出原样示例，按 v 时就会被当成结论（n042 attempt 1 评审 blocker 2）。
	b.WriteString("格式：这一行只写三样东西——大写的 VERDICT、紧跟一个英文冒号和一个空格、再写 accept 或 rework，前后不加别的字。\n")
	b.WriteString("- 判 rework 时逐条列 blocker，每条给出位置与复现方式。\n")
	return b.String()
}
