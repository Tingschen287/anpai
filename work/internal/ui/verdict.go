package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// 评审结论（n042，n040 终审断点 6；n044 改由报告文件取）。
//
// 选中 submitted 节点按 v：Work Deck 读这次 attempt 的评审报告文件，取最后一个 VERDICT 行，
// 连同报告的 sha256、一条填好参数的 projectctl review 命令摆出来。评审会话画面里的 VERDICT 只作提示。
// n043 复审实测：画面谁都能往里写字（在评审会话里 printf 一行就行），拿画面当结论来源等于谁写谁算。
// 报告文件同样可能被评审会话之外的进程写，所以界面明说这不是验收事实，哈希给 owner 核对用。
// **Work Deck 不替 owner 记账**：命令由 owner 核对后自己在终端运行。本界面对 Graph 只有派工前的开工一个写入口。

// verdictRe 只认**独占一行**的结论：行首可以有空白、列表符号、引用符或 Markdown 强调，行尾只能有空白、强调或句点。
// 夹在一句话中间的「VERDICT: accept」（例如格式说明、引用别人的话）不算（n042 attempt 1 评审 blocker 2）。
var verdictRe = regexp.MustCompile("(?m)^[\\s•*>`-]*VERDICT:[ \\t]*(accept|rework)[\\s*`.]*$")

// lastVerdict 取画面里最后一个独占一行的 VERDICT 结论；没有就是空。
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
// reportSHA 是 owner 按 v 时报告文件的 sha256，写进理由：记账后报告再被改，对得出来。
func reviewCommand(script string, node projectctl.Node, attempt int, b binding.Binding, verdict, reportSHA string) string {
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
		"--reason", shq(fmt.Sprintf("评审会话 %s 的报告（sha256 %s）结论 VERDICT: %s；由 owner 核对报告后运行本命令记账", b.SessionID, reportSHA, verdict)),
	}, " ")
}

// notAFact 是按 v 时必须摆出来的那句话：界面上的结论不是验收事实。
const notAFact = "这不是验收事实：报告文件和会话画面都可能被评审会话之外的进程写入。记账前打开报告，核对内容与 sha256。"

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
	if m.bindings == nil {
		return fail("没接绑定存储，查不到评审会话")
	}
	b, bound := m.bindings.Get(dispatch.ReviewerKey(node.ID), attempt)
	if !bound {
		return fail("%s attempt %d 还没有 Work Deck 派出的评审会话：先按 d 派评审", node.ID, attempt)
	}
	report := reviewReportPath(node.ID, attempt)
	data, err := os.ReadFile(filepath.Join(m.snap.ProjectRoot, report))
	if err != nil {
		msg := fmt.Sprintf("评审报告 %s 还读不到（%v）：评审还没写完，按 p 看评审会话 %s", report, err, b.SessionID)
		if b.Delivery == binding.DeliveryUnconfirmed {
			msg += "（它的 brief 送达未确认，也可能还停在输入框）"
		}
		return fail("%s", msg)
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	verdict := lastVerdict(string(data))
	if verdict == "" {
		return fail("评审报告 %s（sha256 %s）里没有独占一行的 VERDICT 结论：评审还没写完，或格式不对", report, sha[:12])
	}

	// 画面只作提示：读得到就摆出来，和报告不一致要喊出来；读不到不影响按报告给命令。
	hint := "读不到评审会话画面，只按报告"
	mismatch := ""
	if m.attacher != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		pane, perr := m.attacher.PaneOutputPlain(ctx, b.SessionID)
		cancel()
		switch pv := lastVerdict(pane); {
		case perr != nil:
			hint = fmt.Sprintf("读不到评审会话 %s 的画面（%v），只按报告", b.SessionID, perr)
		case pv == "":
			hint = fmt.Sprintf("评审会话 %s 的画面里没有 VERDICT 行", b.SessionID)
		default:
			hint = fmt.Sprintf("评审会话 %s 的画面里最后一个 VERDICT 行是 %s", b.SessionID, pv)
			if pv != verdict {
				mismatch = fmt.Sprintf("注意：画面写的是 %s，报告写的是 %s。以报告为准，先弄清为什么不一致。\n", pv, verdict)
			}
		}
	}
	info, _ := os.Stat(filepath.Join(m.snap.ProjectRoot, report))
	var d strings.Builder
	fmt.Fprintf(&d, "评审结论（取自报告文件）：%s\n", verdict)
	fmt.Fprintf(&d, "报告：%s（sha256 %s，%d 字节", report, sha, len(data))
	if info != nil {
		fmt.Fprintf(&d, "，修改于 %s", info.ModTime().Format("2006-01-02 15:04:05"))
	}
	fmt.Fprintf(&d, "；评审会话 %s，%s / %s）\n", b.SessionID, b.Model, b.RouteClass)
	fmt.Fprintf(&d, "画面提示（只作提示，不作依据）：%s\n", hint)
	d.WriteString(mismatch)
	d.WriteString(notAFact + "\n")
	d.WriteString("Work Deck 不替你记账。核对后在项目根运行：\n")
	d.WriteString("$ " + reviewCommand(m.qw.script, node, attempt, b, verdict, sha))
	m.detail, m.detailOf = d.String(), "评审结论 "+node.ID
	m.notice, m.noticeOK = fmt.Sprintf("%s 的评审报告写的是 %s（sha256 %s…）。这不是验收事实，记账前核对报告", node.ID, verdict, sha[:12]), true
	return m
}
