package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/queue"
)

var t0 = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

type fixedQuota map[string]Quota

func (f fixedQuota) QuotaOf(model string, now time.Time) Quota {
	if q, ok := f[model]; ok {
		return q
	}
	return Quota{State: QuotaUnknown, Source: "测试：没有观测"}
}

func avail() Quota {
	at := t0
	return Quota{State: QuotaAvailable, ObservedAt: &at, Source: "测试观测"}
}

func factor(d Decision, model string) Factor {
	for _, f := range d.Candidates {
		if f.Model == model {
			return f
		}
	}
	return Factor{}
}

// 能力位只看显式声明：同一个工具的两个模型，一个写了、一个没写，没写的就是 unknown。
func TestCapabilitiesAreNeverInferredFromTheToolName(t *testing.T) {
	cands := []Candidate{
		{ID: "a", Tool: "claude", Class: "frontier", Caps: map[string]string{"resume": Yes}},
		{ID: "b", Tool: "claude", Class: "frontier"},
	}
	d := Decide(Request{RequiredClass: "frontier", RequiredCaps: []string{"resume"}}, cands, fixedQuota{"a": avail(), "b": avail()}, t0)
	if factor(d, "b").Capabilities["resume"] != Unknown {
		t.Errorf("没声明的能力必须是 unknown，不从工具名推断：%+v", factor(d, "b"))
	}
	if factor(d, "b").Recommendable || !factor(d, "b").Selectable {
		t.Error("能力未知的候选可以让 owner 选，但不能自动推荐")
	}
	if d.Recommended != "a" {
		t.Errorf("应当推荐能力已声明满足的 a，实际 %q", d.Recommended)
	}
}

// 额度未知：不推荐，升级给 owner，不用默认值填。
func TestUnknownQuotaEscalatesToOwner(t *testing.T) {
	cands := []Candidate{{ID: "a", Tool: "claude", Class: "frontier", Caps: map[string]string{"resume": Yes}}}
	d := Decide(Request{RequiredClass: "frontier", RequiredCaps: []string{"resume"}}, cands, fixedQuota{}, t0)
	if d.Recommended != "" || !d.Escalated {
		t.Fatalf("额度未知时不该推荐：%+v", d)
	}
	if len(d.Unknowns) == 0 || !strings.Contains(strings.Join(d.Unknowns, ","), "额度未知") {
		t.Errorf("未知项要记下来：%v", d.Unknowns)
	}
	if !strings.Contains(d.Reason, "不用默认值填补") {
		t.Errorf("理由要说清为什么不推荐：%q", d.Reason)
	}
}

// 全部已知才推荐，推荐满足条件里档位最低的。
func TestRecommendsTheCheapestFullyKnownCandidate(t *testing.T) {
	cands := []Candidate{
		{ID: "big", Tool: "claude", Class: "frontier"},
		{ID: "mid", Tool: "kimi", Class: "balanced"},
	}
	d := Decide(Request{RequiredClass: "balanced"}, cands, fixedQuota{"big": avail(), "mid": avail()}, t0)
	if d.Recommended != "mid" || d.Escalated {
		t.Errorf("应当推荐够用的 balanced：%+v", d)
	}
}

// 不降档：低于节点档位的候选不可选，owner 也选不了。
func TestRoutingNeverLowersTheRouteClass(t *testing.T) {
	cands := []Candidate{{ID: "cheap", Tool: "kimi", Class: "balanced"}}
	d := Decide(Request{RequiredClass: "frontier"}, cands, fixedQuota{"cheap": avail()}, t0)
	f := factor(d, "cheap")
	if f.Selectable || f.MeetsRouteClass || !strings.Contains(f.Excluded, "不降档") {
		t.Fatalf("低档候选必须被排除：%+v", f)
	}
	if err := d.Choose("cheap", "owner"); err == nil {
		t.Error("owner 选低档也必须被拒")
	}
}

// 声明不具备所需能力、或观测到额度耗尽：排除。
func TestDeclaredNoAndExhaustedQuotaExclude(t *testing.T) {
	cands := []Candidate{
		{ID: "nocap", Tool: "x", Class: "frontier", Caps: map[string]string{"mcp": No}},
		{ID: "dry", Tool: "y", Class: "frontier", Caps: map[string]string{"mcp": Yes}},
	}
	at := t0
	q := fixedQuota{"nocap": avail(), "dry": {State: QuotaExhausted, ObservedAt: &at, Source: "测试：卡在额度上"}}
	d := Decide(Request{RequiredClass: "frontier", RequiredCaps: []string{"mcp"}}, cands, q, t0)
	if factor(d, "nocap").Selectable || factor(d, "dry").Selectable {
		t.Errorf("两者都该被排除：%+v", d.Candidates)
	}
	if !d.Escalated {
		t.Error("没有可选候选时要升级给 owner")
	}
}

// 额度只来自 Work Deck 自己读到的会话结果：卡在额度上 → 耗尽；正常完成 → 可用；
// 出错、等待、失败的完成都不说明额度；超过 6 小时的观测不算。
func TestQuotaComesOnlyFromWorkbenchObservations(t *testing.T) {
	// seen 同时当作上游记录的发生时间；LastSeen 一律设成「刚才」，模拟每轮轮询都再读到它。
	sess := func(id string, cat queue.Category, done string, seen time.Time) queue.Item {
		at := seen
		return queue.Item{Category: cat, LastSeen: t0, Source: queue.Source{Kind: queue.SourceSession, SessionID: id, DoneStatus: done, ReportedAt: &at}}
	}
	items := []queue.Item{
		sess("s-limit", queue.CatSessionUsageLimit, "", t0.Add(-time.Hour)),
		sess("s-ok", queue.CatSessionFinished, "ok", t0.Add(-time.Hour)),
		sess("s-fail", queue.CatSessionFinished, "fail", t0.Add(-time.Hour)),
		sess("s-err", queue.CatSessionError, "", t0.Add(-time.Hour)),
		sess("s-old", queue.CatSessionFinished, "ok", t0.Add(-7*time.Hour)),
		sess("s-foreign", queue.CatSessionFinished, "ok", t0),
		// 没有发生时间的记录不折算。
		{Category: queue.CatSessionFinished, LastSeen: t0, Source: queue.Source{Kind: queue.SourceSession, SessionID: "s-notime", DoneStatus: "ok"}},
	}
	modelOf := map[string]string{"s-limit": "m-limit", "s-ok": "m-ok", "s-fail": "m-fail", "s-err": "m-err", "s-old": "m-old", "s-notime": "m-notime"}
	obs := Observed{Obs: ObservationsFromQueue(items, func(id string) (string, bool) { m, ok := modelOf[id]; return m, ok })}
	for model, want := range map[string]string{
		"m-limit": QuotaExhausted, "m-ok": QuotaAvailable, "m-fail": QuotaUnknown,
		"m-err": QuotaUnknown, "m-old": QuotaUnknown, "m-none": QuotaUnknown, "m-notime": QuotaUnknown,
	} {
		if got := obs.QuotaOf(model, t0).State; got != want {
			t.Errorf("%s：额度应当是 %s，实际 %s", model, want, got)
		}
	}
}

// 每条记录都写明额度为什么不取 CLI 自报，并引用 n026 的实测。
func TestEveryDecisionCitesN026(t *testing.T) {
	d := Decide(Request{RequiredClass: "frontier"}, nil, nil, t0)
	for _, must := range []string{"n026", "8 个样本只有 4 个", "三个数量级", "不可依赖"} {
		if !strings.Contains(d.QuotaBasis, must) {
			t.Errorf("额度依据缺 %q：%s", must, d.QuotaBasis)
		}
	}
}

// 审计记录是一行一条的 JSON，写不进去返回错误。
func TestAppendWritesOneLineOrFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routing.jsonl")
	d := Decide(Request{NodeID: "n1", RequiredClass: "frontier"}, []Candidate{{ID: "a", Tool: "t", Class: "frontier"}}, nil, t0)
	d.Choose("a", "owner")
	if err := Append(path, d); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var back Decision
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &back); err != nil || back.Chosen != "a" || back.ChosenBy != "owner" {
		t.Errorf("读回的记录不对：%v %+v", err, back)
	}
	if err := Append("", d); err == nil {
		t.Error("没有路径时必须报错")
	}
}

// 配置里的能力声明：键只认全集，值只认 yes/no。
func TestCapabilityDeclarationsAreValidated(t *testing.T) {
	if err := ValidateCaps(map[string]string{"resume": Yes, "mcp": No}); err != nil {
		t.Errorf("合法声明被拒：%v", err)
	}
	for name, bad := range map[string]map[string]string{
		"键不认识": {"telepathy": Yes}, "值不认识": {"resume": "maybe"}, "写 unknown": {"resume": Unknown},
	} {
		if err := ValidateCaps(bad); err == nil {
			t.Errorf("%s：应当报错", name)
		}
	}
	if err := ValidateRequired([]string{"resume", "nope"}); err == nil {
		t.Error("不认识的能力要求应当报错")
	}
}

// 选择理由单独记：采纳推荐、没有推荐由 owner 挑、owner 改选，三种说法不同。
func TestChoiceReasonIsRecordedSeparatelyFromTheRecommendation(t *testing.T) {
	cands := []Candidate{{ID: "a", Tool: "t", Class: "frontier"}, {ID: "b", Tool: "t", Class: "frontier"}}
	q := fixedQuota{"a": avail()}
	d := Decide(Request{RequiredClass: "frontier"}, cands, q, t0)
	if d.Recommended != "a" {
		t.Fatalf("前提：应当推荐 a，实际 %q", d.Recommended)
	}
	took := d
	took.Choose("a", "owner")
	over := d
	over.Choose("b", "owner")
	none := Decide(Request{RequiredClass: "frontier"}, cands, nil, t0)
	none.Choose("b", "owner")
	for name, c := range map[string]struct {
		d    Decision
		want string
	}{
		"采纳": {took, "采纳了推荐"}, "改选": {over, "没有采纳推荐的 a，改选 b"}, "无推荐": {none, "没有推荐"},
	} {
		if !strings.Contains(c.d.ChoiceReason, c.want) || c.d.ChosenBy != "owner" {
			t.Errorf("%s：选择理由 %q、选择者 %q", name, c.d.ChoiceReason, c.d.ChosenBy)
		}
	}
	if strings.Contains(over.Reason, "改选") {
		t.Error("Reason 只解释推荐，不该混进选择理由")
	}
}
