package deckd

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// n051 承接 n050 评审 4：主 agent 的 SSE 放行与跨项目事件过滤，直接走 HTTP 测。

// streamEvents 以 as 的身份连上 /v1/events，返回收到的 (kind, project) 序列，读够 n 个或超时为止。
func streamEvents(t *testing.T, f *fixture, as, query string, n int, during func()) (int, [][2]string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", f.http.URL+"/v1/events"+query, nil)
	req.Header.Set("X-Test-As", as)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return resp.StatusCode, nil
	}
	go during()
	var got [][2]string
	sc := bufio.NewScanner(resp.Body)
	kind := ""
	for sc.Scan() && len(got) < n {
		line := sc.Text()
		if k, ok := strings.CutPrefix(line, "event: "); ok {
			kind = k
		}
		if d, ok := strings.CutPrefix(line, "data: "); ok && kind != "" {
			var e deckstore.Event
			json.Unmarshal([]byte(d), &e)
			got = append(got, [2]string{kind, e.Project})
			kind = ""
		}
	}
	return 200, got
}

func TestMainAgentSSEOnlySeesOwnProject(t *testing.T) {
	f := newFixture(t)
	during := func() {
		time.Sleep(100 * time.Millisecond)
		f.do("betamain", "POST", "/v1/decisions", `{"kind":"push","summary":"beta 推送"}`)
		f.do("main", "POST", "/v1/decisions", `{"kind":"push","summary":"alpha 推送"}`)
		f.do("betamain", "POST", "/v1/decisions", `{"kind":"cost","summary":"beta 花钱"}`)
		f.do("main", "POST", "/v1/decisions", `{"kind":"config","summary":"alpha 改设置"}`)
	}
	code, got := streamEvents(t, f, "main", "", 2, during)
	if code != 200 {
		t.Fatalf("主 agent 应能订阅 SSE：%d", code)
	}
	if len(got) < 2 {
		t.Fatalf("应收到本项目的两条：%v", got)
	}
	for _, e := range got {
		if e[1] != "alpha" {
			t.Fatalf("主 agent 收到了别的项目的事件：%v", got)
		}
	}
	// owner 不加过滤能看到全部项目
	code, got = streamEvents(t, f, "owner", "", 4, during)
	projects := map[string]bool{}
	for _, e := range got {
		projects[e[1]] = true
	}
	if code != 200 || !projects["alpha"] || !projects["beta"] {
		t.Fatalf("owner 应收到全部项目：%d %v", code, got)
	}
}

func TestSSERefusedForSubUnknownAndOtherProject(t *testing.T) {
	f := newFixture(t)
	for _, as := range []string{"sub", "unknown"} {
		if code, _ := streamEvents(t, f, as, "", 1, func() {}); code != 403 {
			t.Errorf("%s 不能订阅 SSE：%d", as, code)
		}
	}
	if code, _ := streamEvents(t, f, "main", "?project=beta", 1, func() {}); code != 403 {
		t.Errorf("主 agent 不能订阅别的项目：%d", code)
	}
}
