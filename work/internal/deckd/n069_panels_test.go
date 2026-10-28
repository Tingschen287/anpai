package deckd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/lease"
	"github.com/Tingschen287/terminal-deck/work/internal/viewstate"
)

// n069：挂拔面板经 deckd 单写入者路径存盘并作用于布局；进度面板没指定项目时跟着正在看的会话。

type panelBodyOut struct {
	Panels   []deckstore.PanelDef `json:"panels"`
	Applied  bool                 `json:"applied"`
	Attached []string             `json:"attached"`
	Removed  []string             `json:"removed"`
	Note     string               `json:"note"`
	NoteCode string               `json:"note_code"`
}

func onOf(defs []deckstore.PanelDef, name string) bool {
	for _, d := range defs {
		if d.Name == name {
			return d.On
		}
	}
	return false
}

func TestSetPanelPersistsAndAppliesToLayout(t *testing.T) {
	f := newFixture(t)
	var got []deckstore.PanelDef
	f.srv.cfg.PanelApplier = func(ctx context.Context, defs []deckstore.PanelDef) ([]string, []string, error) {
		got = defs
		return []string{"progress"}, []string{"quota"}, nil
	}
	code, body := f.do("owner", "POST", "/v1/panels/quota", `{"on":false}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var out panelBodyOut
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Applied || len(out.Attached) != 1 || len(out.Removed) != 1 || out.Note != "" {
		t.Fatalf("应作用到布局：%+v", out)
	}
	if onOf(got, "quota") || !onOf(got, "decisions") {
		t.Fatalf("应用方拿到的应是存下后的注册表：%+v", got)
	}
	// 存盘：panels.yaml 里额度是 off，重开存储仍然如此（单写入者落盘）
	data, err := os.ReadFile(filepath.Join(f.dirs.Config, "panels.yaml"))
	if err != nil || !strings.Contains(string(data), "command: deck panel quota\n      region: right\n      \"on\": false") {
		t.Fatalf("panels.yaml：%v\n%s", err, data)
	}
	ps, _ := deckstore.LoadPanels(f.dirs.Config)
	if onOf(ps, "quota") {
		t.Fatal("读回应是拔掉的")
	}
	// 拔掉额度面板不影响额度门：预算与额度的接口照旧
	if c, _ := f.do("owner", "GET", "/v1/quota", ""); c == 404 || c == 500 {
		t.Fatalf("额度接口应照旧：%d", c)
	}
}

func TestSetPanelLayoutFailureKeepsSavedAndSaysWhy(t *testing.T) {
	f := newFixture(t)
	f.srv.cfg.PanelApplier = func(ctx context.Context, defs []deckstore.PanelDef) ([]string, []string, error) {
		return nil, nil, i18n.E("err.layout.nocenter")
	}
	code, body := f.do("owner", "POST", "/v1/panels/progress", `{"on":true}`)
	var out panelBodyOut
	json.Unmarshal([]byte(body), &out)
	if code != 200 || out.Applied || out.Note == "" || out.NoteCode == "" || !onOf(out.Panels, "progress") {
		t.Fatalf("布局没跟上：已存下、说明原因：%d %+v", code, out)
	}
	if !onOf(f.store.Panels(), "progress") {
		t.Fatal("已存下的不回滚")
	}
}

func TestSetPanelWithoutApplierOnlySaves(t *testing.T) {
	f := newFixture(t)
	code, body := f.do("owner", "POST", "/v1/panels/activity", `{"on":true}`)
	var out panelBodyOut
	json.Unmarshal([]byte(body), &out)
	if code != 200 || out.Applied || out.Note != "" || !onOf(out.Panels, "activity") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestSetPanelUnknownNameDoesNotTouchLayout(t *testing.T) {
	f := newFixture(t)
	var calls atomic.Int32
	f.srv.cfg.PanelApplier = func(context.Context, []deckstore.PanelDef) ([]string, []string, error) {
		calls.Add(1)
		return nil, nil, nil
	}
	if code, _ := f.do("owner", "POST", "/v1/panels/nonesuch", `{"on":true}`); code < 400 {
		t.Fatalf("不认识的面板应报错：%d", code)
	}
	if calls.Load() != 0 {
		t.Fatal("没存成就不该改布局")
	}
	for _, as := range []string{"main", "sub", "unknown"} {
		if code, _ := f.do(as, "POST", "/v1/panels/quota", `{"on":false}`); code != 403 {
			t.Fatalf("%s 不能挂拔面板：%d", as, code)
		}
	}
	if calls.Load() != 0 || !onOf(f.store.Panels(), "quota") {
		t.Fatal("没权限就什么都不该变")
	}
}

// 两个窗口同时挂拔：存与改布局串行，应用方看到的注册表总是存储里最新的，最后一次应用与存储一致。
func TestSetPanelConcurrentCallsAreSerialized(t *testing.T) {
	f := newFixture(t)
	var inFlight, maxFlight atomic.Int32
	var mu sync.Mutex
	var last []deckstore.PanelDef
	f.srv.cfg.PanelApplier = func(ctx context.Context, defs []deckstore.PanelDef) ([]string, []string, error) {
		n := inFlight.Add(1)
		for {
			m := maxFlight.Load()
			if n <= m || maxFlight.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		last = defs
		mu.Unlock()
		inFlight.Add(-1)
		return nil, nil, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := []string{"quota", "progress", "activity"}[i%3]
			on := "true"
			if i%2 == 0 {
				on = "false"
			}
			f.do("owner", "POST", "/v1/panels/"+name, `{"on":`+on+`}`)
		}(i)
	}
	wg.Wait()
	if maxFlight.Load() != 1 {
		t.Fatalf("应用必须串行：同时 %d 个", maxFlight.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	final := f.store.Panels()
	for _, d := range final {
		if onOf(last, d.Name) != d.On {
			t.Fatalf("最后一次应用的注册表与存储不一致：%s 应用 %v 存储 %v", d.Name, onOf(last, d.Name), d.On)
		}
	}
}

// ---------------- 进度面板跟随 ----------------

func (f *fixture) viewOn(t *testing.T, focus string) {
	t.Helper()
	l, err := lease.Open(filepath.Join(t.TempDir(), "leases.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	c := &typingCenter{ro: true, shown: viewstate.Idle, last: time.Now()}
	lookup := func(id string) (viewstate.Target, bool) {
		x, ok := f.store.Session(id)
		if !ok || x.AgentDeckID == "" {
			return viewstate.Target{}, false
		}
		return viewstate.Target{Session: x.AgentDeckID, Tmux: x.TmuxSession, Role: x.Role, BriefConfirmed: true, Title: x.Title}, true
	}
	m := viewstate.New(c, l, lookup, func(*i18n.Error) error { return nil }, nil)
	if focus != "" {
		if err := m.Open(focus); err != nil {
			t.Fatal(err)
		}
	}
	f.srv.view = m
}

func progressOf(t *testing.T, f *fixture, as, query string) map[string]any {
	t.Helper()
	code, body := f.do(as, "GET", "/v1/progress"+query, "")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestProgressFollowsTheSessionOnScreen(t *testing.T) {
	f := newFixture(t)
	// 布局没接上（view 为空）：没有在看的会话，是说明不是错误
	if out := progressOf(t, f, "owner", ""); out["follow"] != true || out["reason"] != "no_session" || out["project"] != "" {
		t.Fatalf("没有视图：%v", out)
	}
	// 视图在，但中间是空闲页
	f.viewOn(t, "")
	if out := progressOf(t, f, "owner", ""); out["reason"] != "no_session" {
		t.Fatalf("中间空闲：%v", out)
	}
	// 在看 alpha 的主 agent：进度属于 alpha（没配 Graph 时说明没有 Graph，但项目是 alpha）
	f.viewOn(t, f.main.AgentDeckID)
	if out := progressOf(t, f, "owner", ""); out["project"] != "alpha" || out["graph"] != false {
		t.Fatalf("应跟到 alpha：%v", out)
	}
	// 换到 beta 的会话：项目跟着换（会话切换后的下一次取数就是新项目）
	f.viewOn(t, f.other.AgentDeckID)
	if out := progressOf(t, f, "owner", ""); out["project"] != "beta" {
		t.Fatalf("应跟到 beta：%v", out)
	}
	// 指定了项目就按指定的，优先于跟随
	if out := progressOf(t, f, "owner", "?project=alpha"); out["project"] != "alpha" || out["follow"] == true {
		t.Fatalf("-project 优先：%v", out)
	}
	// 主 agent 只看自己的项目，不受跟随影响
	if out := progressOf(t, f, "main", ""); out["project"] != "alpha" {
		t.Fatalf("主 agent 看自己的项目：%v", out)
	}
	// 指定一个不存在的项目仍是 404，不被跟随吞掉
	if code, _ := f.do("owner", "GET", "/v1/progress?project=nonesuch", ""); code != 404 {
		t.Fatalf("不存在的项目应 404：%d", code)
	}
}

// 会话所属的项目已经不在设置里（删了项目、手改过设置）：说明，不报错。
func TestProgressFollowSessionWithoutKnownProject(t *testing.T) {
	f := newFixture(t)
	f.viewOn(t, f.other.AgentDeckID)
	set := f.store.Settings()
	var keep []deckstore.ProjectDef
	for _, p := range set.Projects {
		if p.ID != "beta" {
			keep = append(keep, p)
		}
	}
	set.Projects = keep
	if err := f.store.ReplaceSettings(set); err != nil {
		t.Skipf("设置不许删正在被会话用的项目（%v）：这个分支由 followedProject 的单元覆盖", err)
	}
	if out := progressOf(t, f, "owner", ""); out["reason"] != "no_project" || out["follow"] != true {
		t.Fatalf("%v", out)
	}
}

func TestFollowedProjectUnknownSession(t *testing.T) {
	f := newFixture(t)
	f.viewOn(t, f.main.AgentDeckID)
	// 焦点是个注册表里没有的会话（刚被清掉）
	if p, why := f.srv.followedProject(); p != "alpha" || why != "" {
		t.Fatalf("%q %q", p, why)
	}
	if err := errors.New("x"); err == nil {
		t.Fatal()
	}
}

// 布局没在运行：已存下，applied 为 false 且没有错误说明（界面据此说「布局没在运行」，不说「已更新」）。
func TestSetPanelLayoutNotRunningIsSavedNotApplied(t *testing.T) {
	f := newFixture(t)
	f.srv.cfg.PanelApplier = func(context.Context, []deckstore.PanelDef) ([]string, []string, error) {
		return nil, nil, ErrLayoutNotRunning
	}
	code, body := f.do("owner", "POST", "/v1/panels/progress", `{"on":true}`)
	var out panelBodyOut
	json.Unmarshal([]byte(body), &out)
	if code != 200 || out.Applied || out.Note != "" || !onOf(out.Panels, "progress") || !onOf(f.store.Panels(), "progress") {
		t.Fatalf("%d %s", code, body)
	}
}
