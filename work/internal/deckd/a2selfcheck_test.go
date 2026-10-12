package deckd

import (
	"context"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
)

// n051 attempt 2 自查：任务代次去重不能吞掉真正的完成。

// 发话上游没证实（Codex 总是这样）也开新一代：之后的完成照常送到。
func TestUnconfirmedSendStillStartsNewGeneration(t *testing.T) {
	w := newAgentWorld(t)
	sub := spawnedSub(t, w)
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"第一件"}`)
	w.srv.deliverReports(context.Background(), newInferState())
	w.up.sendErr = &agentdeck.ErrDeliveryUnconfirmed{SessionID: sub.AgentDeckID, Detail: "delivery=unverified"}
	if code, b := w.do("main", "POST", "/v1/agents/"+sub.Key+"/send", `{"text":"第二件"}`); code != 202 {
		t.Fatalf("没证实：202：%d %s", code, b)
	}
	w.up.sendErr = nil
	if x, _ := w.store.Session(sub.Key); x.Gen() != 2 {
		t.Fatalf("没证实也开新一代：%d", x.Gen())
	}
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"第二件做完"}`)
	w.srv.deliverReports(context.Background(), newInferState())
	if len(w.up.mainSends) != 2 || !strings.Contains(w.up.mainSends[1], "第二件做完") {
		t.Fatalf("之后的完成应送到：%v", w.up.mainSends)
	}
}

// 额度未知且包络也超了：同一条决定写明两个原因（这条批准随后也会被当作超额例外用掉）。
func TestQuotaGateDecisionNamesEnvelopeToo(t *testing.T) {
	w := newAgentWorld(t) // 默认包络 frontier 0 个
	code, b := w.do("main", "POST", "/v1/agents", spawnJSON("frontier", "难题", "t", "qe1", "/tmp/alpha", "", "claude", "claude-opus-5-5"))
	if code != 202 || !strings.Contains(b, "额度未知") || !strings.Contains(b, "超出预算包络") {
		t.Fatalf("决定应写明两个原因：%d %s", code, b)
	}
}
