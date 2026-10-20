package deckui

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/panels"
)

// TestEmptyKeyOnRenderRecordsMiss：map 里没有的状态取出零值键。
// 检查器不报这种来源。渲染时缺键记录必须不是空的。
func TestEmptyKeyOnRenderRecordsMiss(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	stop := i18n.RecordMisses()
	panels.RenderActivity([]deckstore.Session{{
		Project: "demo", Role: "sub", Title: "Work", Agent: "claude", Status: "not-a-status",
	}}, time.Date(2026, 10, 14, 15, 0, 0, 0, time.UTC))
	miss := stop()
	for _, k := range miss {
		if k == "" {
			return
		}
	}
	t.Fatalf("空键应记入缺键，实际 %q", miss)
}

// ownerChineseAllow 是第 2、3、4 类中文的例外。键是「文件 · 函数」，值是理由。
// 按函数登记，不按整个文件放过。登记了但函数里已经没有中文，测试失败。
var ownerChineseAllow = map[string]string{
	"agents.go · (pkg)":                    "busySubText 仍留给模型读；owner 的冲突响应另带 err.deckd.busysubtext",
	"agents.go · subBrief":                 "brief，合同不改",
	"agents.go · MainBrief":                "brief，合同不改",
	"reports.go · Server.reportText":       "给模型读的报告正文，合同不改",
	"reports.go · Server.inferCompletions": "给模型读的推断提醒，合同不改",
	"reports.go · Server.getReportsWait":   "成功回执里留给模型的确认说明；失败提示已改成码",
	"reports.go · Server.graphSubmits":     "给模型读的账本提醒；同函数里给 owner 的状态已改成码",
	"nested.go · NestingAdvice":            "只给旧界面 internal/ui 用，不经 deckd，本段不改",
	"manifest.go · (pkg)":                  "给评审看的证明说明，不进界面",
	"manifest.go · EffectKind.String":      "给评审看的证明说明，不进界面",
	"manifest.go · Effect.String":          "给评审看的证明说明，不进界面",
	"manifest.go · FuncRef.String":         "给评审看的证明说明，不进界面",
	"viewstate.go · (pkg)":                 "哨兵 codeErr 的中文正文必须留在源码里，和码表逐字相同",
	"viewstate.go · ErrPartial.Error":      "Error() 拼出来的中文必须和 I18n() 逐字相同",
	"viewstate.go · ErrHandback.Error":     "Error() 拼出来的中文必须和 I18n() 逐字相同",

	// cmd/deck（返工 4）。deck center 自己画在中间窗格里的字（fence、Pause）已经改成码，不在这里；
	// 下面这些要么只写日志，要么是命令行输出（命令行输出的边界：打在调用者终端，不属于界面）。
	"up.go · fence":            "只写 center.log 的日志行；画在中间窗格的提示已改成 center.fence.waiting 和 err.deck.fence.*",
	"up.go · announce":         "只写 center.log 的日志行（client 已连上，界面看不到）",
	"up.go · up":               "deck up 的 flag 说明和失败说明，打在 owner 启动命令的终端 stderr，布局建好之前",
	"up.go · ensureDeckd":      "deck up 的失败说明（stderr），同上",
	"up.go · buildLayout":      "deck up 的失败说明（stderr），同上",
	"up.go · layoutCmd":        "deck layout 子命令的用法，由 tmux 快捷键在后台调用，输出不进窗格",
	"up.go · socketFlags":      "flag 说明",
	"main.go · daemon":         "deckd 前台进程的 flag 说明和启动横幅，跑在布局的后台窗口里，等同日志",
	"main.go · usage":          "deck 命令行用法（stderr）",
	"main.go · panel":          "deck panel 的 flag 说明和用法（stderr）",
	"main.go · settings":       "deck settings -check 的命令行输出和 flag 说明（升级前自查用，不需要 deckd）",
	"agent.go · agentCmd":      "deck agent 命令行：给模型和 owner 的 shell 看的 stderr 和 flag 说明，错误正文是中文 err.Error()，合同不改",
	"agent.go · agentSpawn":    "同上：flag 说明",
	"agent.go · agentEscalate": "同上：flag 说明",
	"agent.go · agentUsage":    "同上：用法",
	"agent.go · needArg":       "同上：缺参数的失败说明",
	"agent.go · projectCmd":    "同上：deck project 的用法和 flag 说明",
	"agent.go · reportsCmd":    "同上：deck reports 的用法",
}

// TestOwnerFacingChineseIsCoded：deckd、deckstore、agentdeck、deckclient、viewstate 里，
// 没登记的函数不能再留中文字面量。登记的函数必须还留着中文，并且写了理由。
// viewstate 按函数登记哨兵正文，不整包排除。
func TestOwnerFacingChineseIsCoded(t *testing.T) {
	hits := map[string][]string{}
	walkDirs(t, ownerScanDirs(), func(path string, f *ast.File, _ *token.FileSet) {
		base := filepath.Base(path)
		walkStack(f, func(n ast.Node, stack []ast.Node) {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return
			}
			v, err := strconv.Unquote(lit.Value)
			if err != nil || !cjk(v) {
				return
			}
			hits[base+" · "+ownerFuncLabel(stack)] = append(hits[base+" · "+ownerFuncLabel(stack)], v)
		})
	})
	for site, lits := range hits {
		if ownerChineseAllow[site] == "" {
			t.Errorf("%s 还有中文字面量，没有按函数登记：%q", site, lits[0])
		}
	}
	for site, why := range ownerChineseAllow {
		if why == "" {
			t.Errorf("%s 的例外没有写理由", site)
		}
		if len(hits[site]) == 0 {
			t.Errorf("%s 已经没有中文，例外过期了", site)
		}
	}
}

func ownerFuncLabel(stack []ast.Node) string {
	for i := len(stack) - 1; i >= 0; i-- {
		fd, ok := stack[i].(*ast.FuncDecl)
		if !ok || fd.Name == nil {
			continue
		}
		if fd.Recv != nil && len(fd.Recv.List) > 0 {
			return ownerRecv(fd.Recv.List[0].Type) + "." + fd.Name.Name
		}
		return fd.Name.Name
	}
	return "(pkg)"
}

func ownerRecv(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return ownerRecv(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return ownerRecv(t.X)
	case *ast.IndexListExpr:
		return ownerRecv(t.X)
	default:
		return "?"
	}
}
