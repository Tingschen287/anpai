package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/binding"
	"github.com/Tingschen287/terminal-deck/work/internal/dispatch"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

// 评审结论（n042，n040 终审断点 6）。
//
// 选中 submitted 节点按 v：Work Deck 读这次 attempt 的评审会话画面，取最后一个 VERDICT 行，
// 连同报告文件在不在、一条填好参数的 projectctl review 命令摆出来。
// **Work Deck 不替 owner 记账**：命令由 owner 核对后自己在终端运行。本界面对 Graph 只有派工前的开工一个写入口。

var verdictRe = regexp.MustCompile(`VERDICT:\s*(accept|rework)\b`)

// lastVerdict 取画面里最后一个 VERDICT 行的结论；没有就是空。
func lastVerdict(pane string) string {
	all := verdictRe.FindAllStringSubmatch(pane, -1)
	if len(all) == 0 {
		return ""
	}
	return all[len(all)-1][1]
}

// reviewReportPath 是评审者要写的报告路径（相对项目根），reviewBrief 里告诉评审者写在这里。
func reviewReportPath(nodeID string, attempt int) string {
	return fmt.Sprintf("4.eval/reviews/%s-attempt%d-workdeck-review.md", nodeID, attempt)
}

// shq 给 shell 参数加单引号。
func shq(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"$`\\;&|<>()*?[]{}!#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// reviewCommand 造一条可复制的 projectctl review 命令。空的必填参数原样留成 <...>，让人一眼看出缺什么。
func reviewCommand(script string, node projectctl.Node, attempt int, b binding.Binding, verdict string) string {
	or := func(v, placeholder string) string {
		if strings.TrimSpace(v) == "" {
			return placeholder
		}
		return shq(v)
	}
	return strings.Join([]string{
		"python3", or(script, "<控制器脚本路径>"), "review", shq(node.ID),
		"--decision", shq(verdict),
		"--reviewer", shq("workdeck-review-" + b.SessionID),
		"--role", or(node.ReviewerRole, "<review_policy.reviewer_role>"),
		"--route-class", or(b.RouteClass, "<route_class>"),
		"--model", or(b.Model, "<model>"),
		"--report", shq(reviewReportPath(node.ID, attempt)),
		"--reason", shq(fmt.Sprintf("评审会话 %s 给出 VERDICT: %s；owner 已在 Work Deck 核对", b.SessionID, verdict)),
	}, " ")
}

// showVerdict 是按 v。
func (m Model) showVerdict() Model {
	node, ok := m.selected()
	if !ok {
		return m
	}
	fail := func(format string, a ...any) Model {
		m.notice, m.noticeOK = fmt.Sprintf(format, a...), false
		return m
	}
	if state, known := node.State.Get(); !known || state != projectctl.StateSubmitted {
		return fail("%s 不是 submitted，没有待记的评审结论", node.ID)
	}
	attempt, known := node.Attempt.Get()
	if !known {
		return fail("%s 不知道是第几次 attempt（%s），对不上评审会话", node.ID, node.Attempt.Reason())
	}
	if m.bindings == nil || m.attacher == nil {
		return fail("没接绑定存储或进入组件，读不了评审会话")
	}
	b, bound := m.bindings.Get(dispatch.ReviewerKey(node.ID), attempt)
	if !bound {
		return fail("%s attempt %d 还没有 Work Deck 派出的评审会话：先按 d 派评审", node.ID, attempt)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	pane, err := m.attacher.PaneOutputPlain(ctx, b.SessionID)
	cancel()
	if err != nil {
		return fail("读不了评审会话 %s 的画面：%v", b.SessionID, err)
	}
	verdict := lastVerdict(pane)
	if verdict == "" {
		msg := fmt.Sprintf("评审会话 %s 的画面里还没有 VERDICT 行：评审还没做完，按 p 看看", b.SessionID)
		if b.Delivery == binding.DeliveryUnconfirmed {
			msg += "（它的 brief 送达未确认，也可能还停在输入框）"
		}
		return fail("%s", msg)
	}
	report := reviewReportPath(node.ID, attempt)
	reportState := "已存在"
	if _, err := os.Stat(filepath.Join(m.snap.ProjectRoot, report)); err != nil {
		reportState = "还不存在：先让评审者写好，或把评审会话的结论另存到这个路径，否则控制器会拒绝"
	}
	var d strings.Builder
	fmt.Fprintf(&d, "评审结论：%s\n", verdict)
	fmt.Fprintf(&d, "来源：评审会话 %s 画面里最后一个 VERDICT 行（%s / %s）\n", b.SessionID, b.Model, b.RouteClass)
	fmt.Fprintf(&d, "报告：%s（%s）\n", report, reportState)
	d.WriteString("Work Deck 不替你记账。核对结论与报告后，在项目根运行：\n")
	d.WriteString("$ " + reviewCommand(m.qw.script, node, attempt, b, verdict))
	m.detail, m.detailOf = d.String(), "评审结论 "+node.ID
	m.notice, m.noticeOK = fmt.Sprintf("%s 的评审结论是 %s，记账命令见详情", node.ID, verdict), true
	return m
}
