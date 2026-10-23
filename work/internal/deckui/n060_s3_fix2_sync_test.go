package deckui

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 段 3 返工 2，M2（续）：设置进程空闲时的定时对语言。用到返工 2 新加的 SyncLang 接口与 tick，所以和旧代码上能编译的测试分开放。

// langSyncAPI 是带「对语言」能力的假设置接口：每次对时把本进程语言设成 lang（模拟 deckd 回的响应头）。
type langSyncAPI struct {
	*fakeSettingsAPI
	loads, syncs *atomic.Int32
	lang         *atomic.Value
}

func (a langSyncAPI) Load(ctx context.Context) (deckstore.Settings, []deckstore.TrustGrant, error) {
	a.loads.Add(1)
	return a.fakeSettingsAPI.Load(ctx)
}

func (a langSyncAPI) SyncLang(context.Context) error {
	a.syncs.Add(1)
	i18n.SetLang(a.lang.Load().(string))
	return nil
}

// M2：设置进程读完设置之后持续跟 deckd 对语言；别的窗口换了语言，闲着的设置窗口跟上并重画。
func TestN060S3Fix2SettingsFollowsLanguageWhileIdle(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	old := settingsLangEvery
	settingsLangEvery = time.Millisecond
	t.Cleanup(func() { i18n.SetLang(i18n.ZH); settingsLangEvery = old })
	_, base := n057Fixture()
	var loads, syncs atomic.Int32
	var lang atomic.Value
	lang.Store("zh")
	m := &Settings{API: langSyncAPI{fakeSettingsAPI: base, loads: &loads, syncs: &syncs, lang: &lang}, Installed: func(string) bool { return true }} // 刚启动
	m.width, m.height = 100, 40
	cmd := m.Init()
	_, next := m.Update(cmd())
	if loads.Load() != 1 || next == nil {
		t.Fatalf("读完设置之后应开始定时对语言：读 %d 次，下一步 %v", loads.Load(), next)
	}
	step := func(c tea.Cmd) tea.Cmd { // 走一步：取消息、交给 Update、返回下一步
		t.Helper()
		_, n := m.Update(c())
		return n
	}
	// 一轮：tick → 对语言 → 结果；语言没变，不重读
	n := step(next) // langTickMsg → 对语言
	n = step(n)     // langSyncedMsg → 再排一个 tick
	if syncs.Load() != 1 || n == nil || loads.Load() != 1 {
		t.Fatalf("第一轮：对 %d 次，重读 %d 次", syncs.Load(), loads.Load())
	}
	// 别的窗口把语言换成了英文
	lang.Store("en")
	n = step(n)
	n2 := step(n) // langSyncedMsg{changed}：重画 + 静默重读
	if i18n.Lang() != i18n.EN {
		t.Fatalf("闲着的设置进程没有跟上别处换的语言：%s", i18n.Lang())
	}
	if r, bad := hanIn(plainView(m)); bad {
		t.Errorf("语言变了之后设置页还有汉字 %q", r)
	}
	// 没有未存的改动：顺手静默重读（让「界面语言」那一行和代数跟上）
	if n2 == nil {
		t.Fatal("语言变了应有后续命令")
	}
	batch, ok := n2().(tea.BatchMsg)
	if !ok {
		t.Fatalf("应是 tick + 静默重读：%T", n2())
	}
	reloaded := false
	for _, c := range batch {
		if lm, isLoaded := c().(loadedMsg); isLoaded {
			reloaded = true
			m.Update(lm)
		}
	}
	if !reloaded || loads.Load() != 2 {
		t.Errorf("没有未存的改动时应静默重读设置：重读 %d 次", loads.Load())
	}
	// 之后再读一次设置（按 R、静默重读、刷新授权列表）不开新的链：同时只有一条在转
	if _, again := m.Update(loadedMsg{set: base.set}); again != nil {
		t.Error("再读一次设置不该再开一条定时链，不然读几次就有几条链同时在对语言")
	}
	// 有未存的改动时不重读（不能丢掉 owner 正在改的东西）
	m.dirty = true
	lang.Store("zh")
	n = step(m.langTick()) // 新排一个 tick（tea.Tick 的命令只能执行一次）
	_, nn := m.Update(n())
	if nn == nil {
		t.Fatal("仍应继续定时对语言")
	}
	if b, ok := nn().(tea.BatchMsg); ok {
		for _, c := range b {
			if _, isLoaded := c().(loadedMsg); isLoaded {
				t.Error("有未存的改动时不该静默重读")
			}
		}
	}
	if i18n.Lang() != i18n.ZH {
		t.Errorf("应跟上换回中文：%s", i18n.Lang())
	}
}
