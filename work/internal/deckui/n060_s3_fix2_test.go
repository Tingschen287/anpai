package deckui

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 段 3 返工 2：M1 已显示的提示切换后跟着换语言；M2 设置进程持续跟随语言、迟到的旧响应撤销不了新语言。

// shortSock 给一个够短的 unix socket 路径（macOS 上 sockaddr_un 只有 104 字节）。
func shortSock(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "n6")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "d.sock")
}

// codedErr 是一条 deckd 回的带码错误（中文原文 + 码）：英文界面按码翻译。
func codedErr() error {
	return &deckclient.Error{Status: 409, Message: i18n.MustTextZH("err.viewstate.notopen"), Code: "err.viewstate.notopen"}
}

// M1：设置页底部的提示、保存失败的原因、新建项目表单的报错、模型输入框的提示——
// 屏幕上还留着的时候语言变了，下一帧就是新语言。每个场景先确认中文下真的显示着一条提示（有汉字的那一行），
// 再切到英文，整个画面没有汉字。
func TestN060S3Fix2SettingsPromptsFollowLanguageSwitch(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	type scenario struct {
		name  string
		setup func(t *testing.T) *Settings
	}
	cases := []scenario{
		{"cancel-new-project", func(t *testing.T) *Settings {
			m, _ := n057Fixture()
			goLangRow(t, m)
			press(m, "n", "esc")
			return m
		}},
		{"reload-confirm", func(t *testing.T) *Settings {
			m, _ := n057Fixture()
			m.dirty = true
			press(m, "R")
			return m
		}},
		{"quit-unsaved", func(t *testing.T) *Settings {
			m, _ := n057Fixture()
			m.dirty = true
			press(m, "q")
			return m
		}},
		{"revoke-confirm", func(t *testing.T) *Settings {
			m, _ := settingsFixture()
			m.switchPage(pageTrust)
			press(m, "d")
			return m
		}},
		{"load-fail", func(t *testing.T) *Settings {
			m := &Settings{API: &fakeSettingsAPI{}}
			m.Update(loadedMsg{err: codedErr()})
			return m
		}},
		{"save-fail", func(t *testing.T) *Settings {
			m, _ := n057Fixture()
			m.dirty, m.saving = true, true
			m.saveDone(doneMsg{save: true, err: &deckclient.Error{Status: 502, Message: i18n.MustTextZH("err.viewstate.notopen"), Code: "err.viewstate.notopen"}, edits: m.edits})
			return m
		}},
		{"save-conflict", func(t *testing.T) *Settings {
			m, _ := n057Fixture()
			m.dirty, m.saving = true, true
			m.saveDone(doneMsg{save: true, err: &deckclient.Error{Status: http.StatusConflict, Message: "changed"}, edits: m.edits})
			return m
		}},
		{"form-error", func(t *testing.T) *Settings {
			m, _ := n057Fixture()
			dir := t.TempDir()
			m.set.Projects[0].Path = dir
			m.form = newProjectForm(dir)
			m.choose(dir) // 该目录已是项目，表单底部报错
			return m
		}},
		{"model-input-note", func(t *testing.T) *Settings {
			m, _ := n057Fixture()
			goRow(t, m, rowGlobal, "claude", "")
			press(m, "enter", "+")
			if m.sub == nil || m.sub.input == nil {
				t.Fatal("前提：应打开了输入框")
			}
			return m
		}},
		{"tools-fail", func(t *testing.T) *Settings {
			m, _ := n057Fixture()
			m.Update(toolsMsg{err: codedErr()})
			return m
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			i18n.SetLang(i18n.ZH)
			m := c.setup(t)
			m.width, m.height = 100, 40
			zh := plainView(m)
			if _, bad := hanIn(zh); !bad {
				t.Fatalf("前提：中文下应显示着一条提示：\n%s", zh)
			}
			i18n.SetLang(i18n.EN) // 同一个实例、提示还在屏幕上：语言变了（侧栏、保存、别的窗口……任何来源）
			en := plainView(m)
			if r, bad := hanIn(en); bad {
				t.Errorf("切到英文后提示还是中文 %q：\n%s", r, en)
			}
			i18n.SetLang(i18n.ZH)
			if back := plainView(m); back != zh {
				t.Errorf("切回中文后应与切换前一致：\n--- 前\n%s\n--- 后\n%s", zh, back)
			}
		})
	}
}

// M1：侧栏起主 agent 的结果与报错（loop 里存 12 秒）切语言后跟着换。
func TestN060S3Fix2SidebarResultFollowsLanguageSwitch(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	cases := map[string]struct {
		res StartMainResult
		err error
	}{
		"error":   {err: codedErr()},
		"ok":      {},
		"pending": {res: StartMainResult{Pending: "cost"}},
		"note":    {res: StartMainResult{Note: i18n.MustTextZH("err.viewstate.notopen"), NoteCode: "err.viewstate.notopen"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			i18n.SetLang(i18n.ZH)
			l := &loop{}
			sayStartMain(l, "demo", c.res, c.err)
			zh := l.merge(Data{})
			if _, bad := hanIn(zh.Err + zh.Info); !bad {
				t.Fatalf("前提：中文下应有提示：%+v", zh)
			}
			i18n.SetLang(i18n.EN)
			en := l.merge(Data{})
			if r, bad := hanIn(en.Err + en.Info); bad {
				t.Errorf("侧栏的提示留着中文 %q：%+v", r, en)
			}
			if en.Err+en.Info == "" {
				t.Errorf("提示还没过期，不该消失：%+v", en)
			}
			// 操作出错（act）的 6 秒提示同理
			l2 := &loop{api: failingViewAPI{err: codedErr()}, kick: make(chan struct{}, 1)}
			i18n.SetLang(i18n.ZH)
			l2.act("open", "x")
			if _, bad := hanIn(l2.merge(Data{}).Err); !bad {
				t.Fatalf("前提：act 出错中文下应有提示")
			}
			i18n.SetLang(i18n.EN)
			if r, bad := hanIn(l2.merge(Data{}).Err); bad {
				t.Errorf("act 的报错留着中文 %q", r)
			}
		})
	}
}

type failingViewAPI struct {
	API
	err error
}

func (f failingViewAPI) View(context.Context, string, string) error { return f.err }

// M2：迟到的取工具响应（发出时 deckd 的语言头还是旧语言）不能把刚存上的新语言改回去。
func TestN060S3Fix2LateToolsResponseDoesNotRevertSavedLanguage(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	sock := shortSock(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(i18n.LangHeader, "zh") // 慢请求在换语言之前就被处理了，头是那一刻的旧语言
		if r.URL.Path == "/v1/tools" {
			close(started)
			<-release
			w.Write([]byte(`[]`))
			return
		}
		w.Write([]byte(`{"generation":8,"ui_lang":"en"}`))
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	api := DeckSettingsAPI{C: deckclient.NewUI(sock).FollowLang()}
	done := make(chan struct{})
	go func() { defer close(done); api.Tools(context.Background()) }()
	<-started
	// owner 离开还在加载的子视图，进「通用」页保存英文
	gen, err := api.Save(context.Background(), deckstore.Settings{Generation: 7, UILang: "en"})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := n057Fixture()
	m.set.Generation, m.set.UILang = 7, "en"
	m.saveDone(doneMsg{save: true, gen: 7, saved: gen, lang: "en"})
	if i18n.Lang() != i18n.EN {
		t.Fatal("前提：保存之后应已是英文")
	}
	close(release)
	<-done // 慢的取工具响应此时才回来
	if i18n.Lang() != i18n.EN {
		t.Errorf("迟到的取工具响应把刚存上的语言改回去了：%s", i18n.Lang())
	}
}

// M2（Codex 审查 1 的 TestAuditS3SettingsHasNoFollowupFetch）：读完设置之后，设置进程要有下一步命令（定时对语言），
// 不能读完就再也不向 deckd 问语言。只用 Init / Update 看，不依赖具体怎么实现。
func TestN060S3Fix2SettingsHasAFollowupCommandAfterLoad(t *testing.T) {
	_, api := n057Fixture()
	loads := 0
	m := &Settings{API: &countingLoadAPI{fakeSettingsAPI: api, loads: &loads}, Installed: func(string) bool { return true }} // 刚启动的设置进程
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("没有初始读取")
	}
	_, next := m.Update(cmd())
	if loads != 1 {
		t.Fatalf("读取 %d 次", loads)
	}
	if next == nil {
		t.Error("读完设置之后没有后续命令：设置进程闲着时再也不会向 deckd 对语言")
	}
}

type countingLoadAPI struct {
	*fakeSettingsAPI
	loads *int
}

func (a *countingLoadAPI) Load(ctx context.Context) (deckstore.Settings, []deckstore.TrustGrant, error) {
	*a.loads++
	return a.fakeSettingsAPI.Load(ctx)
}
