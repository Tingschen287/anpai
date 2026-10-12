package deckd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// n051 评审 1 B5：deck agent wait 领走后要主 agent 确认才算送达；回应没写出去就退回；久不确认转为未确认并通知 owner。

func reportState(t *testing.T, w *agentWorld) deckstore.Report {
	t.Helper()
	for _, r := range w.store.Reports("alpha") {
		return r
	}
	t.Fatal("没有汇报")
	return deckstore.Report{}
}

func TestWaitClaimNeedsAck(t *testing.T) {
	w := newAgentWorld(t)
	spawnedSub(t, w)
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"做完了"}`)
	code, b := w.do("main", "GET", "/v1/reports/wait?timeout=0", "")
	if code != 200 || !strings.Contains(b, "做完了") {
		t.Fatalf("wait 应领到：%d %s", code, b)
	}
	r := reportState(t, w)
	if r.State != deckstore.ReportSending || r.Claim != deckstore.ClaimWait {
		t.Fatalf("领走后未确认前不算送达：%s %q", r.State, r.Claim)
	}
	if _, b := w.do("main", "GET", "/v1/reports/wait?timeout=0", ""); strings.Contains(b, "做完了") {
		t.Fatal("领走的不能再领一次")
	}
	w.srv.deliverReports(context.Background(), newState())
	if len(w.up.mainSends) != 0 {
		t.Fatal("领走的推送循环不送")
	}
	// 别的主 agent、没领过的 id 都确认不了
	if _, b := w.do("betamain", "POST", "/v1/reports/ack", `{"ids":["`+r.ID+`"]}`); !strings.Contains(b, `"acked":0`) {
		t.Fatalf("别的项目的主 agent 确认不了：%s", b)
	}
	if _, b := w.do("main", "POST", "/v1/reports/ack", `{"ids":["`+r.ID+`"]}`); !strings.Contains(b, `"acked":1`) {
		t.Fatalf("确认收到：%s", b)
	}
	if r := reportState(t, w); r.State != deckstore.ReportDelivered {
		t.Fatalf("确认后送达：%s", r.State)
	}
}

type failingWriter struct{ h http.Header }

func (f *failingWriter) Header() http.Header       { return f.h }
func (f *failingWriter) WriteHeader(int)           {}
func (f *failingWriter) Write([]byte) (int, error) { return 0, errors.New("客户端已断开") }

func TestWaitResponseWriteFailureReleasesClaim(t *testing.T) {
	w := newAgentWorld(t)
	spawnedSub(t, w)
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"做完了"}`)
	req := httptest.NewRequest("GET", "/v1/reports/wait?timeout=0", nil)
	req.Header.Set("X-Test-As", "main")
	w.srv.Handler().ServeHTTP(&failingWriter{h: http.Header{}}, req)
	if r := reportState(t, w); r.State != deckstore.ReportPending || r.Claim != "" {
		t.Fatalf("回应没写出去：退回等送：%s %q", r.State, r.Claim)
	}
	w.srv.deliverReports(context.Background(), newState())
	if len(w.up.mainSends) != 1 {
		t.Fatalf("退回后推送循环照常送：%v", w.up.mainSends)
	}
}

func TestUnackedClaimExpiresToUnconfirmedAndNotifies(t *testing.T) {
	w := newAgentWorld(t)
	spawnedSub(t, w)
	w.do("sub", "POST", "/v1/reports", `{"done":true,"text":"做完了"}`)
	w.do("main", "GET", "/v1/reports/wait?timeout=0", "")
	w.srv.expireClaims(time.Now()) // 还没到时
	if r := reportState(t, w); r.State != deckstore.ReportSending {
		t.Fatalf("没到时不动：%s", r.State)
	}
	w.srv.expireClaims(time.Now().Add(claimTimeout + time.Second))
	r := reportState(t, w)
	if r.State != deckstore.ReportUnconfirmed {
		t.Fatalf("久不确认：转为未确认：%s", r.State)
	}
	found := false
	for _, n := range w.store.Notifications(0, "alpha") {
		found = found || strings.Contains(n.Text, "没有确认收到")
	}
	if !found {
		t.Fatal("应通知 owner")
	}
	if _, b := w.do("main", "POST", "/v1/reports/ack", `{"ids":["`+r.ID+`"]}`); !strings.Contains(b, `"acked":0`) {
		t.Fatalf("转为未确认后不能再确认：%s", b)
	}
	w.srv.deliverReports(context.Background(), newState())
	if len(w.up.mainSends) != 0 {
		t.Fatal("未确认的不重发")
	}
}
