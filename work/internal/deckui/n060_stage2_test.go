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
	"agents.go · (pkg)":                    "busySubText 发给主 agent，给模型读，合同不改",
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
}

// TestOwnerFacingChineseIsCoded：deckd、deckstore、agentdeck、deckclient 里，
// 没登记的函数不能再留中文字面量。登记的函数必须还留着中文，并且写了理由。
func TestOwnerFacingChineseIsCoded(t *testing.T) {
	hits := map[string][]string{}
	walkProd(t, func(path string, f *ast.File, _ *token.FileSet) {
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
