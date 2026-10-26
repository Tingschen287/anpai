package deckui

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// n060 段 3 返工 4（Codex 复审 3 的 R3-1、R3-2）。
// 反例来自 4.eval/results/n060_ui_i18n/stage-3/review-3/audit_settings_test.go，在这里改成正式测试（不用只在 n067 才有的包）。

// R3-1：成功但比轮询周期慢的静默读取不能被后来的轮询持续作废（Codex 的 TestAuditS3R3SlowSuccessfulLoadEventuallyAligns）。
// 真实的 Unix HTTP 服务、真实的 DeckSettingsAPI，走 tick → syncLangCmd → Update → Batch 这条真实的定时链：
// 另一个请求已经让进程采用了英文，设置还是中文、代数 7；/v1/trust 每次 2.3 秒后成功（比 2 秒的轮询周期慢，比 5 秒的上限快）。
func TestN060S3Fix4SlowSuccessfulLoadEventuallyAligns(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	m, api := n057Fixture()
	m.set.UILang = i18n.ZH
	m.langAt = i18n.ZH
	authority, _ := cloneSettings(api.set)
	authority.UILang = i18n.EN
	authority.Generation = 40
	var reads, completed atomic.Int32
	sock := shortSock(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(i18n.LangHeader, i18n.EN)
		switch r.URL.Path {
		case "/v1/lang":
			json.NewEncoder(w).Encode(map[string]string{"lang": i18n.EN})
		case "/v1/settings":
			reads.Add(1)
			json.NewEncoder(w).Encode(authority)
		case "/v1/trust":
			select {
			case <-time.After(2300 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
			completed.Add(1)
			json.NewEncoder(w).Encode([]deckstore.TrustGrant{})
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
	})}
	go srv.Serve(ln)
	defer srv.Close()
	m.API = DeckSettingsAPI{C: deckclient.NewUI(sock).FollowLang()}
	i18n.SetLang(i18n.EN) // 一次普通的取工具响应已经让进程采用了新语言
	events := make(chan tea.Msg, 100)
	dispatch := func(c tea.Cmd) {
		if c != nil {
			go func() { events <- c() }()
		}
	}
	_, first := m.Update(langSyncedMsg{})
	dispatch(first)
	deadline := time.NewTimer(8500 * time.Millisecond)
	defer deadline.Stop()
	for {
		select {
		case msg := <-events:
			if b, ok := msg.(tea.BatchMsg); ok {
				for _, c := range b {
					dispatch(c)
				}
				continue
			}
			_, c := m.Update(msg)
			dispatch(c)
			if m.set.Generation == 40 && m.uiLang() == i18n.EN {
				if n := reads.Load(); n != 1 {
					t.Errorf("同一时刻只该有一份静默读取，对齐用了 %d 次读取", n)
				}
				if m.quietBusy {
					t.Error("回包处理完之后在途标记应清掉")
				}
				return
			}
		case <-deadline.C:
			t.Fatalf("成功的读取被后来的轮询持续作废：发出 %d 次，完成 %d 次，quietSeq=%d langAt=%q 进程=%q 设置=%q 代数=%d",
				reads.Load(), completed.Load(), m.quietSeq, m.langAt, i18n.Lang(), m.set.UILang, m.set.Generation)
		}
	}
}

// fastTick 把轮询周期缩到很短：测试里 Update(langSyncedMsg) 返回的命令一执行就要等一个周期。
func fastTick(t *testing.T) {
	t.Helper()
	old := settingsLangEvery
	settingsLangEvery = time.Millisecond
	t.Cleanup(func() { settingsLangEvery = old })
}

// quietPending 发出一份静默读取（语言没对上、设置干净），返回读取命令；此时在途标记应是开着的。
func quietPending(t *testing.T, m *Settings) tea.Cmd {
	t.Helper()
	_, next := m.Update(langSyncedMsg{})
	b, ok := next().(tea.BatchMsg)
	if !ok {
		t.Fatal("语言没对上、设置干净：应排出静默读取")
	}
	if !m.quietBusy {
		t.Fatal("静默读取发出之后应记下在途")
	}
	return b[len(b)-1]
}

// noQuietRead 说明这一拍轮询没有发新的静默读取，也没有加序号。
func noQuietRead(t *testing.T, m *Settings, why string) {
	t.Helper()
	seq := m.quietSeq
	_, next := m.Update(langSyncedMsg{})
	if b, ok := next().(tea.BatchMsg); ok && len(b) > 1 {
		t.Errorf("%s：不该再发新的静默读取", why)
	}
	if m.quietSeq != seq {
		t.Errorf("%s：不该增加序号（%d → %d）", why, seq, m.quietSeq)
	}
}

// R3-1：有在途读取时轮询不发新读取、不加序号；回包处理完（接受）才允许下一次。
func TestN060S3Fix4OnlyOneQuietReadInFlight(t *testing.T) {
	fastTick(t)
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	m, api := n057Fixture()
	api.set.UILang, api.set.Generation = "en", 40
	i18n.SetLang(i18n.EN)
	read := quietPending(t, m)
	first := m.quietSeq
	for i := 0; i < 5; i++ { // 慢读取还在路上，轮询连着来几拍
		noQuietRead(t, m, "在途")
	}
	m.Update(read()) // 回包被接受
	if m.quietBusy || m.set.Generation != 40 || m.set.UILang != "en" || m.langAt != "en" {
		t.Fatalf("在途读取的回包应被接受并清掉在途标记：busy=%v gen=%d lang=%q at=%q", m.quietBusy, m.set.Generation, m.set.UILang, m.langAt)
	}
	noQuietRead(t, m, "已对齐")
	if m.quietSeq != first {
		t.Errorf("对齐之后序号不该再动：%d → %d", first, m.quietSeq)
	}
}

// R3-1：保留上轮全部失效保护——编辑、保存、按 R、冲突之后到的旧回包照旧丢弃，owner 的新值留在本地；
// 丢弃之后回到干净状态要能再读（在途标记不能因为丢弃而一直开着）。
func TestN060S3Fix4DiscardedReplyClearsInFlightAndRereads(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	for _, how := range []string{"edit-then-save", "reload-R", "conflict"} {
		t.Run(how, func(t *testing.T) {
			fastTick(t)
			i18n.SetLang(i18n.ZH)
			m, api := n057Fixture()
			api.set.UILang, api.set.Generation = "en", 40
			i18n.SetLang(i18n.EN)
			old := quietPending(t, m)
			oldMsg := old()
			m.sub = &subView{kind: subAgent, agent: "codex", cur: 0}
			before := m.agentPtr("codex").Models[0].Tier
			_, save := m.Update(tea.KeyMsg{Type: tea.KeySpace}) // 在途读取期间 owner 改了一处
			edited := m.agentPtr("codex").Models[0].Tier
			if edited == before || save == nil || !m.dirty {
				t.Fatal("改动没有生效")
			}
			switch how {
			case "edit-then-save":
				m.Update(oldMsg) // 旧回包：丢弃
				if m.agentPtr("codex").Models[0].Tier != edited || !m.dirty {
					t.Fatal("旧回包覆盖了新编辑")
				}
				if m.quietBusy {
					t.Fatal("被丢弃的回包也要清掉在途标记")
				}
				run(m, save) // 保存成功，回到干净
			case "reload-R":
				m.Update(oldMsg)
				if m.quietBusy {
					t.Fatal("被丢弃的回包也要清掉在途标记")
				}
				api.set.UILang, api.set.Generation = "en", 41
				fresh := m.load().(loadedMsg)
				fresh.force = true
				m.Update(fresh) // owner 按 R：丢掉改动重读
				run(m, save)    // 路上那次保存回来：按 R 之后它不再算数
				i18n.SetLang(i18n.EN)
				m.langAt = "zh" // 让对齐检查再次触发
			case "conflict":
				api.fail = &deckclient.Error{Status: 409, Message: "other window"}
				run(m, save)
				if !m.conflict || !m.dirty {
					t.Fatal("应是冲突、改动仍在本地")
				}
				m.Update(oldMsg)
				if m.agentPtr("codex").Models[0].Tier != edited {
					t.Fatal("冲突之后旧回包覆盖了新值")
				}
				if m.quietBusy {
					t.Fatal("被丢弃的回包也要清掉在途标记")
				}
				api.fail = nil
				api.set.UILang, api.set.Generation = "en", 42
				fresh := m.load().(loadedMsg)
				fresh.force = true
				m.Update(fresh) // R 解除冲突
				m.langAt = "zh"
			}
			if !m.clean() {
				t.Fatalf("应回到干净状态：dirty=%v saving=%v pending=%v conflict=%v", m.dirty, m.saving, m.pending, m.conflict)
			}
			seq := m.quietSeq
			_, next := m.Update(langSyncedMsg{})
			b, ok := next().(tea.BatchMsg)
			if !ok || len(b) < 2 || m.quietSeq != seq+1 || !m.quietBusy {
				t.Fatalf("丢弃之后回到干净状态，下一拍要能再读：ok=%v seq %d → %d busy=%v", ok, seq, m.quietSeq, m.quietBusy)
			}
		})
	}
}

// R3-1：在途读取失败后下一拍能重读（失败回包要清掉在途标记）。
func TestN060S3Fix4FailedQuietReadClearsInFlight(t *testing.T) {
	fastTick(t)
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	m, api := n057Fixture()
	api.set.UILang, api.set.Generation = "en", 40
	i18n.SetLang(i18n.EN)
	read := quietPending(t, m)
	failed := read().(loadedMsg)
	failed.err = errors.New("temporary")
	noQuietRead(t, m, "失败回包还没到")
	m.Update(failed)
	if m.quietBusy {
		t.Fatal("失败的回包要清掉在途标记")
	}
	if m.langAt == "en" || m.set.Generation == 40 {
		t.Fatal("失败的读取不该推进对齐")
	}
	again := quietPending(t, m) // 下一拍：能重读
	m.Update(again())
	if m.set.Generation != 40 || m.langAt != "en" || m.quietBusy {
		t.Fatalf("重读之后应对齐：gen=%d at=%q busy=%v", m.set.Generation, m.langAt, m.quietBusy)
	}
}

// hangAPI 的读取一直卡到上下文到期（模拟 deckd 不回话）；到期后返回上下文的错误。
type hangAPI struct {
	*fakeSettingsAPI
	hang *atomic.Bool
}

func (h hangAPI) Load(ctx context.Context) (deckstore.Settings, []deckstore.TrustGrant, error) {
	if h.hang.Load() {
		<-ctx.Done()
		return deckstore.Settings{}, nil, ctx.Err()
	}
	return h.fakeSettingsAPI.Load(ctx)
}

// R3-1：在途读取超时（load 的 5 秒上限）也要清掉在途标记，下一拍能重读（真实等 5 秒）。
func TestN060S3Fix4TimedOutQuietReadClearsInFlight(t *testing.T) {
	fastTick(t)
	if testing.Short() {
		t.Skip("要真实等 5 秒的读取上限")
	}
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	m, api := n057Fixture()
	api.set.UILang, api.set.Generation = "en", 40
	var hang atomic.Bool
	m.API = hangAPI{fakeSettingsAPI: api, hang: &hang}
	i18n.SetLang(i18n.EN)
	hang.Store(true)
	read := quietPending(t, m)
	start := time.Now()
	reply := read()
	if d := time.Since(start); d < 4*time.Second || d > 7*time.Second {
		t.Fatalf("读取应在 5 秒上限处结束：%v", d)
	}
	lm := reply.(loadedMsg)
	if lm.err == nil || !errors.Is(lm.err, context.DeadlineExceeded) {
		t.Fatalf("应是超时的错误：%v", lm.err)
	}
	m.Update(lm)
	if m.quietBusy {
		t.Fatal("超时的回包要清掉在途标记")
	}
	if m.set.Generation == 40 {
		t.Fatal("超时的读取不该改设置")
	}
	hang.Store(false)
	again := quietPending(t, m)
	m.Update(again())
	if m.set.Generation != 40 || m.langAt != "en" {
		t.Fatalf("超时之后下一拍重读应对齐：gen=%d at=%q", m.set.Generation, m.langAt)
	}
}

// R3-2：工具版本是 deckd 已经 Clean 过的文字（deckd/tools.go），里面的标记是真打码，不能被入口中和抹掉；
// 同一实例中英文分别显示 [已打码] / [redacted]。同一画面里外部原始文字（路径）里伪造的标记仍被中和。
func TestN060S3Fix4ToolVersionKeepsGenuineRedaction(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	m, _ := n057Fixture()
	m.width, m.height = 160, 40
	version := deckstore.Clean("tool 1.2 token=abcdefghijklmnop", 80) // 生产（getTools）的清洗形状
	if !i18n.HasMark(version) {
		t.Fatal("夹具应带真打码标记")
	}
	forged := "/opt/" + i18n.RedactMark + "/bin/codex" // 外部原始路径里的伪造标记
	m.tools = []ToolInfo{{Tool: "codex", Name: "Codex", Binary: "codex", Installed: true, Path: forged, Version: version}}
	views := map[string]func() string{
		"agent": func() string { m.sub = &subView{kind: subAgent, agent: "codex"}; v, _ := m.agentView(); return v },
		"add": func() string {
			m.sub = &subView{kind: subAdd}
			m.set.Agents = nil
			v, _ := m.addView()
			return v
		},
	}
	for name, view := range views {
		for _, lang := range []string{i18n.ZH, i18n.EN} {
			i18n.SetLang(lang)
			v := view()
			want := "[redacted]"
			if lang == i18n.ZH {
				want = "[已打码]"
			}
			if !strings.Contains(v, "tool 1.2") || !strings.Contains(v, want) {
				t.Errorf("%s/%s：工具版本的真打码应显示 %s：\n%s", name, lang, want, v)
			}
			// 外部路径里伪造的标记被中和成替换字符（只有「加入」页显示路径）；版本里的真打码不能变成替换字符
			wantNeutral := 0
			if name == "add" {
				wantNeutral = 1
			}
			if n := strings.Count(v, "�����"); n != wantNeutral {
				t.Errorf("%s/%s：替换字符应有 %d 处（路径里伪造的标记），实际 %d 处，版本里的真打码被中和了：\n%s", name, lang, wantNeutral, n, v)
			}
			if strings.Count(v, want) != 1 {
				t.Errorf("%s/%s：只有版本里的 1 处真打码，路径里伪造的标记不能显示成记号（共 %d 处）：\n%s", name, lang, strings.Count(v, want), v)
			}
		}
	}
}

// R3-1 自查：在途读取期间按 R 但 R 本身失败（没有改任何计数），旧静默回包仍有效；无论接受还是丢弃，在途标记都不能一直开着。
func TestN060S3Fix4FailedForcedReloadDoesNotStickInFlight(t *testing.T) {
	fastTick(t)
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	m, api := n057Fixture()
	api.set.UILang, api.set.Generation = "en", 40
	i18n.SetLang(i18n.EN)
	read := quietPending(t, m)
	old := read()
	m.Update(loadedMsg{force: true, err: errors.New("R failed")})
	if !m.quietBusy {
		t.Fatal("R 失败不是那份静默读取的回包，在途标记应还开着")
	}
	noQuietRead(t, m, "R 失败之后静默读取仍在路上")
	m.Update(old)
	if m.quietBusy {
		t.Fatal("静默回包到了，在途标记要清掉")
	}
	if m.set.Generation != 40 || m.langAt != "en" {
		t.Fatalf("R 没有改任何状态，旧静默回包仍应被接受：gen=%d at=%q", m.set.Generation, m.langAt)
	}
}

// 序号判断仍是第二道防线（Codex 复审 3 的 TestAuditS3R3QuietFailureRetryAndSequenceBoundary 改成新规则下能构造的形状）：
// 同一时刻只有一份在途，所以要用「放弃前一份的在途标记」来构造两份读取；序号跨过 MaxInt 回绕时，较早的回包仍不能被接受，
// 失败的回包不推进对齐，下一拍重读能对齐，对齐之后不再读。
func TestN060S3Fix4SequenceBoundaryStillDiscardsOlder(t *testing.T) {
	fastTick(t)
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	m, api := n057Fixture()
	m.set.UILang = i18n.ZH
	m.langAt = i18n.ZH
	i18n.SetLang(i18n.EN)
	m.quietSeq = int(^uint(0)>>1) - 1
	older := quietPending(t, m)().(loadedMsg)
	api.set.UILang = i18n.EN
	api.set.Generation = 60
	m.quietBusy = false                       // 放弃较早的那份
	newer := quietPending(t, m)().(loadedMsg) // 序号从 MaxInt 回绕到 MinInt，仍和较早的那份不同
	m.Update(older)
	if m.set.Generation == 60 {
		t.Fatal("较早序号的回包被接受了")
	}
	failed := newer
	failed.err = errors.New("temporary")
	m.Update(failed)
	if m.langAt != i18n.ZH || m.set.Generation == 60 || m.quietBusy {
		t.Fatalf("失败的回包不该推进对齐，并且要清标记：at=%q gen=%d busy=%v", m.langAt, m.set.Generation, m.quietBusy)
	}
	m.Update(quietPending(t, m)())
	if m.langAt != i18n.EN || m.set.Generation != 60 {
		t.Fatalf("重读应对齐：at=%q gen=%d", m.langAt, m.set.Generation)
	}
	noQuietRead(t, m, "已对齐")
}
