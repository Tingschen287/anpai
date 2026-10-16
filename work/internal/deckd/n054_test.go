package deckd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

// n054（n051 评审 4 B1）：一条推断提醒等送时被账本提醒换掉，账本提醒送达；子 agent 再跑、再停下，
// 这段安静开始在账本提醒到达之前，就不该再补送推断提醒。对照：没有旧提醒、账本提醒新建一行时同样只送账本提醒一条。
func TestLedgerReplacingHintSuppressesLaterInference(t *testing.T) {
	for _, replace := range []bool{false, true} {
		name := map[bool]string{false: "对照_账本提醒新建一行", true: "账本提醒换掉等送的推断提醒"}[replace]
		t.Run(name, func(t *testing.T) {
			w, _ := graphWorld(t)
			code, b := w.do("main", "POST", "/v1/agents", spawnGraphJSON("balanced", "节点活", "做 n099", map[bool]string{false: "n054-new", true: "n054-replace"}[replace], "/tmp/alpha", "n099_x", "claude", "claude-sonnet-5-5"))
			if code != 201 {
				t.Fatalf("Graph 通道开子 agent：%d %s", code, b)
			}
			var out struct{ Session deckstore.Session }
			json.Unmarshal([]byte(b), &out)
			sub := out.Session
			st := newState()
			state := func(status string) {
				w.srv.activity = &activityCache{byTmx: map[string]string{sub.TmuxSession: status}, at: time.Now(), now: time.Now}
			}
			if replace {
				t0 := time.Now()
				state("running")
				w.srv.inferCompletions(st, t0.Add(-2*time.Minute))
				state("waiting")
				w.srv.inferCompletions(st, t0.Add(-61*time.Second))
				w.srv.inferCompletions(st, t0)
				if pendingCount(w) != 1 {
					t.Fatal("旧推断提醒应在等送")
				}
			}
			time.Sleep(3 * time.Millisecond)
			state("running")
			w.srv.inferCompletions(st, time.Now())
			state("waiting")
			quiet := time.Now()
			w.srv.inferCompletions(st, quiet)
			time.Sleep(3 * time.Millisecond)
			w.srv.agents.graphState = func(context.Context, deckstore.ProjectDef, string) (projectctl.State, error) {
				return projectctl.StateSubmitted, nil
			}
			w.srv.graphSubmits(context.Background(), st, time.Now())
			w.srv.deliverReports(context.Background(), st)
			if len(w.up.mainSends) != 1 || !strings.Contains(w.up.mainSends[0], "账本提醒") {
				t.Fatalf("应先送一条账本提醒：%v", w.up.mainSends)
			}
			w.srv.inferCompletions(st, quiet.Add(61*time.Second))
			w.srv.deliverReports(context.Background(), st)
			if len(w.up.mainSends) != 1 {
				t.Fatalf("这段安静里已到过账本提醒，不该再补送推断提醒：%v", w.up.mainSends)
			}
		})
	}
}
